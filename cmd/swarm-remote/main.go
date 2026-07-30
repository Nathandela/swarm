// Command swarm-remote is the gateway sidecar process (slice G2): it dials the
// relay over a machine-authenticated WebSocket, then runs remotegw.Service to
// bridge the daemon's journal to the phone's mailbox (journal-OUT) and forward
// phone commands to the daemon (command-IN) until signalled -- redialling the
// relay on section 6.0's backoff whenever the link dies under it.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/supervise"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// serviceConfigFromParams copies a resolved gatewayParams (slice G1) plus a
// dialed relay Mailbox into a remotegw.ServiceConfig. Forwarder, PollInterval,
// ReconnectDelay, and Now are left zero: remotegw.NewService defaults them.
//
// ServiceConfig.Machine is deliberately NOT set here: the reconcile record must name the
// DAEMON'S endpoint id (the id the phone pairs against and namespaces every session id
// with), which nothing in the provisioned state can produce -- it arrives in the daemon
// hello, and Gateway.RunJournal stamps the sink with it there.
func serviceConfigFromParams(p gatewayParams, mailbox remotegw.Mailbox) remotegw.ServiceConfig {
	return remotegw.ServiceConfig{
		DaemonSocket:   p.DaemonSocket,
		Relay:          mailbox,
		PhoneTarget:    p.PhoneTarget,
		Key:            p.Key,
		WakeKey:        p.WakeKey,
		EpochID:        p.EpochID,
		GrantSeq:       p.GrantSeq,
		RecipientKeyID: p.RecipientKeyID,
		SenderKeyID:    p.SenderKeyID,
		JournalSeq:     p.JournalSeq,
		ReplySeq:       p.ReplySeq,
		PushSeq:        p.PushSeq,
		PushPrefs:      p.PushPrefs,
		Outbox:         p.Outbox,
		Inbound:        p.Inbound,
		StateDir:       p.StateDir,
		DeviceID:       p.DeviceID,
	}
}

// relayConn is the relay surface ONE gateway generation needs. It exists to pin the
// LIVENESS half at COMPILE time: remotegw.Service discovers Done() by type assertion (a
// Mailbox that cannot report liveness is a supported configuration -- every unit-test fake
// is one), so a production client that quietly stopped satisfying remotegw.LinkWatcher
// would not fail to build, it would fail to RECONNECT, silently, in the field.
type relayConn interface {
	remotegw.Mailbox
	remotegw.LinkWatcher
	grantDeliverer
	Close() error
}

// The production relay client is a relayConn. Pinned at compile time.
var _ relayConn = (*relay.Client)(nil)

