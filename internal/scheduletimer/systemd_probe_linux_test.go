package scheduletimer

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/Nathandela/swarm/internal/status"
)

// On a Linux VM with a running systemd user manager, from the repository root:
//
//	SWARM_SCHEDULE_SYSTEMD_PROBE=1 go test -count=1 -v ./internal/scheduletimer -run '^TestSystemdColdScheduleTick$'
//
// The probe uses a transient timer, a fresh daemon state and home, and the
// repository's fake Codex. It does not invoke schedule timer install.
func TestSystemdColdScheduleTick(t *testing.T) {
	if os.Getenv("SWARM_SCHEDULE_SYSTEMD_PROBE") != "1" {
		t.Skip("set SWARM_SCHEDULE_SYSTEMD_PROBE=1 on a Linux VM with systemd --user")
	}
	if out, err := exec.Command("systemctl", "--user", "show-environment").Output(); err != nil {
		t.Fatalf("systemd user manager unavailable: %v (%s)", err, out)
	}
	binDir := t.TempDir()
	swarm := filepath.Join(binDir, "swarm")
	codex := filepath.Join(binDir, "codex")
	for name, output := range map[string]string{"swarm": swarm, "swarm-fake-codex": codex} {
		if dir := os.Getenv("SWARM_SCHEDULE_PROBE_BIN_DIR"); dir != "" {
			if err := os.Symlink(filepath.Join(dir, name), output); err != nil {
				t.Fatal(err)
			}
			continue
		}
		cmd := exec.Command("go", "build", "-o", output, "../../cmd/"+name)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, out)
		}
	}
	unit := renderSystemdService(swarm)
	var serviceProperties []string
	for _, line := range strings.Split(unit, "\n") {
		if key, _, ok := strings.Cut(line, "="); ok && slices.Contains([]string{"Type", "KillMode", "RemainAfterExit", "ExitType"}, key) {
			serviceProperties = append(serviceProperties, "--property="+line)
		}
	}
	t.Run("default_cgroup_cleanup", func(t *testing.T) {
		probeColdTick(t, swarm, binDir, []string{"--property=Type=oneshot"}, false)
	})
	t.Run("rendered_service_lifecycle", func(t *testing.T) {
		probeColdTick(t, swarm, binDir, serviceProperties, true)
	})
}

