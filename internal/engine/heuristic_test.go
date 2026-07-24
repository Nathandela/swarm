package engine

// E10.8 (T-3/T-4): the grid-heuristic evaluator mechanism. OnOutput evaluates
// the emulated screen; a low-frequency Tick poll re-evaluates; there is no
// busy-poll; an inconclusive read preserves the committed turn (ADR-007).
//
// PIN: the fixtures here are CLI-AGNOSTIC — they exercise the engine MECHANISM
// (evaluate-on-output, re-evaluate-on-poll, precedence, staleness, preserve on
// inconclusive), not any Claude/Codex-specific rule. Per-CLI grid rules are the
// per-adapter signatures (ADR-007). The generic patterns the engine recognizes:
//   - a settled trailing prompt sentinel with a parked cursor -> idle/none
//   - a trailing braille/ASCII spinner glyph -> active
//   - anything else (prose, blank) -> inconclusive; the committed turn is
//     preserved, never a confident guess (ADR-007)

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// idlePromptSnap: a settled prompt line ("> ") with the cursor parked after it
// and no animation -> a conclusive idle/none reading.
func idlePromptSnap() *vt.Snap {
	return snapFromLines(20, 2, 2, true, []string{
		"agent ready",
		"",
		"> ",
	})
}

// spinnerSnap: a trailing braille spinner glyph, the near-universal "working"
// indicator -> a conclusive active reading.
func spinnerSnap() *vt.Snap {
	return snapFromLines(20, 9, 2, false, []string{
		"agent ready",
		"",
		"⠋ Working",
	})
}

// ambiguousSnap: prose output with neither a prompt sentinel nor an animation ->
// inconclusive.
func ambiguousSnap() *vt.Snap {
	return snapFromLines(20, 5, 1, true, []string{
		"lorem ipsum dolor",
		"sit amet conse",
	})
}

// blankSnap: an empty grid -> nothing to read.
func blankSnap() *vt.Snap {
	return snapFromLines(20, 0, 0, true, []string{"", "", ""})
}

// E10.8: OnOutput derives a conclusive (turn, interaction) from the grid.
func TestHeuristicConclusiveGrids(t *testing.T) {
	cases := []struct {
		name  string
		snap  *vt.Snap
		turn  status.Turn
		inter status.Interaction
	}{
		{"idle-prompt", idlePromptSnap(), status.TurnIdle, status.InteractionNone},
		{"spinner", spinnerSnap(), status.TurnActive, status.InteractionNone},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			rec := &emitRecorder{}
			e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
			e.RegisterSession("s1", "tok1", 1, kinds("heuristic"))

			e.OnOutput("s1", tc.snap)
			got, ok := rec.last()
			if !ok {
				t.Fatalf("%s grid emitted no status change from the unknown baseline", tc.name)
			}
			if got.s.Turn != tc.turn || got.s.Interaction != tc.inter {
				t.Fatalf("%s grid -> (turn=%s, interaction=%s), want (%s, %s)",
					tc.name, got.s.Turn, got.s.Interaction, tc.turn, tc.inter)
			}
		})
	}
}

// E10.8 (T-4, ADR-007): an inconclusive grid read is absence of evidence, so it
// PRESERVES the committed turn rather than downgrading it to unknown. Drive a
// definite turn first, then feed an inconclusive grid and assert the turn is held
// — repeatably, with no flip-flop and no spurious emit. (This supersedes the
// former apply-unknown behavior; see ADR-007 and the field-test fixtures in
// v05_status_accuracy_test.go.)
func TestHeuristicInconclusivePreservesCommittedTurn(t *testing.T) {
	cases := []struct {
		name string
		snap *vt.Snap
	}{
		{"ambiguous", ambiguousSnap()},
		{"blank", blankSnap()},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			rec := &emitRecorder{}
			e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
			e.RegisterSession("s1", "tok1", 1, kinds("heuristic"))

			// Establish a definite turn.
			e.OnOutput("s1", spinnerSnap())
			if got, _ := rec.last(); got.s.Turn != status.TurnActive {
				t.Fatalf("precondition turn=%s, want active", got.s.Turn)
			}
			settled := rec.count()

			// An inconclusive read preserves the committed turn and emits nothing.
			e.OnOutput("s1", tc.snap)
			if rec.count() != settled {
				t.Fatalf("%s grid emitted %d change(s); an inconclusive read must preserve, not commit (ADR-007)", tc.name, rec.count()-settled)
			}
			if got, _ := rec.last(); got.s.Turn != status.TurnActive {
				t.Fatalf("%s grid -> turn=%s, want active held (ADR-007)", tc.name, got.s.Turn)
			}

			// Re-evaluating the same inconclusive grid is stable (deterministic).
			e.OnOutput("s1", tc.snap)
			if rec.count() != settled {
				t.Fatalf("%s grid second eval emitted %d change(s); want stable, no flip-flop", tc.name, rec.count()-settled)
			}
			if got, _ := rec.last(); got.s.Turn != status.TurnActive {
				t.Fatalf("%s grid second eval -> turn=%s, want stable active", tc.name, got.s.Turn)
			}
		})
	}
}

// E10.8 (T-3): no busy-poll. The engine does periodic work ONLY when the daemon
// drives Tick. Advancing the clock without Tick samples no CPU and emits nothing;
// each Tick samples exactly once per registered session (bounded frequency).
func TestNoBusyPoll(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	cpu := &countingCPU{value: 0}
	e := newEngine(clk, cpu.sample, rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook", "heuristic"))

	clk.advance(10 * time.Minute)
	if cpu.calls() != 0 {
		t.Fatalf("CPU sampled %d times with no Tick; the engine must not busy-poll", cpu.calls())
	}
	if rec.count() != 0 {
		t.Fatalf("engine emitted %d change(s) with no Tick and no signal; it must be externally driven", rec.count())
	}

	e.Tick()
	if cpu.calls() != 1 {
		t.Fatalf("one Tick sampled CPU %d times, want 1 per session", cpu.calls())
	}
	e.Tick()
	if cpu.calls() != 2 {
		t.Fatalf("two Ticks sampled CPU %d times, want 2 (one per session per Tick)", cpu.calls())
	}
}

// E10.8: the low-frequency Tick poll re-evaluates, so a heuristic-derived active
// that has since gone quiet (no new output, idle CPU, past threshold) is not left
// confidently active — Tick downgrades it to unknown (S7 via the fallback poll).
func TestTickReevaluatesStaleHeuristicActive(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	staleness := 30 * time.Second
	e := newEngine(clk, constCPU(0), rec, staleness, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("heuristic"))

	e.OnOutput("s1", spinnerSnap())
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("setup turn=%s, want active", got.s.Turn)
	}
	before := rec.count()

	clk.advance(2 * staleness)
	e.Tick()
	if rec.count() == before {
		t.Fatalf("Tick did not re-evaluate a stale heuristic active")
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnUnknown {
		t.Fatalf("stale heuristic active -> turn=%s, want unknown (S7)", got.s.Turn)
	}
}
