package supervise

// FAILING-FIRST tests for the re-pair half of PB-LIFE-3(c): `swarm remote pair` must ensure
// a gateway is RUNNING, on the second pairing as much as the first.
//
// WHY THIS IS THE PATH THAT BREAKS. Nothing in production calls Stop today, so after
// init -> pair -> revoke -> re-pair the launchd job is still bootstrapped in the owner's
// domain and every re-pair takes Ensure's already-loaded branch. That branch is currently
// decided by SNIFFING launchctl's message for "already" or "file exists" -- and macOS
// commonly reports an already-bootstrapped label as
//
//	Bootstrap failed: 5: Input/output error
//
// which carries neither substring. The re-pair then returns the bootstrap error and NEVER
// reaches kickstart: the operator is told "the gateway was not started", and it genuinely
// was not.
//
// INTENDED PRODUCTION (RED — the seam and the behavior do not exist yet):
//
//	// hostSupervisor gains a `run` field (defaulting to runUnit) so a test can drive
//	// Ensure/Stop without a real init system.
//
//	// Ensure (launchd): run bootstrap, IGNORE its error entirely, then let kickstart
//	// decide. kickstart fails only if the label really is not loaded, so the classifier
//	// -- and its dependence on a message Apple never documented -- goes away.
//
// The assertions below are therefore message-INDEPENDENT: they script launchctl's exit
// status, not its prose.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// scriptedLaunchctl stands in for the real exec of launchctl/systemctl. It keys both its
// recorded calls and its scripted failures by SUBCOMMAND (bootstrap, kickstart, bootout),
// which is the only part of the command line these tests care about.
type scriptedLaunchctl struct {
	ran  []string          // subcommands, in order
	fail map[string]error  // subcommand -> exit error
	out  map[string]string // subcommand -> combined output
}

func (s *scriptedLaunchctl) run(name string, args ...string) ([]byte, error) {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		// systemctl's first arg is --user; its verb is next.
		if sub == "--user" && len(args) > 1 {
			sub = args[1]
		}
	}
	s.ran = append(s.ran, sub)
	return []byte(s.out[sub]), s.fail[sub]
}

func (s *scriptedLaunchctl) didRun(sub string) bool {
	for _, r := range s.ran {
		if r == sub {
			return true
		}
	}
	return false
}

