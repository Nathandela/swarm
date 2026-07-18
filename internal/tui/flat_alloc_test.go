package tui

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// R4.1.2 — apply() must not build a fresh full-board []protocol.SessionView copy
// (via flat()) once for the pre-mutation selectedID() lookup and again for the
// post-mutation restoreSel() search. Both now walk sessions in flat order
// in-place (selected/restoreSel), so a same-group status update — no reorder, no
// banner, no session insert — allocates nothing. Failing-first: before the
// rework, this measured 2.0 allocs/run (one per flat() call).
func TestApply_NoFlatAllocOnSameGroupUpdate(t *testing.T) {
	sessions := []protocol.SessionView{
		sNeedsInput("endpoint/s1", "claude", "~/Code/quanthome-api", "Permission: run db migration?", 12*time.Minute),
		sWorking("endpoint/s2", "codex", "~/Code/agents-tracker", "Writing adapter fixture tests", 3*time.Minute),
		sReview("endpoint/s3", "claude", "~/Code/mcp-soml", "Turn finished, review the diff", 1*time.Hour),
		sCompleted("endpoint/s4", "gemini", "~/Code/scratch", "exit 0", 2*time.Hour),
	}
	updated := sWorking("endpoint/s2", "codex", "~/Code/agents-tracker", "still building", 2*time.Minute)

	gm := newGeneralModel(append([]protocol.SessionView(nil), sessions...))
	gm.sel = 1
	orig := gm.sessions[1]

	allocs := testing.AllocsPerRun(200, func() {
		gm.apply(updated)
		gm.sessions[1] = orig
		gm.sel = 1
	})
	if allocs > 0 {
		t.Fatalf("apply() on a same-group update allocated %.1f times per run, want 0 (flat() churn not eliminated)", allocs)
	}
}

// R4.1.2 companion — selection-by-identity across a reorder (the behavior
// restoreSel exists for) must still work after the walk-in-place rework; this
// duplicates the intent of TestSelection_FollowsIdentityAcrossReorder
// (identity_test.go) at the generalModel level, directly on apply()/selected().
func TestApply_PreservesSelectionByIdentityAcrossReorder(t *testing.T) {
	gm := newGeneralModel([]protocol.SessionView{
		sWorking("endpoint/A", "codex", "~/Code/a", "AAA", time.Minute),
		sWorking("endpoint/B", "claude", "~/Code/b", "BBB", time.Minute),
	})
	gm.sel = 1 // B selected

	gm.apply(sNeedsInput("endpoint/Z", "gemini", "~/Code/z", "ZZZ", 0)) // sorts before both Working rows

	s, ok := gm.selected()
	if !ok || s.ID != "endpoint/B" {
		t.Fatalf("selection must stay on B by identity across the reorder, got %+v ok=%v", s, ok)
	}
}
