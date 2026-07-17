package e2e

// Kill -9 the daemon DURING active client input writes (E14.3 T7a, invariant S1).
// TestE2E_DaemonKilledMidAttach kills mid-ATTACH (an idle attachment); this kills
// while a client is ACTIVELY streaming PTY input through the daemon (mid-write), the
// harsher case: the input relay is severed mid-frame. The shim and its agent must
// still survive (S1), and after a restart a fresh client must reconnect to the SAME
// session — not a relaunch — with its grid and transcript intact.

import (
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func TestE2E_DaemonKilledMidInputWrite(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)

	// Launch a fake agent that prints a marker then idles, and attach once its output
	// has settled into the grid.
	d1 := startDaemon(t, env)
	c := dial(t, env.sock)
	id := launchFakeSession(t, c, "print MID-WRITE\nidle 600s\n")
	waitOneView(t, c)

	a, _ := attachWhenGridHas(t, c, id, "MID-WRITE")
	if len(a.Snapshot()) == 0 {
		t.Fatal("attach painted an empty snapshot before the kill")
	}

	// The shim and the AGENT it owns — both must survive the mid-write kill (S1).
	local := localOf(t, id)
	meta := readMeta(t, env.stateDir, local)
	if !alive(meta.ShimPID) {
		t.Fatalf("shim %d not alive before kill", meta.ShimPID)
	}
	agentPID := agentPIDOf(t, meta.ShimPID)
	if !alive(agentPID) {
		t.Fatalf("agent %d (child of shim %d) not alive before kill", agentPID, meta.ShimPID)
	}

	// Drain the live frame stream so the client read-loop never backs up while we
	// stream input; it exits when the lease closes (the daemon dies).
	var drain sync.WaitGroup
	drain.Add(1)
	go func() {
		defer drain.Done()
		for range a.Frames() {
		}
	}()

	// Actively stream PTY input through the daemon. Single non-newline bytes keep the
	// echoed input on one line (the marker is on row 0 and cannot scroll off), and the
	// tight loop guarantees writes are genuinely in flight when the daemon is killed.
	stopStream := make(chan struct{})
	var stream sync.WaitGroup
	stream.Add(1)
	go func() {
		defer stream.Done()
		for {
			select {
			case <-stopStream:
				return
			default:
			}
			if err := a.Input([]byte("x")); err != nil {
				return // the daemon socket died mid-write, as expected
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Let the input stream get genuinely in flight, then kill -9 the daemon mid-write.
	time.Sleep(30 * time.Millisecond)
	if err := syscall.Kill(d1.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill -9 daemon mid-write: %v", err)
	}
	close(stopStream)
	stream.Wait()
	drain.Wait()

	// Give the OS a moment to release the flock, then assert S1: the shim and agent
	// survived the daemon dying mid-write.
	time.Sleep(200 * time.Millisecond)
	if !alive(meta.ShimPID) {
		t.Fatal("shim died when the daemon was kill -9'd mid-write — violates S1")
	}
	if !alive(agentPID) {
		t.Fatalf("agent %d died when the daemon was kill -9'd mid-write — violates S1 (agent survival)", agentPID)
	}

	// Transcript intact: the agent's captured output survived the mid-write kill.
	transcriptContains(t, env.stateDir, local, "MID-WRITE")

	// Restart: a fresh daemon reconnects the SAME session, nothing lost.
	startDaemon(t, env)
	c2 := dial(t, env.sock)
	view2 := waitOneView(t, c2)
	if view2.ID != id {
		t.Fatalf("after restart, listed id %q != %q", view2.ID, id)
	}
	if view2.Status.Process == status.ProcessLost {
		t.Fatal("session marked lost after a mid-write daemon kill + restart — violates S1/L2 zero-loss")
	}
	meta2 := readMeta(t, env.stateDir, local)
	if meta2.ShimPID != meta.ShimPID {
		t.Fatalf("reconnected shim PID %d != original %d — the session was relaunched, not reconnected",
			meta2.ShimPID, meta.ShimPID)
	}

	// Re-attach: the client-painted grid still holds the agent's output, intact.
	a2, err := c2.Attach(id)
	if err != nil {
		t.Fatalf("re-Attach after mid-write daemon kill + restart: %v", err)
	}
	if len(a2.Snapshot()) == 0 {
		t.Fatal("re-attach after restart painted an empty snapshot — grid lost")
	}
	postGrid := gridText(t, a2.Snapshot())
	if !strings.Contains(postGrid, "MID-WRITE") {
		t.Fatalf("re-attach grid lost the agent's output after a mid-write kill; grid was:\n%s", postGrid)
	}
}
