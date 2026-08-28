// Package converge decides, and then performs, `swarm daemon restart
// --unattended`: the nightly step that moves the RUNNING daemon and gateway onto
// the binary Homebrew has already installed (docs/specifications/auto-upgrade-plan.md
// revision 5, section 3, layer L2).
//
// It is called by nobody at a keyboard, so it is written to be safe at any hour:
// it does nothing at all unless a daemon is running and is a different build; it
// refuses to spawn from the caller's environment (a launchd timer's PATH would
// leave every phone-launched agent unable to find its CLI, section 3's opening
// paragraph); and it defers whenever a session is working or waiting on the user.
// The five decisions are taken in a fixed order and only the last one spawns.
//
// Every dependency is a function value the caller supplies. That is deliberate:
// the engine can then be exercised against a recorded fake, and the daemon, the
// socket and the platform supervisor stay out of this package entirely.
package converge

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// Session is the part of a persisted session Run needs: which one it is, and its
// raw three-dimensional status. The status is carried raw, not as a derived
// Group, so the grouping rule stays the one in internal/status and this package
// can never drift from what the TUI and the phone display.
type Session struct {
	ID     string
	Status status.Status
}

// Deps is everything Run is allowed to touch. A caller wires the real daemon,
// socket and supervisor in; a test wires recorders in.
type Deps struct {
	// Version is the build this binary IS -- internal/version.Version. Rule 1
	// compares the running daemon's hello against it, which is what makes the
	// nightly call idempotent. Run never reads the build stamp itself.
	Version string

	// LockFree reports whether the daemon singleton lock is free, i.e. no daemon
	// is running. Rule 0.
	LockFree func() bool

	// Hello dials the daemon and returns the build version from its hello reply.
	// Rule 1. An error satisfying errors.Is(err, protocol.ErrIncompatibleVersion)
	// means the protocol itself was bumped: there is no build version to compare,
	// and the daemon is by definition not this binary, so the converge continues.
	// Any other error means a daemon that holds the lock but will not answer.
	Hello func() (build string, err error)

	// Sessions reads every session's persisted status FROM DISK. Rule 2. Reading
	// disk rather than asking the daemon is what lets the busy check run against a
	// daemon of any protocol version, and is why no rule before rule 4 can spawn.
	Sessions func() ([]Session, error)

	// EnsureGateway, when non-nil, makes an INSTALLED gateway running -- an
	// idempotent Ensure, never a restart -- and is consulted on rule 1's
	// already-converged exit. It exists because the daemon/gateway pair was
	// sequential, not transactional (lifecycle plan audit, codex finding 2): a
	// night whose daemon restart succeeded and whose gateway restart failed left
	// the gateway stale FOREVER, because every later night matched at rule 1 and
	// exited before touching it. An error here downgrades the exit to failed --
	// a converged daemon behind a dead gateway must not read as success -- and a
	// machine with no gateway installed answers nil (the caller maps
	// ErrNotInstalled to nil, the restartGatewayForDelivery precedent).
	EnsureGateway func() error

	// SavedEnv returns the environment the daemon saved when it last started
	// interactively. Rule 3. An error satisfying errors.Is(err, os.ErrNotExist)
	// means nothing was ever saved, which is the pre-0.13.2 machine.
	SavedEnv func() ([]string, error)

	// RestartDaemon stops the running daemon and spawns a replacement from env,
	// returning only once that replacement is reachable. Rule 4, first half.
	RestartDaemon func(env []string) error

	// RestartGateway restarts the gateway unit in place. Rule 4, second half. An
	// error satisfying errors.Is(err, ErrGatewayNotInstalled) means no unit was ever
	// installed on this machine, which is benign. The caller maps its supervisor's
	// own not-installed error onto that sentinel: this package must not import
	// internal/remote/supervise, which ADR-007 D5 reserves for the owner-invoked CLI
	// and the gateway binary (cmd/swarm's TestDaemonNeverSpawnsTheGateway fences it).
	RestartGateway func() error

	// Log receives exactly one line per run: the reason for the exit code. It is
	// the nightly job's only account of itself (the timer redirects it to
	// ~/.local/state/swarm/upgrade.log), so it is written on every path.
	Log io.Writer
}

