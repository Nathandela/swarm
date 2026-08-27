// Tests for the `swarm daemon restart --unattended` rule engine
// (docs/specifications/auto-upgrade-plan.md revision 5, section 3 L2 and the
// section 4 test table). They drive Run through a recording fake of every
// dependency, so each rule's premise, action and exit code are fenced
// independently of the daemon, the socket and the gateway.
//
// The external test package is deliberate: these tests may only touch the
// exported API, so they cannot be satisfied by reaching into unexported state.
package converge_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/converge"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/supervise"
	"github.com/Nathandela/swarm/internal/status"
)

// thisBuild is the version of the binary running the converge, i.e. what the
// daemon's hello reply is compared against in rule 1. It is deliberately not
// version.Version: the engine must compare against Deps.Version, never read the
// build stamp itself.
const thisBuild = "v9.9.9-installed"

// otherBuild is a daemon still running a previous release.
const otherBuild = "v9.9.8-running"

// savedEnv is the environment the daemon saved when it last started
// interactively. LC_SWARM_PROBE is the plan's discriminator (section 5).
var savedEnv = []string{"PATH=/opt/homebrew/bin:/usr/bin", "HOME=/Users/owner", "LC_SWARM_PROBE=saved"}

// fake supplies every Deps function and records each call, in order, in trace.
// A fake built by newFake proceeds all the way through rule 4; each test
// perturbs exactly the one field its rule is about.
type fake struct {
	lockFree          bool
	hello             string
	helloErr          error
	sessions          []converge.Session
	sessionsErr       error
	env               []string
	envErr            error
	restartDaemonErr  error
	restartGatewayErr error

	trace          []string
	gotEnv         []string
	daemonRestarts int
	log            bytes.Buffer
}

// newFake returns a fake whose every rule passes: a daemon holds the lock, it
// answers hello with a different build, no session is busy, and an env is saved.
func newFake() *fake {
	return &fake{
		lockFree: false,
		hello:    otherBuild,
		sessions: []converge.Session{
			{ID: "done-1", Status: status.Status{Process: status.ProcessExited, Turn: status.TurnIdle, Interaction: status.InteractionNone}},
			{ID: "review-1", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}},
		},
		env: savedEnv,
	}
}

func (f *fake) record(name string) { f.trace = append(f.trace, name) }

func (f *fake) deps() converge.Deps {
	return converge.Deps{
		Version: thisBuild,
		LockFree: func() bool {
			f.record("LockFree")
			return f.lockFree
		},
		Hello: func() (string, error) {
			f.record("Hello")
			return f.hello, f.helloErr
		},
		Sessions: func() ([]converge.Session, error) {
			f.record("Sessions")
			return f.sessions, f.sessionsErr
		},
		SavedEnv: func() ([]string, error) {
			f.record("SavedEnv")
			return f.env, f.envErr
		},
		RestartDaemon: func(env []string) error {
			f.record("RestartDaemon")
			f.daemonRestarts++
			f.gotEnv = env
			return f.restartDaemonErr
		},
		RestartGateway: func() error {
			f.record("RestartGateway")
			return f.restartGatewayErr
		},
		Log: &f.log,
	}
}

func (f *fake) called(name string) bool {
	for _, c := range f.trace {
		if c == name {
			return true
		}
	}
	return false
}

// indexOf returns the position of name in the trace, or -1.
func (f *fake) indexOf(name string) int {
	for i, c := range f.trace {
		if c == name {
			return i
		}
	}
	return -1
}

// reason asserts the run wrote EXACTLY one non-empty reason line (the plan: one
// line per decision, always) and returns it.
func (f *fake) reason(t *testing.T) string {
	t.Helper()
	out := f.log.String()
	if out == "" {
		t.Fatalf("Run wrote nothing to Log; every exit must write exactly one reason line")
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("Run wrote %d log lines, want exactly 1:\n%s", len(lines), out)
	}
	if strings.TrimSpace(lines[0]) == "" {
		t.Fatalf("Run wrote a blank reason line")
	}
	return lines[0]
}

// wantTrace asserts the exact call sequence.
func (f *fake) wantTrace(t *testing.T, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatalf("call trace = %v, want %v", f.trace, want)
	}
}

// Rule 0. A free lock means no daemon is running: there is nothing to converge,
// the run exits 0, and NO other dependency is consulted -- in particular nothing
// may dial, read disk or spawn. D-1 spawns the next daemon from the owner's shell.
func TestRule0FreeLockExitsConvergedAndTouchesNothingElse(t *testing.T) {
	f := newFake()
	f.lockFree = true

	if got := converge.Run(f.deps()); got != converge.ExitConverged {
		t.Errorf("Run with a free lock = %d, want ExitConverged (%d)", got, converge.ExitConverged)
	}
	f.wantTrace(t, "LockFree")
	for _, dep := range []string{"Hello", "Sessions", "SavedEnv", "RestartDaemon", "RestartGateway"} {
		if f.called(dep) {
			t.Errorf("rule 0 called %s; with no daemon running nothing else may run", dep)
		}
	}
	if line := f.reason(t); line == "" {
		t.Error("rule 0 logged no reason")
	}
}

