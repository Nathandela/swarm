// Command swarm-remote is the gateway sidecar process (slice G2): it dials the
// relay over a machine-authenticated WebSocket, then runs remotegw.Service to
// bridge the daemon's journal to the phone's mailbox (journal-OUT) and forward
// phone commands to the daemon (command-IN) until signalled.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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
		EpochID:        p.EpochID,
		GrantSeq:       p.GrantSeq,
		RecipientKeyID: p.RecipientKeyID,
		SenderKeyID:    p.SenderKeyID,
		JournalSeq:     p.JournalSeq,
		ReplySeq:       p.ReplySeq,
		Outbox:         p.Outbox,
		Inbound:        p.Inbound,
		StateDir:       p.StateDir,
		DeviceID:       p.DeviceID,
	}
}

// run dials the relay and drives remotegw.Service until ctx is cancelled.
func run(ctx context.Context, p gatewayParams) error {
	client, err := relay.Dial(ctx, p.RelayURL, p.RelayAuth)
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	defer client.Close()

	// C5: authorize the paired device and deliver its sealed epoch grant over the mailbox
	// before the bridge starts (idempotent; the phone dedups by grant seq).
	if err := deliverEpochGrant(ctx, client, p); err != nil {
		return fmt.Errorf("deliver epoch grant: %w", err)
	}

	svc := remotegw.NewService(serviceConfigFromParams(p, client))
	return svc.Run(ctx)
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