// Exit codes. They are a contract with launchd and with the operator doc: 0 the
// processes match the installed binary, 1 something broke and the machine may be
// in a worse state than before, 2 nothing was touched and tomorrow's run should
// try again, 3 nothing was touched and only the owner can unblock it.
const (
	ExitConverged = 0
	ExitFailed    = 1
	ExitDeferred  = 2
	ExitRefused   = 3
)

// ErrGatewayNotInstalled is what Deps.RestartGateway returns (wrapped or bare) when
// no gateway unit exists on this machine. Rule 4 treats it as benign: the daemon
// was restarted and there was no gateway to follow it.
var ErrGatewayNotInstalled = errors.New("converge: no gateway unit installed")

// Run evaluates the five rules in order and returns the process exit code. It
// spawns nothing before rule 4, and writes exactly one reason line to d.Log
// before returning.
func Run(d Deps) int {
	// Preflight. A dependency the caller forgot to wire would panic, and a Go
	// panic leaves the process with status 2 -- the one code that tells the timer
	// "nothing was touched, try again tomorrow". That would be a lie, possibly
	// after the daemon had already been stopped, so a misconfigured run refuses
	// before it touches anything.
	for _, dep := range []struct {
		name    string
		missing bool
	}{
		{"LockFree", d.LockFree == nil},
		{"Hello", d.Hello == nil},
		{"Sessions", d.Sessions == nil},
		{"SavedEnv", d.SavedEnv == nil},
		{"RestartDaemon", d.RestartDaemon == nil},
		{"RestartGateway", d.RestartGateway == nil},
	} {
		if dep.missing {
			return say(d.Log, ExitFailed, "failed: converge is misconfigured, Deps.%s is nil; nothing was touched", dep.name)
		}
	}

	// Rule 0: no daemon is running, so there is nothing to converge. The next
	// `swarm` command starts one from the owner's shell (D-1), with the owner's
	// environment; starting one HERE would pin it to the timer's.
	if d.LockFree() {
		return say(d.Log, ExitConverged, "converged: no daemon holds the lock, nothing to converge")
	}

	// Rule 1: idempotence. A daemon already running this build is the ordinary
	// nightly outcome and must cost nothing. A different build, or a protocol
	// bump that leaves no build version to read, continues. Anything else is a
	// daemon wedged between its lock and its socket: defer, do not stop it.
	build, err := d.Hello()
	switch {
	case err == nil && build == d.Version:
		// The gateway retry (lifecycle R3): a matching daemon no longer skips a
		// stopped-but-installed gateway -- see Deps.EnsureGateway.
		if d.EnsureGateway != nil {
			if gerr := d.EnsureGateway(); gerr != nil {
				return say(d.Log, ExitFailed, "failed: the daemon already runs this build (%s) but the gateway could not be ensured: %v", build, gerr)
			}
		}
		return say(d.Log, ExitConverged, "converged: the daemon already runs this build (%s), nothing to do", build)
	case err == nil:
		// A different build: continue.
	case errors.Is(err, protocol.ErrIncompatibleVersion):
		// A protocol bump: by definition not this binary. Continue.
	default:
		return say(d.Log, ExitDeferred, "deferred: the daemon holds the lock but did not answer the hello: %v", err)
	}

	// Rule 2: never interrupt work. A roster that cannot be read is not known to
	// be idle, and unknown is not safe, so it defers too.
	sessions, err := d.Sessions()
	if err != nil {
		return say(d.Log, ExitDeferred, "deferred: cannot read the session roster from disk: %v", err)
	}
	for _, s := range sessions {
		// Completed and ReadyForReview hold nothing in flight. TurnUnknown derives
		// to Working, so a hung session defers every night until it is ended --
		// which is why the line names it.
		if g := status.Derive(s.Status); g == status.GroupWorking || g == status.GroupNeedsInput {
			return say(d.Log, ExitDeferred, "deferred: session %s is %s", s.ID, g)
		}
	}

	// Rule 3: the replacement's environment must be the one the daemon saved when
	// the owner last started it from a terminal. With none saved there is no safe
	// environment to spawn from, and spawning from this process's own would strip
	// PATH and the API keys off every later phone-launched session.
	env, err := d.SavedEnv()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return say(d.Log, ExitRefused,
				"refused: no saved daemon environment (%v); the owner must run `swarm daemon restart` "+
					"from a terminal once, then this runs unattended", err)
		}
		return say(d.Log, ExitFailed, "failed: cannot read the saved daemon environment: %v", err)
	}
	// A daemon.env that exists but holds nothing reads back as an empty slice with
	// no error (a crash between the write and the rename, or anyone truncating the
	// file). Spawning from it is strictly worse than the launchd environment this
	// layer exists to avoid: the replacement would carry only the SWARM_DAEMON_*
	// stamps its spawner adds -- no PATH, no HOME, no keys. It is refused on the
	// same terms as a file that was never written, because the owner's fix is the
	// same one.
	if len(env) == 0 {
		return say(d.Log, ExitRefused,
			"refused: the saved daemon environment is empty, so a replacement would start with no PATH, "+
				"no HOME and no keys; the owner must run `swarm daemon restart` from a terminal once, "+
				"then this runs unattended")
	}

	// Rule 4: the only rule that moves anything. The gateway follows the daemon or
	// nothing moves, and a new daemon behind a dead gateway is never success.
	if err := d.RestartDaemon(env); err != nil {
		return say(d.Log, ExitFailed, "failed: the daemon restart failed, the gateway was not touched: %v", err)
	}
	if err := d.RestartGateway(); err != nil {
		if errors.Is(err, ErrGatewayNotInstalled) {
			return say(d.Log, ExitConverged,
				"converged: daemon restarted from the saved environment; no gateway unit is installed, "+
					"so there was none to restart (%v)", err)
		}
		return say(d.Log, ExitFailed,
			"failed: daemon restarted from the saved environment but the gateway restart failed: %v", err)
	}
	return say(d.Log, ExitConverged,
		"converged: daemon restarted from the saved environment and gateway restarted in place; "+
			"this binary is %s", d.Version)
}

