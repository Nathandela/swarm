package daemon

import (
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestSetTagPersistsAndPublishes(t *testing.T) {
	var observed persist.Meta
	cfg := daemonConfig(t)
	cfg.onMetaSave = func(m persist.Meta) { observed = m }
	d := openDaemon(t, cfg)
	if err := d.saveMeta(persist.Meta{ID: "s1", AgentType: "codex"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := d.SetTag("s1", "backend"); err != nil {
		t.Fatalf("SetTag: %v", err)
	}
	got, ok := d.Get("s1")
	if !ok || got.Tag != "backend" || observed.Tag != "backend" {
		t.Fatalf("tagged meta got=%+v observed=%+v", got, observed)
	}
	if disk, err := d.store.Load("s1"); err != nil || disk.Tag != "backend" {
		t.Fatalf("persisted tag = %q, err=%v", disk.Tag, err)
	}
}