// Rule 1. The hello reply decides idempotence: an equal build version is the
// nightly no-op; a different build or a protocol bump continues to rule 2; any
// other dial error is a wedged daemon and defers.
func TestRule1HelloDecidesIdempotence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hello     string
		helloErr  error
		wantExit  int
		wantTrace []string
	}{
		{
			name:      "same build is the nightly no-op",
			hello:     thisBuild,
			wantExit:  converge.ExitConverged,
			wantTrace: []string{"LockFree", "Hello"},
		},
		{
			name:      "a different build proceeds to the disk checks and restarts",
			hello:     otherBuild,
			wantExit:  converge.ExitConverged,
			wantTrace: []string{"LockFree", "Hello", "Sessions", "SavedEnv", "RestartDaemon", "RestartGateway"},
		},
		{
			// Wrapped, so only errors.Is can see it: a == comparison fails this case.
			name:      "a protocol bump proceeds without a usable build version",
			helloErr:  fmt.Errorf("dial %s: %w", "/tmp/swarm.sock", protocol.ErrIncompatibleVersion),
			wantExit:  converge.ExitConverged,
			wantTrace: []string{"LockFree", "Hello", "Sessions", "SavedEnv", "RestartDaemon", "RestartGateway"},
		},
		{
			name:      "a wedged daemon defers",
			helloErr:  errors.New("dial: i/o timeout"),
			wantExit:  converge.ExitDeferred,
			wantTrace: []string{"LockFree", "Hello"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.hello, f.helloErr = tc.hello, tc.helloErr

			if got := converge.Run(f.deps()); got != tc.wantExit {
				t.Errorf("Run = %d, want %d", got, tc.wantExit)
			}
			f.wantTrace(t, tc.wantTrace...)
			f.reason(t)
		})
	}
}

// Rule 1's deferred arm must log WHY it deferred, so the nightly log tells a
// wedged daemon apart from a busy one.
func TestRule1WedgedDaemonLogsTheDialError(t *testing.T) {
	f := newFake()
	f.helloErr = errors.New("connection refused on a held lock")

	if got := converge.Run(f.deps()); got != converge.ExitDeferred {
		t.Fatalf("Run against a wedged daemon = %d, want ExitDeferred (%d)", got, converge.ExitDeferred)
	}
	if line := f.reason(t); !strings.Contains(line, "connection refused on a held lock") {
		t.Errorf("deferred reason = %q, want it to carry the dial error", line)
	}
}

// Rule 2. Busy-ness is read from disk. A session deriving to Working or
// NeedsInput defers the whole run and names itself in the log; Completed and
// ReadyForReview hold nothing in flight and proceed.
func TestRule2BusySessionDefersAndIsNamed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    status.Status
		wantGroup status.Group
	}{
		{
			name:      "an active turn is working",
			status:    status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone},
			wantGroup: status.GroupWorking,
		},
		{
			name:      "an idle turn on a prompt needs input",
			status:    status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPrompt},
			wantGroup: status.GroupNeedsInput,
		},
		{
			name:      "an idle turn on a permission needs input",
			status:    status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission},
			wantGroup: status.GroupNeedsInput,
		},
		{
			// A hung session: TurnUnknown derives to Working, so it defers every
			// night until it is ended, and the log has to say which session.
			name:      "an unknown turn is working",
			status:    status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
			wantGroup: status.GroupWorking,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.sessions = append(f.sessions, converge.Session{ID: "busy-7", Status: tc.status})

			if got := converge.Run(f.deps()); got != converge.ExitDeferred {
				t.Errorf("Run with a %s session = %d, want ExitDeferred (%d)", tc.wantGroup, got, converge.ExitDeferred)
			}
			if f.called("RestartDaemon") {
				t.Error("rule 2 spawned a daemon; a busy session must stop the run before rule 4")
			}
			if f.called("SavedEnv") {
				t.Error("rule 2 read the saved env; the busy check decides before rule 3")
			}
			line := f.reason(t)
			if !strings.Contains(line, "busy-7") {
				t.Errorf("deferred reason = %q, want it to name session busy-7", line)
			}
			if !strings.Contains(line, string(tc.wantGroup)) {
				t.Errorf("deferred reason = %q, want it to name group %q", line, tc.wantGroup)
			}
		})
	}
}

