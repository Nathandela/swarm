package supervise

// FAILING-FIRST tests for auto-upgrade-plan.md L2 rule 4: the nightly converge restarts
// the gateway IN PLACE once the daemon side of the upgrade is confirmed reachable.
//
// WHY IN PLACE AND NOT STOP-THEN-ENSURE. restampGatewayUnit's Stop-then-Ensure pair
// (cmd/swarm/remote.go:1682-1685) exists to reload a unit whose ProgramArguments/ExecStart
// now names a DIFFERENT path, and it opens a window with no gateway loaded between the two
// calls. A nightly upgrade restamps nothing -- the unit still execs the same linked path,
// only the binary under that path changed -- so `launchctl kickstart -k` and `systemctl
// restart` are the correct call: they replace the running process without ever unloading
// the unit.
//
// INTENDED PRODUCTION (RED -- Restart does not exist yet on Supervisor or hostSupervisor):
//
//	type Supervisor interface {
//		Install(Spec) error
//		Ensure() error
//		Stop() error
//		Restart() error
//	}
//
//	func (h *hostSupervisor) Restart() error {
//		if err := h.requireUnit(); err != nil {
//			return err
//		}
//		switch h.platform {
//		case PlatformLaunchd:
//			// launchctl kickstart -k <domain>/<label>
//		default:
//			// systemctl --user restart <unit>
//		}
//	}

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// restartRecorder stands in for the real exec of launchctl/systemctl during Restart. Unlike
// the subcommand classifiers in supervisor_test.go and stop_classify_test.go, Restart's
// contract IS its argv -- there is no second opinion to consult -- so this records the
// exact call rather than just its subcommand name.
type restartRecorder struct {
	calls [][]string
	err   error
	out   string
}

func (r *restartRecorder) run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return []byte(r.out), r.err
}

// installedRestartSupervisor returns a supervisor for p whose unit file exists (so
// requireUnit passes) and whose init-system calls go to r instead of the real
// launchctl/systemctl.
func installedRestartSupervisor(t *testing.T, p Platform, r *restartRecorder) *hostSupervisor {
	t.Helper()
	dir := t.TempDir()
	path, err := UnitPath(p, dir)
	if err != nil {
		t.Fatalf("UnitPath(%s): %v", p, err)
	}
	if err := os.MkdirAll(UnitDir(dir), 0o700); err != nil {
		t.Fatalf("create unit dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o600); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	return &hostSupervisor{platform: p, unitPath: path, run: r.run}
}

// TestHostRestart_LaunchdKickstartsInPlace pins the exact launchd argv: kickstart WITH -k
// (a bare kickstart, Ensure's call, is a no-op on an already-running job -- -k is what
// forces the replacement) against this user's own gui domain and the gateway's label.
func TestHostRestart_LaunchdKickstartsInPlace(t *testing.T) {
	r := &restartRecorder{}
	sup := installedRestartSupervisor(t, PlatformLaunchd, r)

	if err := sup.Restart(); err != nil {
		t.Fatalf("Restart() = %v, want nil", err)
	}
	want := [][]string{{"launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), LaunchdLabel)}}
	if fmt.Sprint(r.calls) != fmt.Sprint(want) {
		t.Errorf("Restart() ran %v, want %v", r.calls, want)
	}
}

// TestHostRestart_SystemdRestartsInPlace pins the systemd argv, naming the unit the same
// way Stop does (by name, not by the path Ensure's `enable` needs to link it in the first
// time) -- restart, like disable, acts on a unit systemd has already loaded.
func TestHostRestart_SystemdRestartsInPlace(t *testing.T) {
	r := &restartRecorder{}
	sup := installedRestartSupervisor(t, PlatformSystemd, r)

	if err := sup.Restart(); err != nil {
		t.Fatalf("Restart() = %v, want nil", err)
	}
	want := [][]string{{"systemctl", "--user", "restart", SystemdUnitName}}
	if fmt.Sprint(r.calls) != fmt.Sprint(want) {
		t.Errorf("Restart() ran %v, want %v", r.calls, want)
	}
}

// TestHostRestart_NoUnitInstalledRefusesWithoutRunning is the same refusal Ensure and Stop
// give (requireUnit, BEFORE any init system is touched): a machine where `swarm remote
// init` never ran must never shell out.
func TestHostRestart_NoUnitInstalledRefusesWithoutRunning(t *testing.T) {
	r := &restartRecorder{}
	sup := &hostSupervisor{platform: PlatformLaunchd, unitPath: "/nonexistent/com.swarm.remote.plist", run: r.run}

	err := sup.Restart()
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Restart() = %v, want ErrNotInstalled", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("Restart() shelled out %v with no unit installed", r.calls)
	}
}

// TestHostRestart_RunnerFailureWrapsOutput mirrors Ensure/Stop: a failing init-system call
// is returned wrapped with its combined output, the only place launchd/systemd explain
// themselves.
func TestHostRestart_RunnerFailureWrapsOutput(t *testing.T) {
	for _, p := range []Platform{PlatformLaunchd, PlatformSystemd} {
		t.Run(string(p), func(t *testing.T) {
			r := &restartRecorder{err: errors.New("exit status 3"), out: "Could not find service"}
			sup := installedRestartSupervisor(t, p, r)

			err := sup.Restart()
			if err == nil {
				t.Fatal("Restart() = nil, want the runner's error wrapped")
			}
			if !strings.Contains(err.Error(), "Could not find service") {
				t.Errorf("Restart() error = %q, drops the command's output", err)
			}
		})
	}
}
