package opencode

// R-E9 (Phase F follow-up, .claude/tmp/cli-duo-implementation-plan.md): the
// engine-path integration test the plan defers until AFTER Phase C merges —
// mirrors internal/adapter/claude's TestGridHeuristicFallback_ClassifiesIdlePrompt
// (claude_test.go) and this repo's agy R-D9 counterpart: the adapter's OWN
// SignalSources(), fed through the real engine.RegisterSession + OnOutput,
// classify a busy-window frame active. opencode declares no idle rule (R-E4:
// the R-B4 memo could not jointly satisfy a stable idle substring), so the
// settled frame must classify unknown and NEVER active or idle — the honest
// T-4 outcome, not a guessed-wrong idle.
//
// Offsets are the Phase B evidence memo's frozen phase-window bytes
// (docs/verification/cli-duo-adapters-evidence.md): busy window
// [33547,67787], settled candidate 68087. renderGrid (helpers_test.go) already
// uses the fixture's recording geometry (100x30), so this file reuses it on a
// sliced capture prefix rather than adding a duplicate helper.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/status"
)

// TestEnginePath_BusyAndSettled — R-E9: the real engine, driven by the
// opencode adapter's own declared SignalSources, reads a busy-window frame as
// active and the settled candidate frame as unknown (never active or idle).
func TestEnginePath_BusyAndSettled(t *testing.T) {
	a := newAdapter()
	fx, err := fixtureio.LoadFixture("testdata/opencode.json")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	t.Run("busy_offset_classifies_active", func(t *testing.T) {
		const busyOffset = 50000 // inside the memo's busy window [33547,67787]
		snap := renderGrid(t, fx.PTYCapture[:busyOffset])

		var got status.Status
		var emitted bool
		eng := engine.New(engine.Config{
			StalenessThreshold: 0, // no typed-signal freshness window: heuristic always applies
			Emit: func(_ string, s status.Status) {
				got = s
				emitted = true
			},
		})
		eng.RegisterSession("s1", "tok", 0, a.SignalSources())
		eng.OnOutput("s1", snap)

		if !emitted {
			t.Fatalf("OnOutput at busy offset %d emitted no status change", busyOffset)
		}
		if got.Turn != status.TurnActive {
			t.Errorf("busy offset %d classified turn=%q; want active", busyOffset, got.Turn)
		}
	})

	t.Run("settled_offset_preserves_status_never_idle", func(t *testing.T) {
		const settledOffset = 68087 // memo's settled candidate frame
		snap := renderGrid(t, fx.PTYCapture[:settledOffset])

		// Seed the session as if it were still busy right before settling. The
		// settled frame carries no busy marker and opencode declares no idle rule
		// (R-E4), so the read is INCONCLUSIVE — and under ADR-007 (merged from
		// main after this epic's committee pass) an inconclusive grid tap
		// PRESERVES the committed status instead of committing unknown. The
		// safety property is unchanged: the settled frame must never emit idle
		// (or anything else); the stale active is later downgraded to unknown by
		// Tick's staleness guard, not by this tap.
		initial := status.Status{
			Process:     status.ProcessRunning,
			Turn:        status.TurnActive,
			Interaction: status.InteractionNone,
		}

		var got status.Status
		var emitted bool
		eng := engine.New(engine.Config{
			StalenessThreshold: 0,
			Emit: func(_ string, s status.Status) {
				got = s
				emitted = true
			},
		})
		eng.RegisterSession("s1", "tok", 0, a.SignalSources(), initial)
		eng.OnOutput("s1", snap)

		if emitted {
			t.Fatalf("OnOutput at settled offset %d emitted turn=%q; want no emit (inconclusive read preserves the committed status, ADR-007)", settledOffset, got.Turn)
		}
	})
}
