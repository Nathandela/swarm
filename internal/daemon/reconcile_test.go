package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// TestReconcile_ReconnectsLiveShim asserts E5.2/L2: a new daemon rebuilds its
// registry from the meta scan and reconnects to a shim that is still alive by
// (PID, start-time) match. A first daemon launches a real long-lived shim, is
// abandoned (kill -9 model), and a second daemon over the same state dir
// reconnects it — status stays running, the agent stays alive.
func TestReconcile_ReconnectsLiveShim(t *testing.T) {
	cfg := daemonConfig(t)

	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	m, agentPID := launchAnnounce(t, d1)

	d1.abandon() // drop fds, no cleanup, no shim signal — the shim survives

	d2 := openDaemon(t, cfg)
	got := waitStatus(t, d2, m.ID, status.ProcessRunning, pollTimeout)
	if got.Status.Process != status.ProcessRunning {
		t.Fatalf("reconnected session process = %q; want running", got.Status.Process)
	}
	if !processAlive(agentPID) {
		t.Fatalf("agent %d died across daemon restart; want alive", agentPID)
	}
	if len(d2.List()) != 1 {
		t.Fatalf("registry size = %d; want 1", len(d2.List()))
	}
}

// TestReconcile_LostOnStartTimeMismatch asserts E5.2/S3: if the recorded PID is
// alive but its start time does not match the meta (the PID-reuse case), the
// session is marked lost and ZERO signals are sent. The target is a live child
// that records any TERM/INT it receives; the recorded start time is deliberately
// wrong.
func TestReconcile_LostOnStartTimeMismatch(t *testing.T) {
	cfg := daemonConfig(t)
	id := "reuse01"
	sigFile := filepath.Join(t.TempDir(), "sig")

	child := exec.Command(selfExe(t), markerCatchTerm, sigFile)
	child.Stdout, child.Stderr = os.Stderr, os.Stderr
	if err := child.Start(); err != nil {
		t.Fatalf("start catch-term child: %v", err)
	}
	childPID := child.Process.Pid
	t.Cleanup(func() { killTree(childPID); _, _ = child.Process.Wait() })

	realStart, err := processStartTime(childPID)
	if err != nil {
		t.Fatalf("processStartTime(%d): %v", childPID, err)
	}
	// Record a WRONG start time: the PID is alive but is not our shim (S3).
	writeRunningMeta(t, cfg.StateDir, id, childPID, realStart+1)

	d := openDaemon(t, cfg)
	got := waitStatus(t, d, id, status.ProcessLost, pollTimeout)
	if got.Status.Process != status.ProcessLost {
		t.Fatalf("mismatched session process = %q; want lost", got.Status.Process)
	}

	// Zero-signal invariant: the live child must be untouched. Give any errant
	// signal a brief window to land, then assert the child is alive and recorded
	// nothing.
	time.Sleep(500 * time.Millisecond)
	if !processAlive(childPID) {
		t.Fatalf("child %d was signalled/killed on identity mismatch; want zero signals", childPID)
	}
	if b, err := os.ReadFile(sigFile); err == nil && len(b) > 0 {
		t.Fatalf("child recorded signal(s) on identity mismatch: %q; want none", b)
	}
}

// TestReconcile_LostOnReapedPID asserts E5.2/S3: a running meta whose recorded
// PID has exited (and been reaped) reconciles to lost, with no signal sent to
// whatever may later hold that PID.
func TestReconcile_LostOnReapedPID(t *testing.T) {
	cfg := daemonConfig(t)
	id := "reaped01"

	child := exec.Command(selfExe(t), markerCatchTerm, filepath.Join(t.TempDir(), "unused"))
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := child.Process.Pid
	start, err := processStartTime(pid)
	if err != nil {
		t.Fatalf("processStartTime(%d): %v", pid, err)
	}
	// Reap it, then reconcile — the PID now names a dead (or unrelated) process.
	_ = child.Process.Kill()
	_, _ = child.Process.Wait()
	writeRunningMeta(t, cfg.StateDir, id, pid, start)

	d := openDaemon(t, cfg)
	got := waitStatus(t, d, id, status.ProcessLost, pollTimeout)
	if got.Status.Process != status.ProcessLost {
		t.Fatalf("reaped-PID session process = %q; want lost", got.Status.Process)
	}
}

// TestReconcile_NeverTransientlyLost asserts E5.3: a live shim is never
// transiently marked lost. The daemon must verify identity and reconnect BEFORE
// persisting any status, so an observer of every daemon meta write never sees
// `lost` for a session that ends up reconnected. A naive "mark all running →
// lost, then un-lost the live ones" implementation fails here.
func TestReconcile_NeverTransientlyLost(t *testing.T) {
	cfg := daemonConfig(t)

	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	m, agentPID := launchAnnounce(t, d1)
	d1.abandon()

	var mu sync.Mutex
	seq := map[string][]status.Process{}
	cfg.onMetaSave = func(mm persist.Meta) {
		mu.Lock()
		seq[mm.ID] = append(seq[mm.ID], mm.Status.Process)
		mu.Unlock()
	}

	d2 := openDaemon(t, cfg)
	waitStatus(t, d2, m.ID, status.ProcessRunning, pollTimeout)
	if !processAlive(agentPID) {
		t.Fatalf("agent %d not alive after reconnect", agentPID)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, s := range seq[m.ID] {
		if s == status.ProcessLost {
			t.Fatalf("session %s was persisted as lost during reconcile: %v; want never (reconnect-before-lost)", m.ID, seq[m.ID])
		}
	}
}