// run drives remotegw.Service over the relay until ctx is cancelled, REDIALLING whenever
// the link dies (PB-NET-4, ADR-007 D9: "client reconnect backoff + jitter on both hops").
//
// THE DEFECT THIS LOOP CLOSES. The sidecar used to dial once and hold that client for the
// life of the process, and nothing observed it dying: Service.Run's journal loop reconnects
// to the DAEMON, not the relay, and the command loop retried the same dead client forever.
// The process therefore never exited, so neither launchd's KeepAlive{SuccessfulExit:false}
// nor systemd's Restart=on-failure ever fired -- a supervision policy written against EXIT
// cannot restart a zombie. A desktop WiFi blip ended remote control until a human restarted
// the sidecar, while the phone reconnected, reported "online", and nothing appeared in any
// log.
//
// IT IS THE SHAPE App.run ALREADY HAS ON THE PHONE, deliberately: dial, run, observe the
// failure, back off, redial, on ONE schedule that now lives in internal/remote/relay rather
// than as a second set of constants. The two hops differ in exactly one place -- what
// resets the backoff -- and that difference is argued at Service.Progressed.
//
// EACH GENERATION IS REBUILT, not rewired. A fresh Service per connection is what makes the
// reconnect honest: its deferred teardown closes every lease conn and every terminal peek,
// so the daemon is not left holding a control gate for a phone whose own transport has
// severed (the phone severs its input plane on disconnect for the same reason). The durable
// coordinates -- outbox, inbound checkpoint, the three seq sources -- are resolved ONCE, in
// main, and handed to every generation, so a redial resumes rather than restarts.
func run(ctx context.Context, p gatewayParams) error {
	rb := relay.NewReconnectBackoff()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		progressed, err := runGeneration(ctx, p)
		// Shutdown outranks everything: a link that dies as the sidecar is being stopped is
		// the stop, not an outage, and must not be redialled.
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if terminalRelayError(err) {
			return err
		}
		// PB-NET-4's reset, and it is NOT "the dial succeeded" -- see Service.Progressed.
		if progressed {
			rb.Reset()
		}
		delay := rb.Next()
		report(fmt.Sprintf("relay link lost; redialling in %s", delay.Round(time.Millisecond)), err)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// runGeneration dials the relay and drives one Service over that connection until it ends.
// It reports whether traffic crossed the link, which is what the caller's backoff resets on.
func runGeneration(ctx context.Context, p gatewayParams) (progressed bool, err error) {
	// PB-NET-2 on the path production takes (ADR-007 B34/B37). The sidecar's auth_init
	// carries the MACHINE's full relay-auth public key, so a cleartext hop discloses it
	// to any observer; the policy refuses ws:// to everything but a loopback IP literal,
	// decides it from the URL before a socket is opened, and carries the operator's SPKI
	// pin when relay.json configures one.
	client, err := relay.DialSecure(ctx, p.RelayURL, p.RelayAuth, p.RelaySecurity)
	if err != nil {
		return false, fmt.Errorf("dial relay: %w", err)
	}
	defer func() { _ = client.Close() }()

	// C5: authorize the paired device and deliver its sealed epoch grant over the mailbox
	// before the bridge starts (idempotent; the phone dedups by grant seq).
	if err := deliverEpochGrant(ctx, client, p); err != nil {
		return false, fmt.Errorf("deliver epoch grant: %w", err)
	}

	svc := remotegw.NewService(serviceConfigFromParams(p, client))
	// ADR-007 B114's other half: give the stored degraded state a reader. The watcher is
	// JOINED rather than left to a deferred cancel, so this function cannot outlive it.
	wctx, stopWatch := context.WithCancel(ctx)
	watched := make(chan struct{})
	go func() { defer close(watched); watchDegraded(wctx, svc) }()

	err = svc.Run(ctx)
	stopWatch()
	<-watched
	return svc.Progressed(), err
}

// degradedPollInterval is how often the gateway's stored degraded state is consulted. It is
// a poll rather than a callback because all three underlying Err()s are first-error latches:
// the transition it is looking for happens at most once per generation.
const degradedPollInterval = time.Second

// watchDegraded reports the runtime's degraded state the first time it appears, and once
// more at the end of the generation for a state that appeared between two polls.
//
// IT IS THE ONLY READER THE THREE STORED ERRORS HAVE. CommandBridge.Err, RelaySink.Err and
// PushNotifier.Err each latch a first error precisely so a condition that must not fail a
// record is still observable -- a state dir that fails every checkpoint persist and so drops
// every keystroke, a relay that answers no mailbox wait, a wake path that has stopped ringing
// the phone. None of the three had a non-test caller anywhere in the tree, so an operator
// learned nothing about any of them. Reporting is not remediation, and it is not nothing.
func watchDegraded(ctx context.Context, svc *remotegw.Service) {
	t := time.NewTicker(degradedPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := svc.Err(); err != nil {
				report("gateway degraded", err)
			}
			return
		case <-t.C:
			if err := svc.Err(); err != nil {
				report("gateway degraded", err)
				return
			}
		}
	}
}

