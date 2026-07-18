package daemon

// Item 1.3 (agents-tracker-445) — Kill/Delete reachability. Delete must verify
// the shim is dead BEFORE removing a session's directory, so a live agent is
// never orphaned by a removed dir (R1.3.4), and must fall back to signalling the
// shim's process group directly when its socket is unreachable (R1.3.5).

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

// holdShimAttach dials a session's shim socket, completes the hello handshake and
// attaches, then holds the connection open — occupying a shim serve slot the way a
// live controller does. The connection is closed on test cleanup.
func holdShimAttach(t *testing.T, stateDir, id string) net.Conn {
	t.Helper()
	sock := shimSocketPath(stateDir, id)
	conn, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		t.Fatalf("dial shim socket %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	writeShimControl(t, conn, shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	if got := readShimControlType(t, conn); got != shimwire.TypeHello {
		t.Fatalf("shim hello reply type = %q, want hello", got)
	}
	writeShimControl(t, conn, shimwire.Control{Type: shimwire.TypeAttach})
	// Read frames until the attach snapshot arrives, confirming the attach is
	// established (the shim is now holding this connection's serve slot).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		typ, _, rerr := wire.ReadFrame(conn)
		if rerr != nil {
			t.Fatalf("read shim frame while attaching: %v", rerr)
		}
		if typ == wire.TSnapshot {
			_ = conn.SetDeadline(time.Time{})
			return conn
		}
	}
	t.Fatalf("attach snapshot not received within 3s")
	return nil
}

func writeShimControl(t *testing.T, conn net.Conn, ctrl shimwire.Control) {
	t.Helper()
	b, err := shimwire.Encode(ctrl)
	if err != nil {
		t.Fatalf("encode shim control: %v", err)
	}
	if err := wire.WriteFrame(conn, wire.TControl, b); err != nil {
		t.Fatalf("write shim control frame: %v", err)
	}
}

func readShimControlType(t *testing.T, conn net.Conn) string {
	t.Helper()
	typ, payload, err := wire.ReadFrame(conn)
	if err != nil {
		t.Fatalf("read shim control frame: %v", err)
	}
	if typ != wire.TControl {
		t.Fatalf("shim frame type = %d, want control", typ)
	}
	ctrl, err := shimwire.Decode(payload)
	if err != nil {
		t.Fatalf("decode shim control: %v", err)
	}
	return ctrl.Type
}

// T1.3.b / R1.3.1 — deleting a session while a controller holds an attach must
// still reach the shim, terminate the agent, and remove the dir. With the shim
// serving one connection at a time this timed out and orphaned the agent; with
// concurrent serving the fresh signal connection is answered promptly.
func TestDelete_WithAttachedControllerReachesShim(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m, agentPID := launchAnnounce(t, d)
	sessionDir := filepath.Join(cfg.StateDir, m.ID)

	holdShimAttach(t, cfg.StateDir, m.ID) // a live controller holds a serve slot

	done := make(chan error, 1)
	go func() { done <- d.Delete(m.ID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Delete with an attached controller: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Delete did not return within 10s while a controller was attached (blocked-signal defect)")
	}

	waitProcessGone(t, agentPID, 5*time.Second)
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("session dir still present after Delete: stat err = %v", err)
	}
	if _, ok := d.Get(m.ID); ok {
		t.Fatalf("session %s still in registry after Delete", m.ID)
	}
}

// R1.3.5 — when the shim socket is unreachable but the shim is alive and its
// identity still matches, Delete falls back to signalling the shim's process
// group directly (its armed handler contains the agent), then confirms death
// before removing the dir. Pre-fix Delete ignored the failed socket signal and
// removed the dir anyway, orphaning the live agent.
func TestDelete_ProcessGroupFallbackContainsAgent(t *testing.T) {
	prev := deleteWait
	deleteWait = 2 * time.Second // keep the pre-fix orphan wait bounded
	t.Cleanup(func() { deleteWait = prev })

	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m, agentPID := launchAnnounce(t, d)
	sessionDir := filepath.Join(cfg.StateDir, m.ID)

	// Make the socket unreachable while the shim + agent stay alive and the
	// recorded identity still matches: the socket-signal path fails, forcing the
	// direct process-group fallback.
	if err := os.Remove(shimSocketPath(cfg.StateDir, m.ID)); err != nil {
		t.Fatalf("remove shim socket: %v", err)
	}

	if err := d.Delete(m.ID); err != nil {
		t.Fatalf("Delete via process-group fallback: %v", err)
	}
	waitProcessGone(t, agentPID, 5*time.Second) // fallback contained the agent
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("session dir still present after Delete: stat err = %v", err)
	}
}

