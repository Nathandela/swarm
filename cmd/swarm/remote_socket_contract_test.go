package main

// FAILING-FIRST tests for slice S4b -- PB-LIFE-7: the daemon and the gateway must agree on
// the remote socket, or the default install pairs and then goes silent.
//
// THE DEFECT. Two independent definitions of one path:
//
//   - the supervision unit's dial target comes from gatewaySocket(stateDir) (remote.go),
//     which DEFAULTS to ADR-007 D4's canonical <stateDir>/remote.sock;
//   - the daemon's listen path comes from skeletonConfigFromEnv (main.go), which is
//     os.Getenv(daemon.EnvRemoteSocket) with NO default -- "empty => remote control off".
//
// So on a stock install `swarm remote init` installs (and, since S4's remediation, STARTS)
// a unit pointing at a socket nothing serves. The gateway exits failure; both unit types
// restart a failed gateway; launchd's ThrottleInterval has no burst cap. The user-visible
// symptom is the phone pairing and then nothing -- the flagship exit criterion delivering
// its first step and no other.
//
// WHAT THESE TESTS DO NOT DECIDE. PB-LIFE-7 permits two resolutions and the choice has an
// ADR consequence:
//
//	(a) the daemon opens the canonical socket once remote is provisioned (a security
//	    default changes: remote control is currently off unless asked);
//	(b) enabling remote stays an explicit owner action and `swarm remote init` REFUSES
//	    loudly rather than activating a gateway that cannot work, naming the step.
//
// Every assertion below passes under EITHER and fails on the current half-state. None of
// them names a socket path, an error string, or a command that this test invented: the
// daemon's listen path is read from the production config function the `swarm daemon` role
// itself calls, and "served" is decided by a REAL dial against a REAL in-process daemon.
//
// HOW THESE TESTS STAY OFF THE REAL INIT SYSTEM. Every test installs a fake over
// newGatewaySupervisor (remote_supervise_test.go), and supervise.runUnit refuses to exec
// launchctl/systemctl inside a test binary regardless.

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/supervise"
	"github.com/Nathandela/swarm/internal/skeleton"
)

// unsetRemoteSocketEnv leaves daemon.EnvRemoteSocket genuinely UNSET for one test -- the
// stock install, where nobody has ever opted in. t.Setenv first so the developer's own
// value (if any) is restored on cleanup; Unsetenv then removes it, which is not the same
// as setting it empty for anything that distinguishes the two.
func unsetRemoteSocketEnv(t *testing.T) {
	t.Helper()
	t.Setenv(daemon.EnvRemoteSocket, "")
	if err := os.Unsetenv(daemon.EnvRemoteSocket); err != nil {
		t.Fatalf("unset %s: %v", daemon.EnvRemoteSocket, err)
	}
}

// startInstallDaemon stands up a REAL in-process daemon through cmd/swarm's OWN production
// configuration path: skeletonConfigFromEnv is the function the `swarm daemon` role calls
// (main.go), so whatever remote socket a real daemon would open, this one opens. That is
// the point -- the test never states the path itself, and a resolution that adds the
// default anywhere on the daemon's config path (this function, skeleton.Serve, or a shared
// helper) is seen here.
//
// The caller decides the environment BEFORE calling: a stock install leaves
// daemon.EnvRemoteSocket unset, the opted-in install sets it.
func startInstallDaemon(t *testing.T, stateDir string) {
	t.Helper()
	t.Setenv(daemon.EnvStateDir, stateDir)
	t.Setenv(daemon.EnvSocket, filepath.Join(stateDir, "daemon.sock"))
	t.Setenv(daemon.EnvLock, filepath.Join(stateDir, "daemon.lock"))
	t.Setenv(daemon.EnvLog, filepath.Join(stateDir, "daemon.log"))

	cfg, ok := skeletonConfigFromEnv()
	if !ok {
		t.Fatalf("skeletonConfigFromEnv() reported no configuration with %s=%s", daemon.EnvStateDir, stateDir)
	}
	sk, err := skeleton.Serve(cfg)
	if err != nil {
		t.Fatalf("skeleton.Serve: %v", err)
	}
	t.Cleanup(func() { _ = sk.Close() })
}

