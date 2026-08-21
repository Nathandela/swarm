package daemon

// FAILING-FIRST (TDD RED, GG-5) for round-4 r7-check finding 1: markLost (Kill's
// pre-signal identity recheck, run on an RPC goroutine) and handleShimExit (the shim
// monitor) can both call reapOrphanBackend(id) for the SAME session with nothing
// serializing them. The reaper's sequence is read-validate-kill-remove over shared
// on-disk state: two interleaved runs can both read backend.json and both validate the
// recorded (pid, start-time); the first then kills the group and removes the record --
// and if the pid is recycled inside that window, the second, already past its own
// validate, signals a group that is no longer ours. Astronomically narrow (it needs a
// full pid-recycle inside a microsecond window), but it is exactly the TOCTOU class the
// identity check exists to close, so the whole sequence must be one critical section.
//
// HOW THE TEST SEES IT. backendAliveAt's identity read goes through the procStartTimeFn
// seam, and reapOrphanBackend is backendAliveAt's ONLY caller -- so a hook there sits
// inside the reaper's validate step and nothing else. The hook counts concurrent
// entrants for the recorded backend pid and holds the validate open long enough that an
// unserialized twin must land inside it; it then reports the identity unreadable, which
// routes both runs into the arm that signals NOTHING (only a test fixture would be at
// risk regardless). Serialized, the second reap finds the record already removed and
// never reaches the hook at all.
//
// MUTATION: delete the reapMu lock/unlock from reapOrphanBackend. This test fails.

import (
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestR7R4_ConcurrentShimDeathPathsSerializeTheReap(t *testing.T) {
	// Install the hook BEFORE the daemon opens: the var must never be written while a
	// daemon goroutine could read it (worktree_rollback_test.go's discipline). The
	// restore is registered first, so it runs LAST -- after openDaemon's cleanup has
	// closed the daemon.
	var inFlight, overlaps atomic.Int32
	var backendPID atomic.Int64
	orig := procStartTimeFn
	procStartTimeFn = func(pid int) (int64, error) {
		if int64(pid) != backendPID.Load() {
			return orig(pid)
		}
		if inFlight.Add(1) > 1 {
			overlaps.Add(1)
		}
		// Hold the validate open so an unserialized concurrent reap lands inside it.
		time.Sleep(150 * time.Millisecond)
		inFlight.Add(-1)
		return 0, errors.New("identity unreadable (test hook)")
	}
	t.Cleanup(func() { procStartTimeFn = orig })

	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	// A running session over a shim pid that is genuinely dead and reaped, so Kill's
	// identity recheck fails and takes the markLost path (r7r3_marklost_test.go's seed).
	probe := exec.Command("/bin/sh", "-c", "exit 0")
	if err := probe.Run(); err != nil {
		t.Fatalf("spawn dead-shim stand-in: %v", err)
	}
	const id = "r7r4reap"
	now := time.Now()
	if err := d.saveMeta(persist.Meta{
		ID:            id,
		AgentType:     "fake",
		CreatedAt:     now,
		LastActivity:  now,
		ShimPID:       probe.Process.Pid,
		ShimStartTime: 424242,
		Status:        status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
	}); err != nil {
		t.Fatalf("seed running session over a dead shim: %v", err)
	}

	// The recorded backend is a live fixture in its own group, so even a wrongly-issued
	// signal could only ever hit the squatter -- and the unreadable-identity arm the hook
	// forces signals nothing at all.
	dir := d.sessionDir(id)
	bpid := r7Squatter(t, filepath.Join(dir, "codex.sock"))
	r7WriteBackendInfo(t, dir, bpid, bpid, 424242, filepath.Join(dir, "codex.sock"))
	backendPID.Store(int64(bpid))

	// The two REAL shim-death paths, concurrently: Kill->markLost on one goroutine (the
	// RPC shape) and handleShimExit on another (what the monitor calls on shim death).
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		if err := d.Kill(id); err != nil {
			t.Errorf("Kill: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		d.handleShimExit(id)
	}()
	close(start)
	wg.Wait()

	if n := overlaps.Load(); n > 0 {
		t.Errorf("reapOrphanBackend entered its identity validation %d time(s) while another "+
			"reap of the same session was still inside its own read-validate-kill-remove "+
			"sequence. Two interleaved reaps can both validate the same recorded pid, the "+
			"first kills and removes the record, and a pid recycled in that window hands the "+
			"second one's signal to a stranger's group (r7-check round 4, finding 1)", n)
	}
}