// T1.3.g / R1.3.4 — when a running session's shim cannot be confirmed dead,
// Delete must ERROR and mutate nothing: the session directory, registry entry,
// and live agent all remain, rather than the dir being removed under a live agent.
func TestDelete_UnterminableShimMutatesNothing(t *testing.T) {
	orig := terminateForDeleteFn
	terminateForDeleteFn = func(*Daemon, string, persist.Meta) bool { return false }
	t.Cleanup(func() { terminateForDeleteFn = orig })

	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m, agentPID := launchAnnounce(t, d)
	sessionDir := filepath.Join(cfg.StateDir, m.ID)

	err := d.Delete(m.ID)
	if err == nil {
		t.Fatal("Delete succeeded despite an unconfirmed shim death; want an error (R1.3.4)")
	}
	if _, statErr := os.Stat(sessionDir); statErr != nil {
		t.Fatalf("session dir removed after a failed termination: %v (R1.3.4 mutate-nothing)", statErr)
	}
	if _, ok := d.Get(m.ID); !ok {
		t.Fatal("session removed from registry after a failed termination (R1.3.4 mutate-nothing)")
	}
	if !processAlive(agentPID) {
		t.Fatal("agent killed on the mutate-nothing path")
	}
}

// T1.3.g / R1.3.4 — a PID/start-time identity mismatch (the recorded shim's PID
// was reused, or the shim exited) makes Delete send NO signal to that PID: the
// pre-signal identity recheck (F6/S3) applies to Delete's termination step exactly
// as it does to Kill's, not just when the socket happens to be unreachable. A fake
// shim bound at the session socket proves it was never contacted; a live process
// standing in for the reused PID proves it was never signalled either.
func TestDelete_IdentityMismatch_NoSignal(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const id = "deletemismatch1"
	sessionDir := filepath.Join(cfg.StateDir, id)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	// A rebound listener sits at the session socket; if Delete wrongly dialed it,
	// it would register a connection / signal.
	fs := startFakeShim(t, shimSocketPath(cfg.StateDir, id), shimwire.Version)

	// A live PID stands in for the recorded shim, but with a WRONG start-time so
	// the identity recheck fails (PID reuse).
	pid := spawnCatchTermChild(t, filepath.Join(t.TempDir(), "sig"))
	realStart, err := processStartTime(pid)
	if err != nil {
		t.Fatalf("processStartTime: %v", err)
	}
	d.putMem(persist.Meta{
		ID:            id,
		AgentType:     "fake",
		Cwd:           "/tmp",
		CreatedAt:     time.Now(),
		LastActivity:  time.Now(),
		Status:        status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
		ShimPID:       pid,
		ShimStartTime: realStart + 1, // deliberately wrong
	})

	if err := d.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if fs.connCount() != 0 {
		t.Fatalf("Delete dialed the shim socket on an identity mismatch (%d conns); want zero (S3)", fs.connCount())
	}
	if fs.signalled() {
		t.Fatalf("Delete delivered a signal to a rebound socket on an identity mismatch; want none (S3)")
	}
	if !processAlive(pid) {
		t.Fatal("Delete killed the reused-PID stand-in process; want it left untouched (S3)")
	}
	if _, ok := d.Get(id); ok {
		t.Fatal("session still present after Delete")
	}
	if _, statErr := os.Stat(sessionDir); !os.IsNotExist(statErr) {
		t.Fatalf("session dir still present after Delete: stat err = %v", statErr)
	}
}

