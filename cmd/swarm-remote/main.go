// Command swarm-remote is the gateway sidecar process (slice G2): it dials the
// relay over a machine-authenticated WebSocket, then runs remotegw.Service to
// bridge the daemon's journal to the phone's mailbox (journal-OUT) and forward
// phone commands to the daemon (command-IN) until signalled.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// serviceConfigFromParams copies a resolved gatewayParams (slice G1) plus a
// dialed relay Mailbox into a remotegw.ServiceConfig. Forwarder, PollInterval,
// ReconnectDelay, and Now are left zero: remotegw.NewService defaults them.
func serviceConfigFromParams(p gatewayParams, mailbox remotegw.Mailbox) remotegw.ServiceConfig {
	return remotegw.ServiceConfig{
		DaemonSocket:   p.DaemonSocket,
		Relay:          mailbox,
		PhoneTarget:    p.PhoneTarget,
		Key:            p.Key,
		EpochID:        p.EpochID,
		RecipientKeyID: p.RecipientKeyID,
		SenderKeyID:    p.SenderKeyID,
		JournalSeq:     p.JournalSeq,
		ReplySeq:       p.ReplySeq,
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
			fmt.Fprintf(os.Stderr, "swarm-remote: %v\n", err)
			os.Exit(1)
		}
	}
	daemonSocket := os.Getenv(daemon.EnvRemoteSocket)

	p, err := resolveGatewayParams(stateDir, daemonSocket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swarm-remote: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, p); err != nil && err != context.Canceled {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