// terminalRelayError reports the conditions no redial can fix, so the loop returns them to
// main rather than spending the machine's battery and the relay's per-source budget
// re-proving them. Everything else -- a refused quota, an unreachable host, a relay that
// answers nothing, a link that dropped -- is an OUTAGE and is retried on the backoff.
//
// Note what is NOT here: context.DeadlineExceeded. A dial that hits relay.DefaultDialTimeout
// reports exactly that, and it is the single most ordinary transient condition on the list.
// The caller's own ctx is consulted directly instead, which is the only context that means
// "stop".
func terminalRelayError(err error) bool {
	switch {
	case errors.Is(err, remotegw.ErrDeviceRevoked):
		// PB-LIFE-3(c): the owner revoked the paired device, so there is nothing to serve.
		// main turns this into a QUIESCENT exit; only a fresh `swarm remote pair` starts a
		// gateway again, necessarily as a new process under the new epoch.
		return true
	case errors.Is(err, relay.ErrRevoked),
		errors.Is(err, relay.ErrConsentRetired),
		errors.Is(err, relay.ErrConsentMalformed):
		// The relay refuses this identity or this pairing's route consent. The remedy is a
		// new pairing, which is a new process; retrying is a handshake spent re-proving it.
		return true
	case errors.Is(err, relay.ErrPinMismatch),
		errors.Is(err, relay.ErrPinRequired),
		errors.Is(err, relay.ErrPinMalformed),
		errors.Is(err, relay.ErrCleartextRefused):
		// The transport policy refused, and it is decided from relay.json before a socket is
		// opened -- the same file every retry would read, to the same answer. It must reach
		// the operator's log as a failure rather than be buried under a backoff.
		return true
	}
	return false
}

// report writes one operator-facing line to stderr, which is what the systemd/launchd unit
// captures. os.Stderr is read at call time so a test can observe the same channel an
// operator does.
func report(msg string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "swarm-remote: %s: %v\n", msg, err)
		return
	}
	fmt.Fprintf(os.Stderr, "swarm-remote: %s\n", msg)
}

func main() {
	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		var err error
		if stateDir, err = persist.DefaultDir(); err != nil {
			exit(err)
		}
	}
	daemonSocket := os.Getenv(daemon.EnvRemoteSocket)

	// PB-LIFE-3(a): consulted BEFORE resolving params, so a machine with nothing to serve
	// exits quiescent instead of as the provisioning failure resolveGatewayParams reports.
	if err := requireSomethingToServe(stateDir); err != nil {
		exit(err)
	}

	p, err := resolveGatewayParams(stateDir, daemonSocket)
	if err != nil {
		exit(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, p); err != nil && !errors.Is(err, context.Canceled) {
		// PB-LIFE-3(c): a revoke leaves the same nothing-to-serve state as never having
		// paired, so the unit must return to quiescent rather than restart a gateway that
		// would refuse to resolve params anyway. Only a later `swarm remote pair` starts
		// one again -- necessarily a fresh process, under the NEW epoch.
		if errors.Is(err, remotegw.ErrDeviceRevoked) {
			err = fmt.Errorf("%w: %w", err, supervise.ErrQuiescent)
		}
		exit(err)
	}
}

// requireSomethingToServe is PB-LIFE-3(a): the gateway has work only when EXACTLY one
// device is paired -- the count resolveGatewayParams requires. Zero (never paired, or the
// only device revoked) is QUIESCENCE, not a failure, and so is more than one, which
// resolveGatewayParams refuses just as hard.
func requireSomethingToServe(stateDir string) error {
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		return fmt.Errorf("open device registry: %w", err)
	}
	if n := len(reg.List()); supervise.Desired(n) != supervise.StateActive {
		return fmt.Errorf("no paired device to serve (%d paired, want exactly 1): %w", n, supervise.ErrQuiescent)
	}
	return nil
}

// exit reports err and leaves with the status the supervision unit's restart policy is
// written against: quiescence is a SUCCESSFUL status (supervise.ExitQuiescent), so
// neither launchd's KeepAlive{SuccessfulExit:false} nor systemd's Restart=on-failure
// spawns the gateway again every throttle interval for as long as the machine is up.
// Everything else stays a failure and IS restarted. The reason is printed either way: a
// silent success would hide a machine whose gateway is doing nothing.
func exit(err error) {
	fmt.Fprintf(os.Stderr, "swarm-remote: %v\n", err)
	os.Exit(supervise.ExitCodeFor(err))
}
