package daemon

// WAVE R7, ROUND 3 -- the SIGKILL residual, on BOTH of its paths and through the REAL callers.
// Bead: agents-tracker-hggx.8. ADR-013 §R7.2c.
//
// WHAT ROUND 2 LEFT OPEN, and it was the more common half. reapOrphanBackend had exactly ONE
// production call site -- reconcile's orphan arm -- which runs only when a daemon STARTS.
// A shim killed while the daemon is UP goes to pollMonitor/superviseLaunched -> handleShimExit,
// which marked the session LOST and reaped nothing; reconcile then skips any session not
// persisted RUNNING, so no later restart revisited it either. PROBED: after handleShimExit the
// recorded backend pid was still alive. That is verbatim the scenario backend.go opens by
// describing -- "a SIGKILL of its shim leaves a process authenticated to a real ChatGPT account
// alive indefinitely, still serving <session-dir>/codex.sock".
//
// AND THE HALF THAT WAS CLOSED WAS UNFENCED: deleting the reconcile call site left
// ./internal/daemon green, because both reaper tests called the helper directly and nothing
// drove reconcile's orphan path. Both tests here drive a REAL production caller against a REAL
// shim process, so a reaper that stops being CALLED fails them.

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// r7AwaitDead polls until pid is gone, and fails saying why an orphan matters.
func r7AwaitDead(t *testing.T, pid int, within time.Duration, why string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Error(why)
}

// TestR7R3Reaper_AShimKILLEDWhileTheDaemonIsUPHasItsBackendREAPED is the hole.
//
// It drives the REAL path end to end: a REAL shim is launched, its backend is recorded, the
// shim is SIGKILLed (so it can never run its own TERM->grace->KILL over the backend), and the
// daemon's OWN supervisor -- superviseLaunched, watching the child it spawned -- is what must
// notice and reap. Nothing in the test calls the reaper.
//
// MUTATION: delete `d.reapOrphanBackend(id)` from handleShimExit. This test fails.
func TestR7R3Reaper_AShimKILLEDWhileTheDaemonIsUPHasItsBackendREAPED(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m, _ := launchAnnounce(t, d)

	dir := d.sessionDir(m.ID)
	sock := filepath.Join(dir, "codex.sock")
	pid := r7Squatter(t, sock)
	st, err := procStartTimeFn(pid)
	if err != nil {
		t.Fatalf("procStartTimeFn: %v", err)
	}
	r7WriteBackendInfo(t, dir, pid, pid, st, sock)

	// THE UNCATCHABLE SIGNAL: the shim gets no chance to contain anything.
	if err := syscall.Kill(m.ShimPID, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL the shim: %v", err)
	}

	// The daemon's own supervisor finalizes the session...
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cur, ok := d.Get(m.ID); ok && cur.Status.Process != status.ProcessRunning {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if cur, ok := d.Get(m.ID); !ok || cur.Status.Process == status.ProcessRunning {
		t.Fatalf("the daemon never noticed its shim died; the reaper cannot be reached from here")
	}

	// ...and the backend that shim owned must not outlive it.
	r7AwaitDead(t, pid, 10*time.Second,
		"the session backend survived the death of its shim while the daemon was UP. It has no PTY "+
			"to HUP it and no stream anybody watches, so it keeps serving the session's socket "+
			"forever while holding real account credentials, and nothing else in the system will "+
			"ever reap it (§R7.2c)")
}

// TestR7R3Reaper_ARESTARTOverADeadShimREAPSTheOrphanThroughRECONCILE fences the call site round
// 2 shipped, from the caller rather than the helper.
//
// The daemon is CLOSED FIRST so its own supervisor cannot be the party that reaps: what runs
// here is `swarm daemon restart` over a state dir whose shim died in between, which is
// reconcile's orphan arm and nothing else.
//
// MUTATION: delete `d.reapOrphanBackend(m.ID)` from reconcileRunning. This test fails.
func TestR7R3Reaper_ARESTARTOverADeadShimREAPSTheOrphanThroughRECONCILE(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m, _ := launchAnnounce(t, d)

	dir := d.sessionDir(m.ID)
	sock := filepath.Join(dir, "codex.sock")
	pid := r7Squatter(t, sock)
	st, err := procStartTimeFn(pid)
	if err != nil {
		t.Fatalf("procStartTimeFn: %v", err)
	}
	r7WriteBackendInfo(t, dir, pid, pid, st, sock)

	// The daemon goes away FIRST (ADR-001's ordinary operation), and only then does the shim
	// die. Nothing is watching, which is exactly the situation reconcile exists for.
	if err := d.Close(); err != nil {
		t.Fatalf("Close the first daemon: %v", err)
	}
	if err := syscall.Kill(m.ShimPID, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL the shim: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && syscall.Kill(m.ShimPID, 0) == nil {
		time.Sleep(25 * time.Millisecond)
	}
	if syscall.Kill(m.ShimPID, 0) == nil {
		t.Fatalf("the shim (pid %d) is still alive; the restart would legitimately adopt it", m.ShimPID)
	}

	// The restart. Open runs reconcile synchronously.
	d2 := openDaemon(t, cfg)
	if cur, ok := d2.Get(m.ID); !ok || cur.Status.Process != status.ProcessLost {
		t.Fatalf("the restarted daemon did not classify the vanished shim's session as LOST; got %v", cur.Status.Process)
	}

	r7AwaitDead(t, pid, 10*time.Second,
		"the orphaned session backend survived a daemon restart over its dead shim. reconcile's "+
			"orphan arm is the ONLY party that can reap it once both the shim and the previous "+
			"daemon are gone (§R7.2c)")
}
