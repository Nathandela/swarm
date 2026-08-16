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
	"net/http"
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
		Profile:        p.Profile,
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
		PushGateway:    p.PushGateway,
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
	// ADR-015 P9/P12, PG-OBL-8: re-drive whatever non-terminal wake obligation this
	// process finds durable, once per generation (a no-op when the pairing has not
	// migrated off legacy_relay). Riding every redial rather than only the true process
	// start also gives PG-OBL-9's ongoing retry a free ride until a proper backoff driver
	// lands (bd issue agents-tracker-hggx.4.3) -- Drive is idempotent-safe to call again
	// on an obligation that already resolved, so this costs nothing on the common path.
	if err := svc.RedrivePendingWakeObligations(ctx); err != nil {
		report("push gateway: redrive pending wake obligation", err)
	}
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

// revokeHTTPClient builds the revoke producer's HTTP client. A package variable so a
// test can point the drive at its TLS double; production never reassigns it.
var revokeHTTPClient = func() *http.Client { return &http.Client{Timeout: 30 * time.Second} }

// machineRevoke is the shared half of the machine-side revoke PRODUCER (bead
// agents-tracker-u37c; playbook 3.2: "Machine-side device revocation uses the
// machine-revoke capability and retries deletion durably after local epoch rotation").
// With record set it durably registers the obligation to delete this pairing's push
// address BEFORE the first network attempt; without it, it re-drives whatever
// obligation an earlier process left pending, resolved idempotently by the PG-REV-2
// tombstone's 204.
//
// A push-gateway.json without machine_revoke_capability predates the producer: the
// delete cannot be presented, and that is disclosed rather than silently required.
func machineRevoke(stateDir, gatewayURL, capability string, addr remotegw.PushAddress, record bool) {
	// The obligation must be recordable even on a state dir whose remote/ scaffold is
	// not fully provisioned: the record is the durable half of the revoke.
	if err := os.MkdirAll(filepath.Join(stateDir, "remote"), 0o700); err != nil {
		report("machine revoke: create remote state dir", err)
		return
	}
	store, err := remotegw.OpenRevokeObligationStore(
		filepath.Join(stateDir, "remote", "revoke-obligation.json"))
	if err != nil {
		report("machine revoke: open obligation store", err)
		return
	}
	if record {
		// Recorded UNCONDITIONALLY, before the capability check: on a pre-producer
		// provisioning (no machine_revoke_capability yet) the revoke moment still owes
		// the delete, and only a durable obligation lets a later provisioning of the
		// capability drive it. Record needs no revoker.
		m := remotegw.NewRevokeObligationMachine(remotegw.RevokeObligationConfig{
			Store: store, Address: addr,
		})
		if err := m.Record(); err != nil {
			report("machine revoke: record obligation", err)
			return
		}
	} else if _, ok := store.Pending(); !ok {
		return
	}
	if capability == "" {
		report("machine revoke: push-gateway.json carries no machine_revoke_capability; "+
			"the pairing's push address cannot be deleted at the gateway (the obligation "+
			"stays durable for a later provisioning)", nil)
		return
	}
	m := remotegw.NewRevokeObligationMachine(remotegw.RevokeObligationConfig{
		Store: store,
		Revoker: &remotegw.HTTPAddressRevoker{
			BaseURL:                 gatewayURL,
			MachineRevokeCapability: capability,
			Client:                  revokeHTTPClient(),
		},
		Address: addr,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := m.Drive(ctx); err != nil {
		// A retryable failure stays durable for the next start; a terminal refusal
		// resolved the record with the reason preserved. Either way the operator line
		// carries the classification in err itself.
		report("machine revoke: drive obligation", err)
	}
}

// produceMachineRevoke is the record-and-drive arm, run at the revoke moment.
func produceMachineRevoke(p gatewayParams) {
	if p.PushGateway == nil {
		return
	}
	machineRevoke(p.StateDir, p.PushGateway.GatewayURL, p.PushGateway.MachineRevokeCapability,
		p.PushGateway.Address, true)
}

// redrivePendingMachineRevoke is u37c's durable retry across machine death, and it
// MUST run BEFORE requireSomethingToServe: a completed revoke leaves ZERO paired
// devices -- exactly the state the quiescence gate exits on -- so a retry gated behind
// "something to serve" is unreachable in the only world that ever needs it. It needs
// only StateDir and push-gateway.json, never a paired device or resolved params.
//
// THE ZERO-DEVICE PRECONDITION IS ENFORCED, not assumed (round 3): the redrive drives
// only while the device registry is quiescent. push-gateway.json has no writer in this
// tree (a hand-provisioned scaffold), so a re-pair after a revoke keeps the SAME push
// address -- and Drive uses the obligation's stored address regardless. Driving the
// stored DELETE with a device paired again would tombstone the LIVE pairing's wake
// path permanently while reporting success. An obligation found pending in that state
// is RETIRED durably instead: leaving it pending defers the same delete to the next
// zero-device start (the next revoke), against whatever address is stored.
func redrivePendingMachineRevoke(stateDir string) {
	f, addr, present, err := parsePushGatewayFile(filepath.Join(stateDir, "remote"))
	if err != nil {
		report("machine revoke: read push-gateway.json", err)
		return
	}
	if !present {
		return
	}
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		report("machine revoke: open device registry", err)
		return
	}
	if n := len(reg.List()); n > 0 {
		retireStalePendingRevoke(stateDir, n)
		return
	}
	machineRevoke(stateDir, f.GatewayURL, f.MachineRevokeCapability, addr, false)
}

// retireStalePendingRevoke resolves a pending revoke obligation found with n devices
// paired again: the stored address may be the live pairing's own wake path (see
// redrivePendingMachineRevoke), so the delete must never be presented -- now or on any
// later start.
func retireStalePendingRevoke(stateDir string, n int) {
	store, err := remotegw.OpenRevokeObligationStore(
		filepath.Join(stateDir, "remote", "revoke-obligation.json"))
	if err != nil {
		report("machine revoke: open obligation store", err)
		return
	}
	ob, ok := store.Pending()
	if !ok {
		return
	}
	ob.Done = true
	ob.Refusal = fmt.Sprintf("retired undriven: %d device(s) paired again before the delete "+
		"resolved; the stored address may be the live pairing's wake path", n)
	if err := store.Put(ob); err != nil {
		report("machine revoke: retire stale obligation", err)
		return
	}
	report("machine revoke: pending obligation retired undriven; a device is paired again "+
		"and the stored push address may be live", nil)
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

	// The durable retry across machine death (u37c): a revoke obligation an earlier
	// process recorded and could not resolve is re-presented BEFORE the quiescence
	// gate, because a completed revoke leaves zero paired devices and a retry placed
	// after the gate would be unreachable in the one world that needs it. The redrive
	// itself ENFORCES that zero-device precondition (round 3): with a device paired
	// again it retires the obligation instead of deleting what may be the live
	// pairing's wake path. The PG-REV-2 tombstone makes the re-presented delete a 204.
	redrivePendingMachineRevoke(stateDir)

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
			// The owner revoked the paired device: the machine PRODUCES the revoke --
			// record the durable obligation and delete the pairing's push address at the
			// gateway (u37c). Independent of the epoch rotation the revoke performed: the
			// obligation holds pairing material only.
			produceMachineRevoke(p)
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
