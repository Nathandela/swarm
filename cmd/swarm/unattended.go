package main

// `swarm daemon restart --unattended` (auto-upgrade plan revision 5, section 3 layer
// L2; ADR-020). The decision engine is internal/converge, which touches no daemon, no
// socket and no init system of its own: this file is the only place the production
// dependencies are bound to it.

import (
	"errors"
	"fmt"
	"io"

	"github.com/Nathandela/swarm/internal/converge"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/supervise"
	"github.com/Nathandela/swarm/internal/version"
)

// daemonRestartUsage is printed for any argument after `daemon restart` that is not
// exactly `--unattended`.
const daemonRestartUsage = "usage: swarm daemon restart [--unattended]"

// runDaemonRestartUnattended converges the RUNNING daemon and gateway onto this
// binary. It is called by nobody at a keyboard -- a launchd timer at 04:00 -- so its
// return value is converge.Run's exit code (0 converged, 1 failed, 2 deferred, 3
// refused), which is the contract with the timer and with docs/ops/auto-upgrade.md.
//
// The whole point of the flag is the ENVIRONMENT: RestartDaemon spawns the
// replacement from the environment the daemon SAVED at its last interactive start,
// never from os.Environ(), because under the timer that would be launchd's bare
// PATH with no credentials and every later phone-launched session would inherit it
// (PolicyEnv, internal/daemon/policyenv.go).
func runDaemonRestartUnattended(stderr io.Writer) int {
	cc, err := clientConfig()
	if err != nil {
		// Same shape and same exit as the interactive sibling's config failure: nothing
		// was touched, and the reason is on the timer's log.
		_, _ = fmt.Fprintf(stderr, "daemon restart: %v\n", err)
		return converge.ExitFailed
	}
	return converge.Run(converge.Deps{
		Version:  version.Version,
		LockFree: func() bool { return daemon.LockFree(cc) },
		Hello:    converge.HelloVia(cc.SocketPath),
		// The daemon's session store IS the state dir (internal/daemon/daemon.go's
		// persist.NewStore(cfg.StateDir)), so rule 2 reads the same tree the daemon writes.
		Sessions: converge.SessionsFromStore(cc.StateDir),
		SavedEnv: func() ([]string, error) { return daemon.LoadSavedEnv(cc.StateDir) },
		RestartDaemon: func(env []string) error {
			// A copy, so the saved environment is scoped to this one spawn. cc.DaemonBin is
			// os.Executable() UNRESOLVED (clientConfig), which is the linked path the plan
			// requires: resolving it would pin every later session launch to the Caskroom
			// directory the next upgrade purges.
			c := cc
			c.Env = env
			return daemon.Restart(c)
		},
		RestartGateway: func() error {
			// The package var, so a test substitutes a fake and never reaches launchd.
			// supervise.ErrNotInstalled is mapped onto converge's own sentinel here, at
			// the one place allowed to know both: internal/converge must not import
			// internal/remote/supervise (ADR-007 D5, TestDaemonNeverSpawnsTheGateway).
			sup, err := newGatewaySupervisor(cc.StateDir)
			if err != nil {
				return err
			}
			if err := sup.Restart(); err != nil {
				if errors.Is(err, supervise.ErrNotInstalled) {
					return fmt.Errorf("%w: %v", converge.ErrGatewayNotInstalled, err)
				}
				return err
			}
			return nil
		},
		Log: stderr,
	})
}
