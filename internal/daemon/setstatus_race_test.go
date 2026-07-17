package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// TestSetStatus_NoResurrectionAfterExit is the F1 concurrency guard: a late engine
// emit (SetStatus) racing a session's finalization (markLost / handleShimExit) must
// NEVER leave the session showing running after it has exited or been lost.
// SetStatus re-reads the live process dimension inside its write critical section
// (writeMu) and refuses to persist activity for a non-running session, so the
// terminal process state is monotonic under the race — in memory AND on disk. Run
// under -race -count=10.
func TestSetStatus_NoResurrectionAfterExit(t *testing.T) {
	for iter := 0; iter < 30; iter++ {
		d := openDaemon(t, daemonConfig(t))

		const id = "raceX"
		now := time.Now()
		if err := d.saveMeta(persist.Meta{
			ID:           id,
			AgentType:    "fake",
			CreatedAt:    now,
			LastActivity: now,
			Status:       status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
		}); err != nil {
			t.Fatalf("seed running session: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		// Late emits: keep flipping activity as if the engine were still emitting.
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = d.SetStatus(id, status.Status{Turn: status.TurnActive, Interaction: status.InteractionNone})
			}
		}()
		// Finalize concurrently: markLost (no side-file) on even iters, the shim-exit
		// path (also lost, no exit.json) on odd iters — both terminal.
		go func() {
			defer wg.Done()
			if iter%2 == 0 {
				d.markLost(id)
			} else {
				d.handleShimExit(id)
			}
		}()
		wg.Wait()

		// A burst of late emits AFTER finalization must not resurrect it either.
		for i := 0; i < 50; i++ {
			_ = d.SetStatus(id, status.Status{Turn: status.TurnActive, Interaction: status.InteractionNone})
		}

		got, ok := d.Get(id)
		if !ok {
			t.Fatalf("iter %d: session vanished", iter)
		}
		if got.Status.Process == status.ProcessRunning {
			t.Fatalf("iter %d: in-memory status shows running after exit (F1 resurrection)", iter)
		}
		// The persisted meta must agree — no torn disk/memory disagreement.
		disk := scanMetaByID(t, d, id)
		if disk.Status.Process == status.ProcessRunning {
			t.Fatalf("iter %d: persisted meta shows running after exit (F1 resurrection on disk)", iter)
		}
		if disk.Status.Process != got.Status.Process {
			t.Fatalf("iter %d: disk process %q != memory process %q (torn write)", iter, disk.Status.Process, got.Status.Process)
		}
		_ = d.Close()
	}
}

// TestSetStatus_RefusesActivityAfterExit is the deterministic half of the F1
// guard: once a session is terminal, a late engine emit must be REFUSED outright —
// it may not even flip the activity dimensions on a lost/exited row. This pins the
// explicit process guard (distinct from the atomic-RMW that stops Process
// resurrection): a lost session that gets a burst of turn=active emits stays
// exactly lost + unchanged.
func TestSetStatus_RefusesActivityAfterExit(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	const id = "exited1"
	now := time.Now()
	if err := d.saveMeta(persist.Meta{
		ID:           id,
		AgentType:    "fake",
		CreatedAt:    now,
		LastActivity: now,
		Status:       status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
	}); err != nil {
		t.Fatalf("seed running session: %v", err)
	}
	d.markLost(id) // now terminal: {lost, unknown, none}

	before, _ := d.Get(id)
	if before.Status.Process != status.ProcessLost {
		t.Fatalf("precondition: session not lost after markLost: %+v", before.Status)
	}

	// A burst of late activity emits must ALL be refused.
	for i := 0; i < 20; i++ {
		if err := d.SetStatus(id, status.Status{Turn: status.TurnActive, Interaction: status.InteractionPermission}); err != nil {
			t.Fatalf("SetStatus on a lost session returned an error; want a silent no-op: %v", err)
		}
	}

	after, _ := d.Get(id)
	if after.Status != before.Status {
		t.Fatalf("late emit changed a terminal session's status: %+v -> %+v; SetStatus must refuse activity for a non-running session (F1)", before.Status, after.Status)
	}
}

// scanMetaByID reads a session's persisted meta straight off disk via the store.
func scanMetaByID(t *testing.T, d *Daemon, id string) persist.Meta {
	t.Helper()
	metas, err := d.store.Scan()
	if err != nil {
		t.Fatalf("scan store: %v", err)
	}
	for _, m := range metas {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("meta %s not found on disk", id)
	return persist.Meta{}
}
