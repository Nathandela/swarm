package daemon

// FAILING-FIRST (TDD RED, GG-5) for the SECOND R7 containment race of the committee's
// round-2 codex finding 4: markLost is a THIRD shim-death path, and it reaped nothing.
//
// The interleaving: Kill(id) re-verifies the shim's identity before signalling; when the
// shim is ALREADY DEAD (or its pid was recycled) it calls markLost, which finalizes the
// session as LOST -- and performed no backend sweep. The monitor's own handleShimExit then
// finds the session already terminal with no exit.json (a SIGKILLed shim writes none) and
// takes its early return BEFORE its reapOrphanBackend call; a later daemon restart skips
// the session too, because reconcile's orphan arm only visits sessions persisted RUNNING.
// Net effect: the dead shim's backend -- a process authenticated to a real ChatGPT
// account, with no PTY and no stream anybody watches -- survives indefinitely.
//
// The rule this test freezes: the markLost path triggers the SAME reapOrphanBackend sweep
// the other two death paths (handleShimExit, reconcile) already get.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// TestR7R3MarkLost_AKillObservingADeadShimStillReapsTheBackend drives the REAL production
// caller of markLost -- Kill's pre-signal identity recheck -- against a session whose
// recorded shim is already dead while its recorded backend is alive. No monitor is watching
// this session, which is exactly the window the race needs: Kill's markLost wins the
// finalize, so the supervisor's handleShimExit (had one been running) would only ever see an
// already-terminal session and skip its own reap.
//
// MUTATION: delete the reap from markLost. This test fails.
func TestR7R3MarkLost_AKillObservingADeadShimStillReapsTheBackend(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	// A shim pid that is genuinely DEAD and fully reaped: identity can never match.
	probe := exec.Command("/bin/sh", "-c", "exit 0")
	if err := probe.Run(); err != nil {
		t.Fatalf("spawn dead-shim stand-in: %v", err)
	}
	deadShimPID := probe.Process.Pid

	const id = "r7r3lost"
	now := time.Now()
	if err := d.saveMeta(persist.Meta{
		ID:            id,
		AgentType:     "fake",
		CreatedAt:     now,
		LastActivity:  now,
		ShimPID:       deadShimPID,
		ShimStartTime: 424242,
		Status:        status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
	}); err != nil {
		t.Fatalf("seed running session over a dead shim: %v", err)
	}

	// The dead shim's still-live backend, recorded exactly as the shim records it.
	dir := d.sessionDir(id)
	sock := filepath.Join(dir, "codex.sock")
	bpid := r7Squatter(t, sock)
	st, err := procStartTimeFn(bpid)
	if err != nil {
		t.Fatalf("procStartTimeFn(%d): %v", bpid, err)
	}
	r7WriteBackendInfo(t, dir, bpid, bpid, st, sock)

	// The kill. Identity recheck fails (the shim is dead), so this is the markLost path;
	// nothing else in this test ever calls a reaper.
	if err := d.Kill(id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if cur, ok := d.Get(id); !ok || cur.Status.Process != status.ProcessLost {
		t.Fatalf("precondition broke: Kill over a dead shim did not mark the session lost (got %+v)", cur.Status)
	}

	r7AwaitDead(t, bpid, 10*time.Second,
		"the backend survived Kill's markLost path. The session is now persisted LOST, so the "+
			"monitor's handleShimExit early-returns before ITS reap and reconcile never revisits "+
			"a non-running session: nothing in the system will ever reap this orphan (§R7.2c)")
	if _, err := os.Stat(filepath.Join(dir, "backend.json")); err == nil {
		t.Error("backend.json survived the markLost reap; a stale record makes the next reconcile " +
			"try to reap a pid that may now be somebody else's")
	}
}