// Rule 2's passing arm: Completed and ReadyForReview together are not busy.
func TestRule2CompletedAndReadyForReviewProceed(t *testing.T) {
	f := newFake() // newFake's roster is exactly one Completed and one ReadyForReview

	if got := converge.Run(f.deps()); got != converge.ExitConverged {
		t.Errorf("Run with only idle sessions = %d, want ExitConverged (%d)", got, converge.ExitConverged)
	}
	if !f.called("RestartDaemon") {
		t.Error("idle sessions must not stop the converge")
	}
	f.reason(t)
}

// A roster that cannot be read is not known to be idle, so it defers rather than
// restarts: unknown is never treated as safe.
func TestRule2SessionsErrorDefers(t *testing.T) {
	f := newFake()
	f.sessionsErr = errors.New("scan state dir: permission denied")

	if got := converge.Run(f.deps()); got != converge.ExitDeferred {
		t.Errorf("Run with an unreadable roster = %d, want ExitDeferred (%d)", got, converge.ExitDeferred)
	}
	if f.called("RestartDaemon") {
		t.Error("an unreadable roster must never spawn: unknown is not idle")
	}
	if line := f.reason(t); !strings.Contains(line, "permission denied") {
		t.Errorf("deferred reason = %q, want it to carry the scan error", line)
	}
}

// Rule 3. Nothing saved means the 0.13-to-0.14 hop has not been made by hand:
// refuse (exit 3), leave the daemon running, and tell the owner what to run.
func TestRule3NoSavedEnvRefusesAndNamesTheManualStep(t *testing.T) {
	f := newFake()
	f.envErr = fmt.Errorf("open /state/daemon.env: %w", os.ErrNotExist)

	if got := converge.Run(f.deps()); got != converge.ExitRefused {
		t.Errorf("Run with no saved env = %d, want ExitRefused (%d)", got, converge.ExitRefused)
	}
	if f.called("RestartDaemon") {
		t.Error("rule 3 spawned a daemon; with no saved env the running daemon must be left alone")
	}
	if f.called("RestartGateway") {
		t.Error("rule 3 restarted the gateway; a refusal touches nothing")
	}
	if line := f.reason(t); !strings.Contains(line, "swarm daemon restart") {
		t.Errorf("refusal reason = %q, want it to tell the owner to run `swarm daemon restart` from a terminal", line)
	}
}

// Any other failure to read the saved env is a real failure, not a refusal.
func TestRule3UnreadableSavedEnvFails(t *testing.T) {
	f := newFake()
	f.envErr = errors.New("read daemon.env: input/output error")

	if got := converge.Run(f.deps()); got != converge.ExitFailed {
		t.Errorf("Run with an unreadable saved env = %d, want ExitFailed (%d)", got, converge.ExitFailed)
	}
	if f.called("RestartDaemon") {
		t.Error("rule 3 spawned a daemon on an unreadable env file")
	}
	if line := f.reason(t); !strings.Contains(line, "input/output error") {
		t.Errorf("failure reason = %q, want it to carry the read error", line)
	}
}

// Rule 4, the whole happy path: the exact call order, the SAVED environment
// handed to the spawn, and exit 0.
func TestRule4ConvergesInOrderWithTheSavedEnvironment(t *testing.T) {
	f := newFake()

	if got := converge.Run(f.deps()); got != converge.ExitConverged {
		t.Errorf("Run = %d, want ExitConverged (%d)", got, converge.ExitConverged)
	}
	f.wantTrace(t, "LockFree", "Hello", "Sessions", "SavedEnv", "RestartDaemon", "RestartGateway")
	if !reflect.DeepEqual(f.gotEnv, savedEnv) {
		t.Errorf("RestartDaemon env = %v, want the saved env %v", f.gotEnv, savedEnv)
	}
	if f.daemonRestarts != 1 {
		t.Errorf("RestartDaemon called %d times, want exactly 1", f.daemonRestarts)
	}
	f.reason(t)
}

