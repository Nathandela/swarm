package daemon

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestGroupEnteredAt_ChangesOnlyOnDerivedGroupTransition(t *testing.T) {
	transitionAt := time.Date(2026, 8, 27, 15, 0, 0, 0, time.UTC)
	var observed persist.Meta
	cfg := daemonConfig(t)
	cfg.groupNow = func() time.Time { return transitionAt }
	cfg.onMetaSave = func(m persist.Meta) { observed = m }
	d := openDaemon(t, cfg)

	created := transitionAt.Add(-time.Hour)
	if err := d.saveMeta(persist.Meta{
		ID: "s1", AgentType: "codex", CreatedAt: created, LastActivity: created,
		Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got, _ := d.Get("s1"); !got.GroupEnteredAt.Equal(created) {
		t.Fatalf("fresh session entered-at = %v, want creation/activity %v", got.GroupEnteredAt, created)
	}

	// Unknown -> active remains Working, so even a status write preserves the instant.
	if err := d.SetStatus("s1", status.Status{Turn: status.TurnActive, Interaction: status.InteractionNone}); err != nil {
		t.Fatalf("same-group SetStatus: %v", err)
	}
	if got, _ := d.Get("s1"); !got.GroupEnteredAt.Equal(created) {
		t.Fatalf("same-group status update changed entered-at to %v", got.GroupEnteredAt)
	}

	// Active -> idle/none crosses Working -> Ready for review.
	if err := d.SetStatus("s1", status.Status{Turn: status.TurnIdle, Interaction: status.InteractionNone}); err != nil {
		t.Fatalf("transition SetStatus: %v", err)
	}
	got, _ := d.Get("s1")
	if !got.GroupEnteredAt.Equal(transitionAt) {
		t.Fatalf("group transition entered-at = %v, want %v", got.GroupEnteredAt, transitionAt)
	}
	if !observed.GroupEnteredAt.Equal(transitionAt) {
		t.Fatalf("observer received stale entered-at %v, want %v", observed.GroupEnteredAt, transitionAt)
	}
	if disk := scanMetaByID(t, d, "s1"); !disk.GroupEnteredAt.Equal(transitionAt) {
		t.Fatalf("persisted entered-at = %v, want %v", disk.GroupEnteredAt, transitionAt)
	}

	transitionAt = transitionAt.Add(time.Hour)
	if err := d.Rename("s1", "new name"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got, _ := d.Get("s1"); !got.GroupEnteredAt.Equal(transitionAt.Add(-time.Hour)) {
		t.Fatalf("rename changed entered-at to %v", got.GroupEnteredAt)
	}

	d.finalizeTerminal("s1", func(cur persist.Meta) persist.Meta {
		cur.Status.Process = status.ProcessExited
		return cur
	})
	if got, _ := d.Get("s1"); !got.GroupEnteredAt.Equal(transitionAt) {
		t.Fatalf("terminal group transition entered-at = %v, want %v", got.GroupEnteredAt, transitionAt)
	}
}

func TestGroupEnteredAt_SurvivesDaemonRestart(t *testing.T) {
	cfg := daemonConfig(t)
	d, err := Open(cfg)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	entered := time.Date(2026, 8, 27, 17, 0, 0, 0, time.UTC)
	if err := d.saveMeta(persist.Meta{
		ID: "done", AgentType: "claude", CreatedAt: entered.Add(-time.Hour),
		LastActivity: entered, GroupEnteredAt: entered,
		Status: status.Status{Process: status.ProcessExited, Turn: status.TurnIdle, Interaction: status.InteractionNone},
	}); err != nil {
		_ = d.Close()
		t.Fatalf("save before restart: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}

	restarted, err := Open(cfg)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	got, ok := restarted.Get("done")
	if !ok || !got.GroupEnteredAt.Equal(entered) {
		t.Fatalf("after restart entered-at = %v (ok=%v), want %v", got.GroupEnteredAt, ok, entered)
	}
}