// T1.3.g — Delete racing a natural shim exit (the monitor's own finalize path,
// reconcile.go handleShimExit, firing concurrently with Delete) must not panic,
// must leave the session cleanly gone, and must not resurrect the meta: once
// Delete's tombstone lands, a racing finalize is a no-op rather than a second
// write. d.writeMu is used directly (test is in-package) to force handleShimExit
// to attempt its write only after Delete's registry removal + tombstone have
// already landed (both happen before Delete's own writeMu-guarded store.Delete),
// which is the interleaving where a resurrection bug would show up.
func TestDelete_RacesNaturalShimExit(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const id = "race-natural-exit"
	sessionDir := filepath.Join(cfg.StateDir, id)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	d.mu.Lock()
	d.sessions[id] = &session{
		meta: persist.Meta{ID: id, Status: status.Status{Process: status.ProcessRunning}},
		stop: make(chan struct{}),
	}
	d.mu.Unlock()

	// Identity mismatch stand-in: nothing to signal, Phase 1 passes trivially, so
	// the only interesting race is Phase 2 (registry removal + tombstone) vs.
	// handleShimExit's finalize.
	orig := terminateForDeleteFn
	terminateForDeleteFn = func(*Daemon, string, persist.Meta) bool { return true }
	t.Cleanup(func() { terminateForDeleteFn = orig })

	d.writeMu.Lock() // hold the write choke point so Delete blocks AFTER Phase 2

	deleteDone := make(chan struct{})
	go func() {
		defer close(deleteDone)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Delete panicked racing a natural shim exit: %v", r)
			}
		}()
		_ = d.Delete(id)
	}()

	// Wait for Delete's Phase 2 (tombstone) to land — it happens before Delete's
	// writeMu.Lock() attempt, which is currently blocked by the lock held above.
	deadline := time.Now().Add(3 * time.Second)
	for !d.isDeleted(id) {
		if time.Now().After(deadline) {
			d.writeMu.Unlock()
			t.Fatal("Delete's tombstone never landed within 3s")
		}
		time.Sleep(time.Millisecond)
	}

	exitDone := make(chan struct{})
	go func() {
		defer close(exitDone)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("handleShimExit panicked racing Delete: %v", r)
			}
		}()
		d.handleShimExit(id) // the monitor's natural-exit path (reconcile.go), blocks on writeMu too
	}()

	d.writeMu.Unlock() // release both to contend for writeMu in whatever order
	<-deleteDone
	<-exitDone

	if _, ok := d.Get(id); ok {
		t.Fatal("session resurrected in the registry after Delete raced a natural shim exit")
	}
	if _, statErr := os.Stat(sessionDir); !os.IsNotExist(statErr) {
		t.Fatalf("session dir resurrected after Delete raced a natural shim exit: stat err = %v", statErr)
	}
}

// F1 (review regression) — two concurrent Deletes of the SAME id must not double-
// close session.stop. The transactional Delete splits the read (Phase 1) from the
// registry removal + close (Phase 2), so both goroutines capture the session
// present and both would reach close(session.stop) -> "close of closed channel"
// panic. Only the goroutine that actually removes the session may close its stop.
func TestDelete_ConcurrentSameIDClosesStopOnce(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const id = "concurrent-del"
	if err := os.MkdirAll(filepath.Join(cfg.StateDir, id), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	d.mu.Lock()
	d.sessions[id] = &session{
		meta: persist.Meta{ID: id, Status: status.Status{Process: status.ProcessRunning}},
		stop: make(chan struct{}),
	}
	d.mu.Unlock()

	// Hold both Deletes in the termination step (both already past Phase 1, both
	// holding the same session) until released together, so they race into Phase 2.
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	orig := terminateForDeleteFn
	terminateForDeleteFn = func(*Daemon, string, persist.Meta) bool {
		started <- struct{}{}
		<-release
		return true
	}
	t.Cleanup(func() { terminateForDeleteFn = orig })

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = d.Delete(id) }()
	}
	<-started
	<-started      // both are past Phase 1, both hold the same session pointer
	close(release) // release both into Phase 2 concurrently

	wg.Wait() // pre-fix: a double close of session.stop panics and crashes the run

	if _, ok := d.Get(id); ok {
		t.Fatal("session still present after concurrent Delete")
	}
}

// Sibling of TestDelete_ConcurrentSameIDClosesStopOnce (reviewer optional
// hardening, agents-tracker-445): PreDelete must be winner-gated the same way
// close(sess.stop) is. Pre-fix, PreDelete ran on both concurrent Deletes of the
// SAME id because it was gated on Phase 1's stale `ok` (true for both racers)
// instead of Phase 2's `present` winner check — a worktree-teardown hook would
// fire twice for one deleted session.
func TestDelete_ConcurrentSameIDRunsPreDeleteOnce(t *testing.T) {
	cfg := daemonConfig(t)
	var preDeleteCount atomic.Int32
	cfg.PreDelete = func(persist.Meta) error {
		preDeleteCount.Add(1)
		return nil
	}
	d := openDaemon(t, cfg)

	const id = "concurrent-del-predelete"
	if err := os.MkdirAll(filepath.Join(cfg.StateDir, id), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	d.mu.Lock()
	d.sessions[id] = &session{
		meta: persist.Meta{ID: id, Status: status.Status{Process: status.ProcessRunning}},
		stop: make(chan struct{}),
	}
	d.mu.Unlock()

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	orig := terminateForDeleteFn
	terminateForDeleteFn = func(*Daemon, string, persist.Meta) bool {
		started <- struct{}{}
		<-release
		return true
	}
	t.Cleanup(func() { terminateForDeleteFn = orig })

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = d.Delete(id) }()
	}
	<-started
	<-started      // both are past Phase 1, both hold the same session pointer
	close(release) // release both into Phase 2 concurrently

	wg.Wait()

	if got := preDeleteCount.Load(); got != 1 {
		t.Fatalf("PreDelete ran %d times on concurrent Delete of the same id; want exactly 1 (winner-gated)", got)
	}
}