// daemonListensOn is the path a daemon assembled from the CURRENT environment opens its
// remote-tier listener on -- read from the production config function, never restated.
// Empty means the daemon serves no remote socket at all.
func daemonListensOn(t *testing.T) string {
	t.Helper()
	cfg, ok := skeletonConfigFromEnv()
	if !ok {
		t.Fatalf("skeletonConfigFromEnv() reported no configuration; the test set no %s", daemon.EnvStateDir)
	}
	return cfg.RemoteSocketPath
}

// dialRemoteSocket is the gateway's first act against the daemon, performed for real:
// cmd/swarm-remote carries Spec.RemoteSocket into ServiceConfig.DaemonSocket and dials it.
// A nil error means a gateway started here would find a daemon; anything else is the
// failure exit the supervision unit then restarts.
func dialRemoteSocket(path string) error {
	if path == "" {
		return errors.New("the unit names no remote socket at all")
	}
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}

// namesAConcreteStep reports whether output tells the operator something they can DO. Both
// accepted forms are checked against production, not against a phrase this test invented:
// the environment variable that turns the daemon's remote socket on, or a `swarm remote
// <verb>` this binary actually routes (remoteUsage is the dispatcher's own list). `swarm
// remote init` does not count -- it is the command that just refused.
func namesAConcreteStep(output string) bool {
	if strings.Contains(output, daemon.EnvRemoteSocket) {
		return true
	}
	for _, m := range regexp.MustCompile(`swarm remote ([a-z]+)`).FindAllStringSubmatch(output, -1) {
		if verb := m[1]; verb != "init" && strings.Contains(remoteUsage, "swarm remote "+verb) {
			return true
		}
	}
	return false
}

// lastLines trims a captured CLI transcript down for a failure message. `swarm remote pair`
// draws a whole QR symbol, which is kilobytes of block glyphs and escape codes that bury
// the one line a failing test is about.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{"..."}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}

// requireServedOrRefused is PB-LIFE-7's whole contract, in the one form that holds under
// both permitted resolutions: after a verb that would activate a gateway, EITHER the
// gateway was activated against a socket the daemon really serves, OR nothing was
// activated and the operator was refused loudly enough to know what to fix. The third
// outcome -- a gateway pointed at nothing, reported as success -- is the defect.
func requireServedOrRefused(t *testing.T, verb string, exit int, output string, f *fakeGatewaySupervisor) {
	t.Helper()
	if f.count("ensure") > 0 {
		if exit != 0 {
			t.Fatalf("`swarm remote %s` activated the gateway AND exited %d; output=%q", verb, exit, lastLines(output, 6))
		}
		if err := dialRemoteSocket(f.spec.RemoteSocket); err != nil {
			t.Fatalf("PB-LIFE-7: `swarm remote %s` started the gateway against %q, but nothing is "+
				"listening there (%v).\n"+
				"The daemon assembled from this same environment listens on %q.\n"+
				"This is the phone-pairs-then-silence configuration: the gateway exits failure and "+
				"the unit restarts it every throttle interval, forever.",
				verb, f.spec.RemoteSocket, err, daemonListensOn(t))
		}
		return
	}
	// Nothing was activated. Resolution (b) allows that -- but only loudly.
	if exit == 0 {
		t.Fatalf("PB-LIFE-7: `swarm remote %s` activated no gateway and exited 0. A device is "+
			"paired, so the desired state is active (supervise.Desired(1)=%v); exiting 0 with "+
			"nothing running is the silence the requirement forbids. output=%q",
			verb, supervise.Desired(1), lastLines(output, 6))
	}
	if !namesAConcreteStep(output) {
		t.Fatalf("PB-LIFE-7: `swarm remote %s` refused (exit %d) without naming a step the operator "+
			"can take -- neither %s nor a routable `swarm remote <verb>` appears. output=%q",
			verb, exit, daemon.EnvRemoteSocket, lastLines(output, 6))
	}
}

