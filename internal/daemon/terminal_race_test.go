package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// TestFinalize_ExitedWinsOverLost is the S1 terminal-writer race (B4): markLost
// (e.g. a Kill identity-mismatch) and handleShimExit (the monitor) race to finalize
// the SAME session while an authoritative exit.json is present. The exit side-file's
// exited+code must ALWAYS win — a late lost may never clobber it, in either order.
// Run under -race -count=5.
func TestFinalize_ExitedWinsOverLost(t *testing.T) {
	for iter := 0; iter < 40; iter++ {
		d := openDaemon(t, daemonConfig(t))
		const id = "term1"
		now := time.Now()
		if err := d.saveMeta(persist.Meta{
			ID: id, AgentType: "fake", CreatedAt: now, LastActivity: now,
			Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
		}); err != nil {
			t.Fatalf("seed running: %v", err)
		}
		// The authoritative exit side-file: exited, code 7.
		ei, _ := json.Marshal(shim.ExitInfo{ExitCode: 7, FinishedAt: now})
		if err := os.WriteFile(filepath.Join(d.sessionDir(id), shim.ExitFile), ei, 0o600); err != nil {
			t.Fatalf("write exit.json: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); d.markLost(id) }()
		go func() { defer wg.Done(); d.handleShimExit(id) }()
		wg.Wait()

		got, ok := d.Get(id)
		if !ok {
			t.Fatalf("iter %d: session vanished", iter)
		}
		if got.Status.Process != status.ProcessExited {
			t.Fatalf("iter %d: in-memory process = %q; want exited (a racing markLost clobbered the exit report, S1)", iter, got.Status.Process)
		}
		if got.ExitCode == nil || *got.ExitCode != 7 {
			t.Fatalf("iter %d: exit code = %v; want 7 (the exit side-file classification must win)", iter, got.ExitCode)
		}
		disk := scanMetaByID(t, d, id)
		if disk.Status.Process != status.ProcessExited || disk.ExitCode == nil || *disk.ExitCode != 7 {
			t.Fatalf("iter %d: persisted meta = {%q, %v}; want exited+7", iter, disk.Status.Process, disk.ExitCode)
		}
		_ = d.Close()
	}
}
