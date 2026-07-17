package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

// TestKill_TerminatesAndRecordsOutcome asserts E5.6/S-4: Kill routes a signal to
// the session's shim, which terminates the agent's whole process group; the
// outcome is persisted to meta. After Kill the agent PID is gone and the session
// is completed with a recorded exit outcome.
func TestKill_TerminatesAndRecordsOutcome(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m, agentPID := launchAnnounce(t, d)

	if err := d.Kill(m.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	waitProcessGone(t, agentPID, pollTimeout)
	got := waitStatus(t, d, m.ID, status.ProcessExited, pollTimeout)
	if got.Status.Process != status.ProcessExited {
		t.Fatalf("killed session process = %q; want exited", got.Status.Process)
	}
	if got.ExitCode == nil {
		t.Fatalf("killed session ExitCode = nil; want a recorded outcome (S-4)")
	}
}

// TestDelete_RunningKillsThenRemoves asserts E5.6/R-3: deleting a running session
// terminates it and removes the session directory; the session leaves the
// registry.
func TestDelete_RunningKillsThenRemoves(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m, agentPID := launchAnnounce(t, d)
	sessionDir := filepath.Join(cfg.StateDir, m.ID)

	if err := d.Delete(m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	waitProcessGone(t, agentPID, pollTimeout)
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("session dir still present after Delete: stat err = %v", err)
	}
	if _, ok := d.Get(m.ID); ok {
		t.Fatalf("session %s still in registry after Delete", m.ID)
	}
}

// TestMaxSessions_CapEnforced asserts E5.6/S-7: the daemon enforces a
// configurable max concurrent session count and rejects launches over it with a
// clear inline error, tested at a NON-default cap value. The rejected launch
// spawns no shim and does not grow the registry.
func TestMaxSessions_CapEnforced(t *testing.T) {
	cfg := daemonConfig(t)
	cfg.MaxSessions = 2 // non-default
	d := openDaemon(t, cfg)

	launchAnnounce(t, d)
	launchAnnounce(t, d)
	if n := liveCount(d); n != 2 {
		t.Fatalf("live session count = %d; want 2 before cap", n)
	}

	pidFile := filepath.Join(t.TempDir(), "over.pid")
	_, err := d.Launch(announceSpec(t, pidFile))
	if err == nil {
		t.Fatalf("launch over cap succeeded; want ErrMaxSessions")
	}
	if !errors.Is(err, ErrMaxSessions) {
		t.Fatalf("over-cap error = %v; want ErrMaxSessions", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(cfg.MaxSessions)) {
		t.Fatalf("over-cap error %q does not name the cap value %d (unclear message)", err.Error(), cfg.MaxSessions)
	}
	// The rejected launch must not have spawned a shim/agent.
	if _, statErr := os.Stat(pidFile); statErr == nil {
		t.Fatalf("rejected launch spawned an agent; cap must reject before spawn")
	}
	if n := liveCount(d); n != 2 {
		t.Fatalf("live session count = %d after rejected launch; want 2", n)
	}
}

// liveCount counts sessions whose process dimension is running.
func liveCount(d *Daemon) int {
	n := 0
	for _, m := range d.List() {
		if m.Status.Process == status.ProcessRunning {
			n++
		}
	}
	return n
}
