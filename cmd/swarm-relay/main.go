// Command swarm-relay boots the untrusted rendezvous/mailbox/push relay from a
// single config file (R-REL.9). It fails closed: no config path, or an
// unreadable config, is a clean error rather than a boot on unspecified
// defaults.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/remote/push"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// defaultSweepInterval is the production cadence for the relay's clock-driven
// maintenance sweeps (presence-went-silent pushes + retention purges) when the
// config file does not specify one (CR-3). DefaultConfig leaves SweepInterval at
// 0 so in-process tests stay manual; the shipped binary must run the loop.
const defaultSweepInterval = 30 * time.Second

// run parses argv, loads the config, and serves until ctx is canceled.
func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("swarm-relay", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", "", "path to the relay config file (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return errors.New("swarm-relay: --config is required")
	}
	cfg, err := relay.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	// CR-3: the shipped relay runs the maintenance sweeps on a timer. Honor a
	// sweep_interval from the config file if it supplies one; otherwise fall back
	// to a sane non-zero production cadence (DefaultConfig deliberately leaves it 0).
	if cfg.SweepInterval <= 0 {
		cfg.SweepInterval = defaultSweepInterval
	}
	opts, err := pushOptions(cfg)
	if err != nil {
		return err
	}
	srv, err := relay.New(cfg, opts...)
	if err != nil {
		return err
	}
	if err := srv.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return srv.Close()
}

// pushOptions builds the push transport the relay serves with, or none.
//
// This is the seam that makes internal/remote/push more than a library nobody calls: a
// perfectly-tested FCM sender that no binary ever installs is a push system that does not
// exist in production, and every one of its tests stays green while it does not.
//
// It fails CLOSED on a configured-but-broken credential (PB-PUSH-5): booting a relay that
// looks healthy while push is dead means the operator finds out from a user who missed a
// hand-off, hours later, with nothing tying the two together. An UNSET credential is a
// different thing and boots fine with no sink -- "the system works without push".
//
// SCOPE: this wiring has never run against Google. There is no account in this project,
// PB-E2E-5 (real provider, real handset) remains DEFERRED, and nothing here may be read as
// evidence that a wake would be delivered.
func pushOptions(cfg relay.Config) ([]relay.Option, error) {
	if cfg.PushCredentials == "" {
		return nil, nil
	}
	doc, err := os.ReadFile(cfg.PushCredentials)
	if err != nil {
		return nil, fmt.Errorf("swarm-relay: read push credentials: %w", err)
	}
	acct, err := push.LoadServiceAccount(doc)
	if err != nil {
		return nil, fmt.Errorf("swarm-relay: %w", err)
	}
	sender, err := push.NewFCM(push.FCMConfig{Account: acct, RetryDelay: push.DefaultRetryDelay})
	if err != nil {
		return nil, fmt.Errorf("swarm-relay: %w", err)
	}
	return []relay.Option{relay.WithPushSink(sender)}, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