// installedLaunchdSupervisor returns a launchd supervisor whose unit file exists (so
// requireUnit passes) and whose init-system calls go to r instead of the real launchctl.
// The platform is pinned to launchd regardless of the host GOOS: this is a property of the
// launchd path, and the Linux CI box must check it too.
func installedLaunchdSupervisor(t *testing.T, r *scriptedLaunchctl) *hostSupervisor {
	t.Helper()
	dir := t.TempDir()
	path, err := UnitPath(PlatformLaunchd, dir)
	if err != nil {
		t.Fatalf("UnitPath: %v", err)
	}
	if err := os.MkdirAll(UnitDir(dir), 0o700); err != nil {
		t.Fatalf("create unit dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("<plist/>"), 0o600); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	return &hostSupervisor{platform: PlatformLaunchd, unitPath: path, run: r.run}
}

// TestHostEnsure_AlreadyBootstrappedStillKickstarts is the re-pair path. bootstrap fails
// with the message macOS uses for an already-loaded label -- one that says nothing about
// being already loaded -- and Ensure must still reach kickstart and report success.
func TestHostEnsure_AlreadyBootstrappedStillKickstarts(t *testing.T) {
	r := &scriptedLaunchctl{
		fail: map[string]error{"bootstrap": errors.New("exit status 5")},
		out:  map[string]string{"bootstrap": "Bootstrap failed: 5: Input/output error"},
	}
	sup := installedLaunchdSupervisor(t, r)

	if err := sup.Ensure(); err != nil {
		t.Fatalf("Ensure() with an already-bootstrapped job = %v, want nil; "+
			"every re-pair after a revoke takes this path (PB-LIFE-3(c))", err)
	}
	if !r.didRun("kickstart") {
		t.Errorf("Ensure() returned before kickstart; calls=%v. bootstrap's error cannot decide "+
			"whether the job is loaded -- only kickstart can", r.ran)
	}
}

// TestHostEnsure_ClassifiesNothingByMessage is the same property stated as a rule: NO
// bootstrap output, however worded, may stop Ensure from kickstarting. A classifier that
// reads the message is a coin flip on a string Apple has never documented.
func TestHostEnsure_ClassifiesNothingByMessage(t *testing.T) {
	for _, out := range []string{
		"Bootstrap failed: 5: Input/output error",
		"Load failed: 37: Operation already in progress",
		"Bootstrap failed: 17: File exists",
		"Boostrap failed: 125: Domain does not support specified action",
		"",
	} {
		t.Run(out, func(t *testing.T) {
			r := &scriptedLaunchctl{
				fail: map[string]error{"bootstrap": errors.New("exit status 5")},
				out:  map[string]string{"bootstrap": out},
			}
			sup := installedLaunchdSupervisor(t, r)
			if err := sup.Ensure(); err != nil {
				t.Fatalf("Ensure() = %v, want nil: bootstrap output %q must not decide the outcome", err, out)
			}
			if !r.didRun("kickstart") {
				t.Errorf("kickstart never ran for bootstrap output %q; calls=%v", out, r.ran)
			}
		})
	}
}

// TestHostEnsure_KickstartFailureIsTheRealFailure: kickstart is the ONE thing that decides.
// It fails only when the label is not loaded, so its failure is a genuine "the gateway is
// not running" and must be reported with both commands' output -- bootstrap's is the only
// place launchd explains WHY the load did not happen.
func TestHostEnsure_KickstartFailureIsTheRealFailure(t *testing.T) {
	r := &scriptedLaunchctl{
		fail: map[string]error{
			"bootstrap": errors.New("exit status 5"),
			"kickstart": errors.New("exit status 3"),
		},
		out: map[string]string{
			"bootstrap": "Bootstrap failed: 5: Input/output error",
			"kickstart": `Could not find service "com.swarm.remote" in domain for uid: 501`,
		},
	}
	sup := installedLaunchdSupervisor(t, r)

	err := sup.Ensure()
	if err == nil {
		t.Fatal("Ensure() = nil though kickstart failed; a gateway that is not loaded is not running")
	}
	if !strings.Contains(err.Error(), "kickstart") {
		t.Errorf("Ensure() error = %q, want it to name kickstart as the failure", err)
	}
	if !strings.Contains(err.Error(), "Could not find service") {
		t.Errorf("Ensure() error = %q, drops kickstart's own output", err)
	}
	if !strings.Contains(err.Error(), "Bootstrap failed") {
		t.Errorf("Ensure() error = %q; when the load itself failed, bootstrap's output is the "+
			"only explanation launchd gives", err)
	}
}

// TestHostEnsure_HappyPathBootstrapsThenKickstarts pins the order: the unit is loaded
// before it is started, and both run exactly once (Ensure is never a restart).
func TestHostEnsure_HappyPathBootstrapsThenKickstarts(t *testing.T) {
	r := &scriptedLaunchctl{}
	sup := installedLaunchdSupervisor(t, r)

	if err := sup.Ensure(); err != nil {
		t.Fatalf("Ensure() = %v, want nil", err)
	}
	if got := fmt.Sprint(r.ran); got != "[bootstrap kickstart]" {
		t.Errorf("Ensure() ran %v, want [bootstrap kickstart]", r.ran)
	}
}

// TestRunUnit_RefusesUnderTest is the structural half of the isolation this package has so
// far kept by convention (N4): one added Ensure() line in a test that installed a real unit
// would launchctl bootstrap + kickstart com.swarm.remote on a developer's own machine. The
// real runner refuses when it is running inside a test binary, so the only way to exercise
// Ensure/Stop in a test is to substitute a runner -- which is what every test above does.
func TestRunUnit_RefusesUnderTest(t *testing.T) {
	out, err := runUnit("launchctl", "bootstrap", "gui/501", "/nonexistent.plist")
	if err == nil {
		t.Fatalf("runUnit(launchctl bootstrap) = %q, nil under `go test`; it must refuse to touch "+
			"the developer's own init system", out)
	}
	if !strings.Contains(err.Error(), "test") {
		t.Errorf("runUnit error = %q, want it to name the test binary as the reason", err)
	}
}
