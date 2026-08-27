package tui

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func orderedSession(id string, entered, created time.Time) protocol.SessionView {
	return protocol.SessionView{
		ID: id, Agent: "codex", Group: status.GroupWorking,
		Status:         status.Status{Process: status.ProcessRunning, Turn: status.TurnActive},
		GroupEnteredAt: entered, CreatedAt: created, LastActivity: entered,
	}
}

func TestGeneral_OrdersEachCategoryByEntryTimeNewestFirst(t *testing.T) {
	base := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	gm := newGeneralModel([]protocol.SessionView{
		orderedSession("ep/old", base, base.Add(-time.Hour)),
		orderedSession("ep/new", base.Add(2*time.Hour), base.Add(-2*time.Hour)),
		orderedSession("ep/middle", base.Add(time.Hour), base.Add(-3*time.Hour)),
	})

	want := []string{"ep/new", "ep/middle", "ep/old"}
	for i, id := range want {
		if gm.sessions[i].ID != id {
			t.Fatalf("order[%d] = %q, want %q (all=%v)", i, gm.sessions[i].ID, id, want)
		}
	}
}

func TestGeneral_CategoryTransitionReordersAndPreservesSelection(t *testing.T) {
	base := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	a := orderedSession("ep/a", base, base.Add(-time.Hour))
	b := orderedSession("ep/b", base.Add(time.Hour), base.Add(-2*time.Hour))
	gm := newGeneralModel([]protocol.SessionView{a, b})
	gm.sel = 1 // a is second after initial newest-first sort

	a.Group = status.GroupNeedsInput
	a.Status.Turn = status.TurnIdle
	a.Status.Interaction = status.InteractionPrompt
	a.GroupEnteredAt = base.Add(3 * time.Hour)
	gm.apply(a)

	selected, ok := gm.selected()
	if !ok || selected.ID != "ep/a" {
		t.Fatalf("selection moved from a after category reorder: %+v, ok=%v", selected, ok)
	}
	if first, _ := gm.selected(); first.Group != status.GroupNeedsInput {
		t.Fatalf("transitioned session did not move into Needs input: %+v", first)
	}
}

func TestGeneral_OrderFallbackAndDeterministicTies(t *testing.T) {
	base := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	legacyOld := orderedSession("ep/legacy-old", time.Time{}, base.Add(-time.Hour))
	legacyOld.LastActivity = base
	legacyNew := orderedSession("ep/legacy-new", time.Time{}, base.Add(-2*time.Hour))
	legacyNew.LastActivity = base.Add(time.Hour)
	tieB := orderedSession("ep/b", base.Add(2*time.Hour), base)
	tieA := orderedSession("ep/a", base.Add(2*time.Hour), base)

	gm := newGeneralModel([]protocol.SessionView{legacyOld, tieB, legacyNew, tieA})
	want := []string{"ep/a", "ep/b", "ep/legacy-new", "ep/legacy-old"}
	for i, id := range want {
		if gm.sessions[i].ID != id {
			t.Fatalf("order[%d] = %q, want %q", i, gm.sessions[i].ID, id)
		}
	}
}
