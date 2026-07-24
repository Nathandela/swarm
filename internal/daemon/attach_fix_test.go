package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
)

// F6 (audit-006) — DialSession re-verifies the recorded shim identity (PID,
// start-time) BEFORE dialing, exactly as Kill/Delete do. On a mismatch (the shim
// exited or its PID was reused) it must refuse rather than dial a rebound socket.
func TestDialSession_IdentityMismatch_NoDial(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	id := "dialmismatch1"
	if err := os.MkdirAll(filepath.Join(cfg.StateDir, id), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	// A rebound listener sits at the session socket; if DialSession wrongly dialed,
	// it would register a connection.
	fs := startFakeShim(t, shimSocketPath(cfg.StateDir, id), shimwire.Version)

	// A live PID stands in for the recorded shim, but with a WRONG start-time so the
	// identity recheck fails.
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

	conn, _, err := d.DialSession(id)
	if err == nil {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatalf("DialSession dialed despite an identity mismatch; want an error (S3/F6)")
	}
	// It must not have contacted the rebound socket at all.
	time.Sleep(400 * time.Millisecond)
	if fs.connCount() != 0 {
		t.Fatalf("DialSession contacted the rebound socket on identity mismatch (%d conns); want 0 (F6)", fs.connCount())
	}
}
