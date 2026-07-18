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