// provisionThenActivate walks the default install in the order production guarantees, and
// returns the CLI's transcript from the run that activates the gateway.
//
//  1. `swarm remote init` on a machine with nothing running -- provisions
//     <stateDir>/remote/machine.key and installs the unit; zero devices, so nothing starts.
//  2. the daemon assembles. It reads the identity at assembly (skeleton's
//     loadPairingConfig), so this ORDER is not a convenience: a daemon that starts before
//     provisioning has a nil pairing config and BeginPairing fails closed, which is why a
//     machine cannot reach the paired state without a daemon that saw the identity.
//  3. a device is paired (seeded directly: the pairing wire is not what this test is about).
//  4. `swarm remote init` again -- idempotent, always available, and since S4's remediation
//     the command that converges the machine on the state its device count implies. With one
//     device that means ACTIVATING the gateway.
//
// Running the daemon at step 2 rather than step 0 is deliberate: a resolution that opens the
// socket "once remote is provisioned" must be given a daemon that could see the
// provisioning, or this test would fail it for a condition production does not produce.
func provisionThenActivate(t *testing.T, dir string) (exit int, output string, f *fakeGatewaySupervisor) {
	t.Helper()
	fakeGatewayBinaryOnPath(t)
	f = installFakeSupervisor(t)

	var provOut, provErr bytes.Buffer
	if code := runRemoteInit(nil, &provOut, &provErr); code != 0 {
		t.Fatalf("provisioning `swarm remote init` exit = %d on a machine with zero devices, "+
			"want 0 (PB-LIFE-3(a)); output=%q", code, lastLines(provOut.String()+provErr.String(), 6))
	}
	startInstallDaemon(t, dir)
	seedDevice(t, dir, "Nathan's iPhone", device.CapFull) // PB-LIFE-3(b): paired

	var stdout, stderr bytes.Buffer
	exit = runRemoteInit(nil, &stdout, &stderr)
	return exit, stdout.String() + stderr.String(), f
}

// TestDefaultInstall_GatewayIsServedOrTheOperatorIsTold drives the DEFAULT install against
// a REAL daemon -- no SWARM_DAEMON_REMOTE_SOCK anywhere -- to the moment a gateway is
// activated, and requires the gateway to find a daemon or the operator to be told.
//
// The second subtest is the mutation control, and it is why this test cannot be satisfied
// by simply never starting anything: on the install where the daemon really does serve a
// remote socket, the gateway must still come up.
func TestDefaultInstall_GatewayIsServedOrTheOperatorIsTold(t *testing.T) {
	t.Run("stock install: nothing opted in", func(t *testing.T) {
		dir := shortStateDir(t)
		t.Setenv(daemon.EnvStateDir, dir)
		unsetRemoteSocketEnv(t)
		exit, output, f := provisionThenActivate(t, dir)
		requireServedOrRefused(t, "init", exit, output, f)
	})

	// MUTATION CONTROL. Same machine, same sequence, but the owner HAS opted in, so the
	// daemon really listens on the remote socket. A resolution that fixes the silence by
	// never activating a gateway passes the subtest above and fails this one.
	t.Run("mutation control: remote socket opted in", func(t *testing.T) {
		dir := shortStateDir(t)
		t.Setenv(daemon.EnvStateDir, dir)
		t.Setenv(daemon.EnvRemoteSocket, filepath.Join(dir, "remote.sock"))
		exit, output, f := provisionThenActivate(t, dir)
		if exit != 0 {
			t.Fatalf("runRemoteInit exit = %d on an opted-in install, want 0; output=%q", exit, lastLines(output, 6))
		}
		if got := f.count("ensure"); got != 1 {
			t.Fatalf("Ensure called %d times with one paired device and a served remote socket, "+
				"want 1 (PB-LIFE-2/-3(b)); calls=%v", got, f.calls)
		}
		if err := dialRemoteSocket(f.spec.RemoteSocket); err != nil {
			t.Fatalf("the gateway was started against %q, which is not served: %v", f.spec.RemoteSocket, err)
		}
	})
}