// Rule 4's failure arms: both or neither, and a dead gateway is never success.
func TestRule4GatewayFollowsTheDaemonOrNothingMoves(t *testing.T) {
	for _, tc := range []struct {
		name       string
		daemonErr  error
		gatewayErr error
		wantExit   int
		wantGwCall bool
		wantReason string
	}{
		{
			name:       "a failed spawn leaves the gateway untouched",
			daemonErr:  errors.New("spawn replacement: exec format error"),
			wantExit:   converge.ExitFailed,
			wantGwCall: false,
			wantReason: "exec format error",
		},
		{
			// Wrapped, so only errors.Is can see it.
			name:       "no gateway unit installed is benign",
			gatewayErr: fmt.Errorf("restart gateway: %w", supervise.ErrNotInstalled),
			wantExit:   converge.ExitConverged,
			wantGwCall: true,
		},
		{
			name:       "any other gateway error fails the run",
			gatewayErr: errors.New("kickstart: Could not find service"),
			wantExit:   converge.ExitFailed,
			wantGwCall: true,
			wantReason: "Could not find service",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.restartDaemonErr, f.restartGatewayErr = tc.daemonErr, tc.gatewayErr

			if got := converge.Run(f.deps()); got != tc.wantExit {
				t.Errorf("Run = %d, want %d", got, tc.wantExit)
			}
			if got := f.called("RestartGateway"); got != tc.wantGwCall {
				t.Errorf("RestartGateway called = %v, want %v", got, tc.wantGwCall)
			}
			line := f.reason(t)
			if tc.wantReason != "" && !strings.Contains(line, tc.wantReason) {
				t.Errorf("reason = %q, want it to carry %q", line, tc.wantReason)
			}
		})
	}
}

// The plan's protocol-bump night, end to end: a daemon that refuses the hello is
// still converged, and the decision to spawn is taken only AFTER the disk reads.
// This is the rule that proves no premise is evaluated out of order.
func TestRefusedHelloSpawnsOnceAndOnlyAfterTheDiskChecks(t *testing.T) {
	f := newFake()
	f.helloErr = fmt.Errorf("hello: %w", protocol.ErrIncompatibleVersion)

	if got := converge.Run(f.deps()); got != converge.ExitConverged {
		t.Fatalf("Run against a protocol-bumped daemon = %d, want ExitConverged (%d)", got, converge.ExitConverged)
	}
	if f.daemonRestarts != 1 {
		t.Fatalf("RestartDaemon called %d times, want exactly 1", f.daemonRestarts)
	}
	spawn := f.indexOf("RestartDaemon")
	for _, before := range []string{"Sessions", "SavedEnv"} {
		i := f.indexOf(before)
		if i < 0 {
			t.Fatalf("%s was never called; a protocol bump must still read disk", before)
		}
		if i > spawn {
			t.Errorf("%s ran at index %d, after the spawn at %d; the spawn must be last", before, i, spawn)
		}
	}
}

// A dependency the caller forgot to wire must fail the run loudly and BEFORE
// anything is touched. A panic would exit the process with status 2, which is
// exactly the code that means "nothing was touched, try again tomorrow" -- so an
// unguarded nil would have the job lie to launchd every night, forever.
func TestMissingDependencyFailsBeforeTouchingAnything(t *testing.T) {
	for _, tc := range []struct {
		name  string
		blank func(d *converge.Deps)
	}{
		{"LockFree", func(d *converge.Deps) { d.LockFree = nil }},
		{"Hello", func(d *converge.Deps) { d.Hello = nil }},
		{"Sessions", func(d *converge.Deps) { d.Sessions = nil }},
		{"SavedEnv", func(d *converge.Deps) { d.SavedEnv = nil }},
		{"RestartDaemon", func(d *converge.Deps) { d.RestartDaemon = nil }},
		{"RestartGateway", func(d *converge.Deps) { d.RestartGateway = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			d := f.deps()
			tc.blank(&d)

			if got := converge.Run(d); got != converge.ExitFailed {
				t.Errorf("Run with a nil %s = %d, want ExitFailed (%d)", tc.name, got, converge.ExitFailed)
			}
			if len(f.trace) != 0 {
				t.Errorf("Run with a nil %s called %v; a misconfigured run must touch nothing", tc.name, f.trace)
			}
			if line := f.reason(t); !strings.Contains(line, tc.name) {
				t.Errorf("reason = %q, want it to name the missing dependency %q", line, tc.name)
			}
		})
	}
}

// A nil Log must not panic the nightly job: the exit code is the timer's result
// and is never traded for a logging accident.
func TestNilLogDoesNotPanic(t *testing.T) {
	f := newFake()
	d := f.deps()
	d.Log = nil

	if got := converge.Run(d); got != converge.ExitConverged {
		t.Errorf("Run with a nil Log = %d, want ExitConverged (%d)", got, converge.ExitConverged)
	}
}

// The exit codes are a contract with launchd and with docs/ops/auto-upgrade.md.
func TestExitCodesAreTheDocumentedNumbers(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"converged", converge.ExitConverged, 0},
		{"failed", converge.ExitFailed, 1},
		{"deferred", converge.ExitDeferred, 2},
		{"refused", converge.ExitRefused, 3},
	} {
		if tc.got != tc.want {
			t.Errorf("Exit%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// compile-time proof that Log is an io.Writer, so any writer the orchestrator
// already has (a file, os.Stderr) can be passed straight through.
var _ = func() io.Writer { return io.Discard }
