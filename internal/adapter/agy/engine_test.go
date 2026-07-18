package agy

// R-D9 (Phase F follow-up, .claude/tmp/cli-duo-implementation-plan.md): the
// engine-path integration test the plan defers until AFTER Phase C merges —
// mirrors internal/adapter/claude's TestGridHeuristicFallback_ClassifiesIdlePrompt
// (claude_test.go): the adapter's OWN SignalSources(), fed through the real
// engine.RegisterSession + OnOutput (not the low-level evaluateGridWithRules
// internal/engine's own R-C5 fixture replay uses), classify a busy-window frame
// active and the settled-idle frame idle. This proves the wiring end to end —
// package agy's declared descriptors, not a hand-copied rule list, drive the
// engine correctly.
//
// Offsets are the Phase B evidence memo's frozen phase-window bytes
// (docs/verification/cli-duo-adapters-evidence.md): busy window [3802,7261],
// settled-idle frame 7262. snapAtOffset renders at 100x30 — the geometry the
// fixture was recorded at and the memo's offsets are measured against; this
// package's helpers_test.go renderGrid also renders at 100x30 (fixed
// post-committee, R-H4 finding 6 — it previously used 80x24, a stale
// geometry mismatch against the fixture).

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// snapAtOffset feeds capture[:n] (clamped to len(capture)) into a fresh 100x30
// emulator and returns the decoded snapshot — an isolated "offset N" read using
// the fixture's recording geometry (docs/verification/cli-duo-adapters-
// evidence.md), matching internal/engine/fixturereplay_test.go's convention.
func snapAtOffset(t *testing.T, capture []byte, n int) *vt.Snap {
	t.Helper()
	if n > len(capture) {
		n = len(capture)
	}
	emu := vt.NewEmulator(100, 30)
	defer emu.Close()
	emu.Feed(capture[:n])
	b, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap, err := vt.DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return snap
}

// TestEnginePath_BusyAndSettled — R-D9: the real engine, driven by the agy
// adapter's own declared SignalSources, reads a busy-window frame as active and
// the settled-idle frame as idle.
func TestEnginePath_BusyAndSettled(t *testing.T) {
	a := newAdapter()
	fx, err := fixtureio.LoadFixture("testdata/agy.json")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	t.Run("busy_offset_classifies_active", func(t *testing.T) {
		const busyOffset = 5000 // inside the memo's busy window [3802,7261]
		snap := snapAtOffset(t, fx.PTYCapture, busyOffset)

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

	t.Run("settled_offset_classifies_idle", func(t *testing.T) {
		const settledOffset = 7262 // memo's settled-idle frame
		snap := snapAtOffset(t, fx.PTYCapture, settledOffset)

		var got status.Status
		var emitted bool
		eng := engine.New(engine.Config{
			StalenessThreshold: 0,
			Emit: func(_ string, s status.Status) {
				got = s
				emitted = true
			},
		})
		eng.RegisterSession("s1", "tok", 0, a.SignalSources())
		eng.OnOutput("s1", snap)

		if !emitted {
			t.Fatalf("OnOutput at settled offset %d emitted no status change", settledOffset)
		}
		if got.Turn != status.TurnIdle {
			t.Errorf("settled offset %d classified turn=%q; want idle", settledOffset, got.Turn)
		}
	})
}