// say writes the run's single reason line and returns the exit code, so every
// return in Run is one statement that cannot forget to log. A nil or failing
// writer is swallowed: the exit code is the timer's result and is never traded
// for a logging accident.
func say(w io.Writer, code int, format string, args ...any) int {
	if w != nil {
		_, _ = fmt.Fprintf(w, format+"\n", args...)
	}
	return code
}

// SessionsFromStore is the production Sessions dependency: the roster as the
// daemon persisted it, read straight from the state directory. It opens the
// store on every call rather than holding one, so a run started before the
// daemon wrote its latest meta.json still sees the file it wrote.
//
// It reaches rule 2 only when a daemon holds the lock, so the state dir always
// already exists; persist.NewStore's create-if-missing is not load-bearing here.
func SessionsFromStore(dir string) func() ([]Session, error) {
	return func() ([]Session, error) {
		store, err := persist.NewStore(dir)
		if err != nil {
			return nil, err
		}
		metas, err := store.Scan()
		if err != nil {
			return nil, err
		}
		sessions := make([]Session, 0, len(metas))
		for _, m := range metas {
			sessions = append(sessions, Session{ID: m.ID, Status: m.Status})
		}
		return sessions, nil
	}
}

// HelloVia is the production Hello dependency: one bounded dial, one hello, one
// close. protocol.Dial cannot start a daemon and cannot block indefinitely (it
// carries its own dial and read deadlines), which is what makes it safe to call
// from a timer.
func HelloVia(socketPath string) func() (string, error) {
	return func() (string, error) {
		c, err := protocol.Dial(socketPath, nil)
		if err != nil {
			return "", err
		}
		build := c.BuildVersion() // read from the hello reply Dial already consumed
		_ = c.Close()             // the answer is in hand; a close error cannot change it
		return build, nil
	}
}
