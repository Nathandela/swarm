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
	"net/http"
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

// run dispatches to the backup/restore/healthcheck subcommands (playbook 6.5)
// or, with no subcommand given, serves exactly as before -- every existing
// invocation of this binary passes only flags, never a bare first argument, so
// routing on args[0] cannot mistake a real deployment's args for a subcommand
// name.
func run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "backup":
			return runBackup(args[1:])
		case "restore":
			return runRestore(args[1:])
		case "healthcheck":
			return runHealthcheck(args[1:])
		}
	}
	return runServe(ctx, args)
}

// runServe parses argv, loads the config, and serves until ctx is canceled.
func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("swarm-relay", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", "", "path to the relay config file (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return errors.New("swarm-relay: --config is required")
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("swarm-relay: unexpected argument(s) %v -- a subcommand (backup/restore/healthcheck) must come before flags, not after", rest)
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
	operatorSecret, err := ensureOperatorSecret(cfg)
	if err != nil {
		return err
	}
	opts, err := pushOptions(cfg)
	if err != nil {
		return err
	}
	if operatorSecret != "" {
		opts = append(opts, relay.WithOperatorSecret([]byte(operatorSecret)))
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

// runBackup implements `swarm-relay backup --config <path> <dest>`: a
// consistent snapshot of the relay's bbolt store (playbook 6.5, relay.Backup).
// The store path comes from the same config file --config already reads for
// serving, so a backup always targets the db_path a running relay would use.
func runBackup(args []string) error {
	fs := flag.NewFlagSet("swarm-relay backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", "", "path to the relay config file (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return errors.New("swarm-relay backup: --config is required")
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return errors.New("swarm-relay backup: exactly one destination path is required")
	}
	cfg, err := relay.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	return relay.Backup(cfg.DBPath, rest[0])
}

// runRestore implements `swarm-relay restore --config <path> <backup>`: it
// refuses while the relay holds the store open and validates the backup opens
// with every required bucket before replacing anything (relay.Restore).
func runRestore(args []string) error {
	fs := flag.NewFlagSet("swarm-relay restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", "", "path to the relay config file (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return errors.New("swarm-relay restore: --config is required")
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return errors.New("swarm-relay restore: exactly one backup path is required")
	}
	cfg, err := relay.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	return relay.Restore(cfg.DBPath, rest[0])
}

// healthcheckTimeout bounds the round-trip `swarm-relay healthcheck` makes
// against admin_listen's /readyz. Generous for a loopback call, short enough
// that a wedged relay fails the Docker HEALTHCHECK promptly rather than
// hanging it.
const healthcheckTimeout = 5 * time.Second

// runHealthcheck implements `swarm-relay healthcheck --config <path>`: the
// Docker HEALTHCHECK entry point (deploy/relay/Dockerfile, playbook 6.5). The
// distroless final stage has no shell, curl, or wget, so this subcommand --
// reusing the same binary and config the relay itself boots from -- is what
// Docker execs instead. A non-nil error (including any non-200 from /readyz)
// is a failed healthcheck; Docker restarts the container on repeated failures.
func runHealthcheck(args []string) error {
	fs := flag.NewFlagSet("swarm-relay healthcheck", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", "", "path to the relay config file (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return errors.New("swarm-relay healthcheck: --config is required")
	}
	cfg, err := relay.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if cfg.AdminListen == "" {
		return errors.New("swarm-relay healthcheck: admin_listen is not set in the config; there is no /readyz to ask")
	}
	client := &http.Client{Timeout: healthcheckTimeout}
	resp, err := client.Get("http://" + cfg.AdminListen + "/readyz")
	if err != nil {
		return fmt.Errorf("swarm-relay healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("swarm-relay healthcheck: /readyz returned %d, want %d", resp.StatusCode, http.StatusOK)
	}
	return nil
}

// ensureOperatorSecret generates and persists the R2 operator secret
// (playbook 6.5) at cfg.OperatorSecretFile if the config names one -- unset is
// a normal, supported boot with no operator secret, the same opt-in shape
// pushOptions gives push_credentials. The returned secret feeds
// relay.WithOperatorSecret so the doctor capability (a separate R2 slice) can
// use it; it is NEVER logged by this binary -- callers must not wrap the
// return value in a log statement.
func ensureOperatorSecret(cfg relay.Config) (string, error) {
	if cfg.OperatorSecretFile == "" {
		return "", nil
	}
	return relay.EnsureOperatorSecret(cfg.OperatorSecretFile)
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
