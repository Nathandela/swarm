package daemon

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// v0.5 rename — the daemon updates a session's display label through the SINGLE
// meta writer (G6): the change is durable (persisted to disk), observable in the
// in-memory registry, and fires onMetaSave so a roster event fans out to clients.
// Modeled on SetConversationID (a simple meta-field RMW under writeMu).

func seedRunning(t *testing.T, d *Daemon, id, name string) {
	t.Helper()
	now := time.Now()
	if err := d.saveMeta(persist.Meta{
		ID:           id,
		AgentType:    "claude",
		Name:         name,
		CreatedAt:    now,
		LastActivity: now,
		Status:       status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

func TestRename_PersistsAndObserves(t *testing.T) {
	var observed []persist.Meta
	cfg := daemonConfig(t)
	cfg.onMetaSave = func(m persist.Meta) { observed = append(observed, m) }
	d := openDaemon(t, cfg)

	const id = "s1"
	seedRunning(t, d, id, "")
	observed = nil // drop the seed's own onMetaSave

	if err := d.Rename(id, "backend-refactor"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// In-memory registry reflects the new label.
	if got, ok := d.Get(id); !ok || got.Name != "backend-refactor" {
		t.Fatalf("in-memory name = %q (ok=%v), want backend-refactor", got.Name, ok)
	}
	// Persisted to disk (durable across a scan).
	if disk := scanMetaByID(t, d, id); disk.Name != "backend-refactor" {
		t.Fatalf("persisted name = %q, want backend-refactor", disk.Name)
	}
	// onMetaSave fired exactly once, carrying the new name — the roster-event signal.
	if len(observed) != 1 || observed[0].Name != "backend-refactor" {
		t.Fatalf("onMetaSave observations = %+v, want one carrying backend-refactor", observed)
	}
}

// Renaming an unknown session is an error (no silent write).
func TestRename_UnknownSessionErrors(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	if err := d.Rename("nope", "x"); err == nil {
		t.Fatalf("Rename of an unknown session must error")
	}
}

// Renaming to the current name is a no-op: no redundant persist / observer fire.
func TestRename_SameNameIsNoOp(t *testing.T) {
	var observed int
	cfg := daemonConfig(t)
	cfg.onMetaSave = func(persist.Meta) { observed++ }
	d := openDaemon(t, cfg)

	const id = "s1"
	seedRunning(t, d, id, "keep")
	observed = 0

	if err := d.Rename(id, "keep"); err != nil {
		t.Fatalf("Rename same name: %v", err)
	}
	if observed != 0 {
		t.Fatalf("renaming to the same name fired onMetaSave %d times, want 0 (no-op)", observed)
	}
}
