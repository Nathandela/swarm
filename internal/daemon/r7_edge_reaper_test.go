package daemon

// FAILING-FIRST (TDD RED, GG-5) for the R7 reaper EDGE the final audit committee flagged
// (codex HIGH-7): reapOrphanBackend killed the group ONLY when the recorded leader pid was
// alive with a matching start time; a leader that had already died left the record removed and
// the group UNTOUCHED. But `codex app-server` is TWO pids (R1 leg 1: a node launcher plus the
// vendored rust binary), so "the leader died" does not mean "the backend is gone" -- the child
// survives in the leader's group, authenticated, serving, and now unrecorded forever.
//
// The counterpart guard: when the recorded leader pid is ALIVE but its start time disagrees,
// the pid was recycled -- and POSIX only recycles a pid once its old process GROUP has no
// members left, so nothing of the recorded backend can still exist and signalling -pgid would
// hit a stranger. The reaper must never do that.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// r7EdgeDeadLeaderGroup forges the failure shape: a group whose LEADER exited while a child
// lives on in it. The sh leader backgrounds a sleep (same group -- no Setpgid on the child),
// prints its pid, and exits; cmd.Output reaps the leader HERE, so it is fully dead (never a
// zombie) by the time this returns. The leader's pid cannot be recycled while the child keeps
// the group alive (POSIX process-group lifetime), so the synthesized state is stable.
func r7EdgeDeadLeaderGroup(t *testing.T) (leader, child int) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 300 >/dev/null 2>&1 & echo $!")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("spawn dead-leader group: %v", err)
	}
	child, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || child <= 0 {
		t.Fatalf("parse surviving child pid from %q: %v", out, err)
	}
	leader = cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(child, syscall.SIGKILL)
		_ = syscall.Kill(-leader, syscall.SIGKILL)
	})
	if syscall.Kill(leader, 0) == nil {
		t.Fatalf("the leader (pid %d) is still alive; the fixture needs it dead", leader)
	}
	if syscall.Kill(child, 0) != nil {
		t.Fatalf("the child (pid %d) did not survive its leader", child)
	}
	return leader, child
}

// TestR7Edge_ADeadLeadersSurvivingChildIsStillReapedByGroup is edge 1. The record names a
// leader that is GONE; the reaper must still signal the GROUP, because whoever remains in it
// IS the recorded backend's (the group id cannot have been reused while members remain), and
// then remove the record.
//
// MUTATION: delete the dead-leader kill(-pgid) arm from reapOrphanBackend. This test fails.
func TestR7Edge_ADeadLeadersSurvivingChildIsStillReapedByGroup(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m, _ := launchAnnounce(t, d) // launchAnnounce, so the shim and agent are reaped at cleanup
	dir := d.sessionDir(m.ID)

	leader, child := r7EdgeDeadLeaderGroup(t)
	r7WriteBackendInfo(t, dir, leader, leader, time.Now().UnixMilli(), filepath.Join(dir, "codex.sock"))

	d.reapOrphanBackend(m.ID)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(child, 0) != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(child, 0) == nil {
		t.Errorf("the surviving child (pid %d) of the dead leader (pid %d) outlived the reaper. "+
			"`codex app-server` is TWO pids; a reaper that only acts when the LEADER is alive "+
			"leaves the vendored binary running, authenticated and unrecorded, forever", child, leader)
	}
	if _, err := os.Stat(filepath.Join(dir, "backend.json")); err == nil {
		t.Error("backend.json survived the reap; a stale file makes the NEXT reconcile try to reap " +
			"a pid that is now somebody else's")
	}
}

// TestR7Edge_ARecycledLeaderPidIsNeverSignalled is the guard that keeps edge 1's fix from
// over-reaching. The record names a pid that is ALIVE but whose start time disagrees: a
// recycled pid, leading (or belonging to) a STRANGER's group. The reaper must not signal the
// group -- POSIX guarantees the recorded backend's group emptied before its pid could be
// recycled, so there is provably nothing of ours left to reap -- and must still remove the
// record so reconcile never retries this dead end.
//
// MUTATION: make the dead-leader arm unconditional (drop the alive-but-mismatched guard).
// This test fails.
func TestR7Edge_ARecycledLeaderPidIsNeverSignalled(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m, _ := launchAnnounce(t, d) // launchAnnounce, so the shim and agent are reaped at cleanup
	dir := d.sessionDir(m.ID)

	// A live, unrelated group leader standing in for the recycled pid. r7Squatter is the
	// existing fixture: its own group, background-reaped, cleaned up by the test.
	impostor := r7Squatter(t, filepath.Join(shortStateDir(t), "imp.sock"))
	st, err := procStartTimeFn(impostor)
	if err != nil {
		t.Fatalf("procStartTimeFn(%d): %v", impostor, err)
	}
	r7WriteBackendInfo(t, dir, impostor, impostor, st+987654, filepath.Join(dir, "codex.sock"))

	d.reapOrphanBackend(m.ID)

	// Signals are delivered synchronously enough that a wrongly-signalled group dies within
	// this window; the impostor surviving it is the guard holding.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if syscall.Kill(impostor, 0) != nil {
			t.Fatalf("reapOrphanBackend signalled a live pid (%d) whose start time does not match "+
				"the record: that is a RECYCLED pid, a stranger's process, and the one thing the "+
				"identity check exists to protect", impostor)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "backend.json")); err == nil {
		t.Error("backend.json survived; an unmatchable record must still be dropped or every " +
			"future reconcile retries the same dead end")
	}
}
