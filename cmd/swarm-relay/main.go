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
	srv, err := relay.New(cfg)
	if err != nil {
		return err
	}
	if err := srv.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return srv.Close()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
