package daemon

import (
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

func TestOpenCodeAttentionPersistsWithoutInventingIdle(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	m := openCodeMeta()
	m.Status = status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone}
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	next := status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionPermission, Attention: true}
	if err := d.SetOpenCodeStatus(m.ID, m.OpenCodeBinding.Generation, next); err != nil {
		t.Fatal(err)
	}
	got, _ := d.Get(m.ID)
	if !got.Status.Attention || got.Status.Turn != status.TurnActive || status.Derive(got.Status) != status.GroupNeedsInput {
		t.Fatalf("native attention lost in sole writer: %+v", got.Status)
	}
	next.Attention = false
	next.Interaction = status.InteractionNone
	if err := d.SetOpenCodeStatus(m.ID, m.OpenCodeBinding.Generation, next); err != nil {
		t.Fatal(err)
	}
	got, _ = d.Get(m.ID)
	if got.Status.Attention || status.Derive(got.Status) != status.GroupWorking {
		t.Fatal("native resolution did not restore working state")
	}
}