func probeColdTick(t *testing.T, swarm, binDir string, properties []string, survives bool) {
	// Avoid t.TempDir's long test-name prefixes in Unix socket paths.
	root, err := os.MkdirTemp("", "swst")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state, home, work := filepath.Join(root, "state"), filepath.Join(root, "home"), filepath.Join(root, "work")
	for _, dir := range []string{state, home, work} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	pidfile, socket := filepath.Join(state, "daemon.pid"), filepath.Join(state, "daemon.sock")
	if err := os.WriteFile(filepath.Join(state, "daemon.env"), []byte("HOME="+home+"\nPATH="+binDir+":/usr/bin:/bin\nTERM=xterm-256color\n"), 0600); err != nil {
		t.Fatal(err)
	}

	store, err := schedule.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	job, err := store.CreateJob(schedule.JobSpec{
		Name: "cold timer probe", Prompt: "Stay open for the fixture.", Directory: work,
		Workspace: schedule.WorkspaceDirectory, Agents: []string{schedule.RouteCodex},
		Authority: schedule.AuthorityAgentDefault, Grace: 10 * time.Minute,
		Calendar: schedule.Calendar{Kind: schedule.CalendarOnce, TimeZone: "UTC",
			Date: due.Format("2006-01-02"), Time: due.Format("15:04")},
	}, due.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	name := "swarm-schedule-probe-" + filepath.Base(root)
	service, timer := name+".service", name+".timer"
	ctl := func(args ...string) (string, error) {
		out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
		return string(out), err
	}
	t.Cleanup(func() {
		if t.Failed() {
			journal, _ := exec.Command("journalctl", "--user", "--unit", service, "--no-pager", "-n", "40").CombinedOutput()
			log, _ := os.ReadFile(filepath.Join(state, "daemon.log"))
			t.Logf("journal for %s:\n%s\ndaemon log:\n%s", service, journal, log)
		}
		_, _ = ctl("stop", timer, service)
		// Stopping a KillMode=process service can leave its cgroup populated.
		if group, err := ctl("show", "--property=ControlGroup", "--value", service); err == nil && strings.TrimSpace(group) != "" {
			if out, err := ctl("kill", "--kill-who=all", "--signal=SIGKILL", service); err != nil {
				t.Errorf("cleanup %s: %v\n%s", service, err, out)
			}
		}
		_, _ = ctl("reset-failed", service)
	})
	for _, path := range []string{socket, pidfile} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("expected cold fixture at %s: %v", path, err)
		}
	}
	args := []string{"--user", "--unit=" + name, "--on-active=1s", "--on-unit-active=5s", "--timer-property=AccuracySec=100ms", "--property=TimeoutStartSec=20s", "--property=TimeoutStopSec=3s"}
	args = append(args, properties...)
	// env execs the real tick with a clean environment. TestRenderUnits covers
	// the production unit's shell forwarding and quoting separately.
	args = append(args, "--", "/usr/bin/env", "-i", "HOME="+home, "PATH=/usr/bin:/bin",
		"SWARM_DAEMON_STATE="+state, swarm, "schedule", "tick")
	if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
		t.Fatalf("start transient timer: %v\n%s", err, out)
	}

	waitForTick := func(after uint64) uint64 {
		t.Helper()
		var out string
		for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			var err error
			out, err = ctl("show", service, "--property=ActiveState,Result,ExecMainCode,ExecMainStatus,ExecMainExitTimestampMonotonic")
			if err != nil {
				t.Fatalf("inspect %s: %v\n%s", service, err, out)
			}
			values := make(map[string]string)
			for _, line := range strings.Split(out, "\n") {
				key, value, _ := strings.Cut(line, "=")
				values[key] = value
			}
			exited, _ := strconv.ParseUint(values["ExecMainExitTimestampMonotonic"], 10, 64)
			if exited > after && values["ActiveState"] == "inactive" &&
				values["Result"] == "success" && values["ExecMainCode"] == "1" && values["ExecMainStatus"] == "0" {
				t.Logf("%s exited successfully and is inactive (monotonic exit %d)", service, exited)
				return exited
			}
			if values["ActiveState"] == "failed" {
				break
			}
		}
		t.Fatalf("no successful inactive tick after %d:\n%s", after, out)
		return 0
	}

	first := waitForTick(0)
	runs, err := store.Occurrences()
	if err != nil || len(runs) != 1 || runs[0].JobID != job.ID || len(runs[0].Attempts) != 1 {
		t.Fatalf("expected one scheduled worker attempt: %+v, %v", runs, err)
	}
	attempt := runs[0].Attempts[0]
	_, local, ok := protocol.ParseID(attempt.SessionID)
	if !ok || runs[0].Manual || attempt.State != schedule.AttemptRunning {
		t.Fatalf("tick did not launch the fixture worker: %+v", runs[0])
	}
	sessions, err := persist.NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := sessions.Load(local)
	if err != nil || meta.ScheduleAttempt != attempt.ID || meta.ShimPID <= 0 || meta.ShimStartTime <= 0 {
		t.Fatalf("scheduled worker does not identify its shim: %+v, %v", meta, err)
	}
	shim := processcontain.Identity{PID: meta.ShimPID, StartTime: meta.ShimStartTime}

	if !survives {
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			conn, err := net.DialTimeout("unix", socket, time.Second)
			if err == nil {
				_ = conn.Close()
			} else if !shim.Alive() {
				t.Logf("control reproduced loss of the daemon and worker shim %d", shim.PID)
				return
			}
		}
		t.Fatal("default cgroup cleanup did not remove the daemon and scheduled shim")
	}

	content, err := os.ReadFile(pidfile)
	var daemon processcontain.Identity
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscan(string(content), &daemon.PID, &daemon.StartTime); err != nil {
		t.Fatal(err)
	}
	checkLive := func() {
		t.Helper()
		for _, process := range []processcontain.Identity{daemon, shim} {
			cgroup, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", process.PID))
			if !process.Alive() || err != nil || !strings.Contains(string(cgroup), "/"+service+"\n") {
				t.Fatalf("original process %+v is not live in %s: %s %v", process, service, cgroup, err)
			}
		}
		client, err := protocol.Dial(socket, []string{protocol.CapSchedule})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		sessions, err := client.List()
		if err != nil || len(sessions) != 1 || sessions[0].ID != attempt.SessionID || sessions[0].Status.Process != status.ProcessRunning {
			t.Fatalf("direct daemon connection did not see the running scheduled worker: %+v, %v", sessions, err)
		}
		t.Logf("daemon %+v and scheduled shim %+v remain live in %s", daemon, shim, service)
	}
	checkLive()
	// Wait for the next firing of the same recurring timer.
	waitForTick(first)
	checkLive()
}
