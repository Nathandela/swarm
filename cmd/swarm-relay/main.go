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

	"github.com/Nathandela/swarm/internal/remote/relay"
)

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