// TestDefaultInstall_NoThrottledRestartLoop states the defect as the SUPERVISOR sees it
// rather than inferring it from a mismatch: take the machine `swarm remote init` just left
// behind, run the gateway's first act against the daemon (a real dial of the socket its
// unit names), and apply the unit's OWN restart policy -- supervise.ExitCodeFor and
// supervise.ShouldRestart, the two functions both unit types are written against -- to the
// outcome. Nothing changes between iterations, so a policy that still says "restart" on the
// last one says it forever: launchd's ThrottleInterval is its whole restart policy and has
// no burst cap to fall out of.
func TestDefaultInstall_NoThrottledRestartLoop(t *testing.T) {
	dir := shortStateDir(t)
	t.Setenv(daemon.EnvStateDir, dir)
	unsetRemoteSocketEnv(t)
	exit, output, f := provisionThenActivate(t, dir)

	if f.count("ensure") == 0 {
		// No gateway was handed to the supervisor, so there is nothing to loop. That is
		// resolution (b) -- but ONLY if the operator was refused loudly; a silent no-op is
		// the same silence by another route.
		requireServedOrRefused(t, "init", exit, output, f)
		return
	}

	const restarts = 3
	for i := 1; i <= restarts; i++ {
		err := dialRemoteSocket(f.spec.RemoteSocket)
		if err == nil {
			return // the gateway finds its daemon: it runs, and there is no loop
		}
		// cmd/swarm-remote's exit(): the error is handed to supervise.ExitCodeFor, and the
		// unit restarts iff supervise.ShouldRestart says so.
		code := supervise.ExitCodeFor(fmt.Errorf("dial daemon %s: %w", f.spec.RemoteSocket, err))
		if !supervise.ShouldRestart(code) {
			return // quiescent, not a failure: the unit leaves it alone (PB-LIFE-3(a)/(c))
		}
		t.Logf("restart %d/%d: gateway dial of %q failed (%v) -> exit %d -> supervisor restarts",
			i, restarts, f.spec.RemoteSocket, err, code)
	}
	t.Fatalf("PB-LIFE-7: `swarm remote init` left a gateway the supervisor restarts on every "+
		"throttle interval and that can never succeed -- %d identical restarts, and nothing "+
		"between them changes.\n"+
		"unit dials: %q\ndaemon listens on: %q\noutput was: %q",
		restarts, f.spec.RemoteSocket, daemonListensOn(t), lastLines(output, 6))
}

// TestRemoteSocket_OneDefinition is PB-LIFE-7's structural half: the path the daemon
// LISTENS on and the path the unit tells the gateway to DIAL must come out of one
// definition, not two that happen to agree. Both are read from production -- the daemon's
// from skeletonConfigFromEnv, the gateway's from the Spec the CLI installs -- so they are
// compared, never restated.
//
// The opted-in row is the control: it already agrees today, so a change that makes the
// stock row pass by breaking this one is caught here rather than in production.
func TestRemoteSocket_OneDefinition(t *testing.T) {
	for _, tc := range []struct {
		name  string
		optIn bool
	}{
		{"stock install (nothing opted in)", false},
		{"control: remote socket opted in", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortStateDir(t)
			t.Setenv(daemon.EnvStateDir, dir)
			if tc.optIn {
				t.Setenv(daemon.EnvRemoteSocket, filepath.Join(dir, "remote.sock"))
			} else {
				unsetRemoteSocketEnv(t)
			}
			seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
			fakeGatewayBinaryOnPath(t)
			f := installFakeSupervisor(t)

			var stdout, stderr bytes.Buffer
			exit := runRemoteInit(nil, &stdout, &stderr)
			output := stdout.String() + stderr.String()

			if exit != 0 {
				// Resolution (b): refused rather than installing a unit that cannot work.
				if !namesAConcreteStep(output) {
					t.Fatalf("runRemoteInit refused (exit %d) without naming a step; output=%q", exit, lastLines(output, 6))
				}
				if f.count("ensure") > 0 {
					t.Fatalf("runRemoteInit refused (exit %d) yet still activated a gateway; calls=%v", exit, f.calls)
				}
				return
			}
			listen, dialTarget := daemonListensOn(t), f.spec.RemoteSocket
			if listen == "" || listen != dialTarget {
				t.Fatalf("PB-LIFE-7: the daemon listens on %q and the installed unit dials %q. "+
					"ADR-007 D4 names one dedicated remote-tier UDS; two definitions that must "+
					"match by coincidence is the bug class. (exit=%d, output=%q)",
					listen, dialTarget, exit, lastLines(output, 6))
			}
		})
	}
}

