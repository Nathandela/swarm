package skeleton

// Item 1.2 (agents-tracker-mlm), R1.2.6 — a grid-tap attach/snapshot failure must be
// OBSERVABLE, not a silent heuristic death. sampleGrid increments a counter (and
// rate-limit-logs) on every failure; here the real sampleGrid is driven against a
// core with no such session, so the attach fails and the counter advances.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
)

func TestTap_SnapshotFailureObservable(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskobs") // short path: the daemon binds a UDS here
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	core, err := daemon.Open(daemon.Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "d.sock"),
		LockPath:    filepath.Join(dir, "d.lock"),
		LogPath:     filepath.Join(dir, "d.log"),
		MaxSessions: 4,
	})
	if err != nil {
		t.Fatalf("daemon.Open: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	d := &Daemon{core: core, stateDir: dir, sampling: make(map[string]struct{})}
	d.api = newCoreAPI(core, "", endpointID(dir))

	// The real sampleGrid path: an attach to a non-existent session fails, which must
	// be counted (this is exactly how the pre-1.2 oversized-snapshot bug manifested —
	// a failed attach that was silently skipped).
	d.sampleGrid("no-such-session")
	if got := d.tapFailures.Load(); got != 1 {
		t.Fatalf("tap failure counter = %d after one failed sample, want 1 (R1.2.6 observability)", got)
	}
	// A second, independent failure advances the counter again (observable, not one-shot).
	d.sampleGrid("still-missing")
	if got := d.tapFailures.Load(); got != 2 {
		t.Fatalf("tap failure counter = %d after two failed samples, want 2", got)
	}
}