// TestRemoteInitThenPair_DoesNotLeaveTheGatewayDialingNothing walks the requirement's own
// sequence -- daemon with no remote env var -> `swarm remote init` -> `swarm remote pair` --
// on the verb that actually enrolls the phone. pair calls ensureGatewayRunning against the
// unit init installed, so it is the second place a doomed gateway reaches the supervisor.
//
// It uses this package's scripted pairing host (remote_pair_test.go) for the pairing wire,
// so "served" is decided at the CONFIGURATION level -- the path a real daemon assembled
// from this environment would listen on -- rather than by dialing the fake. Dialing is the
// stronger form and lives in TestDefaultInstall_GatewayIsServedOrTheOperatorIsTold, which
// has a real daemon.
func TestRemoteInitThenPair_DoesNotLeaveTheGatewayDialingNothing(t *testing.T) {
	dir := shortStateDir(t)
	unsetRemoteSocketEnv(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)
	fakeGatewayBinaryOnPath(t)
	f := installFakeSupervisor(t)

	var initOut, initErr bytes.Buffer
	if exit := runRemoteInit(nil, &initOut, &initErr); exit != 0 {
		// Resolution (b) may refuse here; the pair leg below is then unreachable and the
		// refusal is judged on its own terms.
		if !namesAConcreteStep(initOut.String() + initErr.String()) {
			t.Fatalf("runRemoteInit refused (exit %d) without naming a step; output=%q",
				exit, lastLines(initOut.String()+initErr.String(), 6))
		}
		return
	}

	var stdout, stderr bytes.Buffer
	exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr)
	output := stdout.String() + stderr.String()
	if !strings.Contains(stdout.String(), "paired") {
		t.Fatalf("pair did not reach the terminal paired result (exit %d):\n%s", exit, lastLines(output, 6))
	}

	if f.count("ensure") > 0 {
		listen, dialTarget := daemonListensOn(t), f.spec.RemoteSocket
		if listen == "" || listen != dialTarget {
			t.Fatalf("PB-LIFE-7: `swarm remote pair` started the gateway against %q, but a daemon "+
				"assembled from this environment listens on %q. The phone is paired and will be "+
				"served nothing. output=%q", dialTarget, listen, lastLines(output, 6))
		}
		return
	}
	// Resolution (b): no gateway. The pairing is durable and must still report success
	// (PB-LIFE-2's existing contract), so the refusal lands on the output, not the exit code.
	if !namesAConcreteStep(output) {
		t.Fatalf("PB-LIFE-7: `swarm remote pair` enrolled the device, started no gateway, and named "+
			"no step the operator can take. That IS the phone-pairs-then-silence symptom. output=%q",
			lastLines(output, 6))
	}
}

// TestRemoteInit_QuiescentInstallIsStillNotAFailure composes PB-LIFE-7 with PB-LIFE-3(a):
// whatever the resolution, a machine with NO paired device has nothing to serve, the
// desired state is quiescent, and quiescence is not a failure. A resolution (b) that
// refuses on every `swarm remote init` -- rather than at the moment a gateway would
// actually be activated -- turns provisioning a fresh machine into an error and breaks the
// only supported way to install the unit.
//
// This one holds today: it is the fence around the fix, not a statement of the defect.
func TestRemoteInit_QuiescentInstallIsStillNotAFailure(t *testing.T) {
	dir := shortStateDir(t)
	t.Setenv(daemon.EnvStateDir, dir)
	unsetRemoteSocketEnv(t)
	fakeGatewayBinaryOnPath(t)
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d on a machine with zero paired devices, want 0: "+
			"PB-LIFE-3(a) quiescence is NOT a failure; stderr=%q", exit, stderr.String())
	}
	if got := f.count("ensure"); got != 0 {
		t.Errorf("Ensure called %d times with zero paired devices, want 0; calls=%v", got, f.calls)
	}
}
