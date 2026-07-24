package engine

// R-C5 (Phase C): fixture-backed rule proof. Replays the agy and opencode
// fixtures committed in Phase B through evaluateGridWithRules with each CLI's
// declared rule set (docs/verification/cli-duo-adapters-evidence.md's frozen
// marker table): BYTE granularity inside agy's busy window (agy declares an
// idle rule, so false-idle is the safety property), 64-byte steps for opencode
// from busy start through capture end (no idle rule declared, so the rules
// layer cannot emit idle; the generic fallback's parked-cursor idle path is a
// residual risk the whole-capture never-idle sweep regression-checks), coarse
// (<=1KB) steps outside. Permanent regression tests for the Opus offset~6132
// false-idle repro and the agy [6228,6299] marker transient: never idle
// mid-turn; the transient classifies ACTIVE via the generic spinner fallback.
//
// Offset convention: "offset N" means capture[:N] has been fed (prefix length)
// — matching the evidence memo's own convention ("Exit screen | 10092 (capture
// end)" for a 10092-byte capture).

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/status"
)

func TestGridRulesFullTimeline_Agy(t *testing.T) {
	fx, err := fixtureio.LoadFixture("../adapter/agy/testdata/agy.json")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	sources := []adapter.SignalSource{
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "prompt-marker"}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "busy-contains", "value": "esc to cancel"}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "busy-contains", "value": "Generating..."}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "idle-line-equals", "value": ">"}},
	}
	rules := parseGridRules(sources)

	const (
		busyStart = 3802
		busyEnd   = 7261 // inclusive: last byte with a busy marker present
		gapStart  = 6228 // documented transient: neither marker matches
		gapUpper  = 6299 // inclusive: end of the documented transient
		settled   = 7262 // first byte satisfying the full idle check
	)
	// Byte-by-byte across the busy window, padded so the exact boundary bytes
	// are covered regardless of the driver's coarse/fine transition point.
	fineSpans := [][2]int{{busyStart - 15, busyEnd + 15}}

	var idleInsideWindow []int
	var nonActiveInsideWindow []int

	replayFixture(t, fx.PTYCapture, rules, fineSpans, timelineFineStepAgy, func(offset int, turn status.Turn, inter status.Interaction) {
		if offset < busyStart || offset > busyEnd {
			return
		}
		if turn == status.TurnIdle {
			idleInsideWindow = append(idleInsideWindow, offset)
		}
		if turn != status.TurnActive {
			nonActiveInsideWindow = append(nonActiveInsideWindow, offset)
		}
	})

	if len(idleInsideWindow) > 0 {
		t.Fatalf("agy: idle emitted inside the busy window at offsets %v (want zero)", idleInsideWindow)
	}
	// The whole window — INCLUDING the documented [6228,6299] marker gap — must
	// classify active. Inside the gap neither busy marker matches and idle is
	// braille-suppressed, so evaluation falls through to the GENERIC evaluator,
	// whose stock trailing-spinner rule sees the animated braille glyph and
	// classifies active. (The plan/memo originally predicted "unknown" here;
	// running the real evaluator corrected that — the audit simulators modeled
	// only the declared rules and omitted the generic fallback layer. Active is
	// the truthful verdict: the CLI is generating. The safety property — never
	// idle mid-turn — is asserted above.)
	if len(nonActiveInsideWindow) > 0 {
		t.Fatalf("agy: busy window was not active at offsets %v (gap [%d,%d] must be active via the generic spinner fallback)", nonActiveInsideWindow, gapStart, gapUpper)
	}

	// Explicit hard-frame offset: within [6100,6227], "esc to cancel" persists
	// while the bare ">" + border corroboration is ALSO present in the bottom-3
	// — precedence must still classify active (busy-contains beats
	// idle-line-equals; this is the Opus offset~6132 false-idle repro).
	hardFrame := snapAtOffset(t, fx.PTYCapture, 6150)
	if turn, _ := evaluateGridWithRules(hardFrame, rules); turn != status.TurnActive {
		t.Fatalf("offset 6150 (the offset~6132 hard-frame class) classified %s, want active", turn)
	}

	// Explicit gap-transient offset: neither busy marker is intact and idle is
	// braille-suppressed, so the verdict comes from the generic evaluator's
	// spinner rule — active, never idle.
	gapFrame := snapAtOffset(t, fx.PTYCapture, 6260)
	if turn, _ := evaluateGridWithRules(gapFrame, rules); turn != status.TurnActive {
		t.Fatalf("offset 6260 (inside the documented [6228,6299] transient) classified %s, want active via the generic spinner fallback", turn)
	}

	// Settled: the full idle corroboration holds from offset 7262 onward.
	for _, off := range []int{settled, settled + 5, settled + 10} {
		snap := snapAtOffset(t, fx.PTYCapture, off)
		if turn, _ := evaluateGridWithRules(snap, rules); turn != status.TurnIdle {
			t.Fatalf("settled offset %d classified %s, want idle", off, turn)
		}
	}
}

func TestGridRulesFullTimeline_Opencode(t *testing.T) {
	fx, err := fixtureio.LoadFixture("../adapter/opencode/testdata/opencode.json")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	sources := []adapter.SignalSource{
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "prompt-marker"}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "busy-contains", "value": "esc interrupt"}},
	}
	rules := parseGridRules(sources)

	const (
		busyStart = 33547
		busyEnd   = 67787 // inclusive
		settled   = 68087 // confirmed non-busy, sustained for the next 500 bytes (memo)
	)
	// Fine granularity spans the padded busy window THROUGH THE END OF THE
	// CAPTURE — R-H4 committee finding: the settled tail was previously only
	// spot-checked at 4 offsets below, leaving the "generic fallback never
	// emits idle here" claim an unproven inference. This extends it to a real
	// regression sweep of the whole tail at the existing fine-step constant
	// (adds ~132 evaluations: 8479 tail bytes / 64). The pre-submit prefix
	// (before the busy window) is untouched, coarse-stepped as before — it
	// precedes any turn and is not the claim under test.
	fineSpans := [][2]int{{busyStart - 15, len(fx.PTYCapture)}}

	var idleAnywhere []int
	var nonActiveInsideWindow []int

	// fineStep 64 (not byte-exact): the rules layer cannot emit idle for
	// opencode (no idle rule is declared in SignalSources); the generic
	// fallback's idle path requires a parked visible cursor, which this
	// whole-tail sweep now regression-checks at 64-byte granularity rather
	// than leaving as an inference. Byte-exact replay measured >10min in
	// normal builds and stays reserved for agy, the adapter that actually
	// declares an idle rule.
	replayFixture(t, fx.PTYCapture, rules, fineSpans, timelineFineStepOpencode, func(offset int, turn status.Turn, inter status.Interaction) {
		if turn == status.TurnIdle {
			idleAnywhere = append(idleAnywhere, offset)
		}
		if offset < busyStart || offset > busyEnd {
			return
		}
		if turn != status.TurnActive {
			nonActiveInsideWindow = append(nonActiveInsideWindow, offset)
		}
	})

	if len(idleAnywhere) > 0 {
		t.Fatalf("opencode: idle emitted at offsets %v (want zero anywhere from the busy window through the end of the capture — opencode declares no idle rule)", idleAnywhere)
	}
	if len(nonActiveInsideWindow) > 0 {
		t.Fatalf("opencode: busy window had non-active offsets %v (want active everywhere, zero gaps)", nonActiveInsideWindow)
	}

	// Settled window: opencode declares no idle rule, so it must classify
	// unknown, and critically NEVER active (the busy marker must be absent).
	for _, off := range []int{settled, settled + 100, settled + 300, settled + 500} {
		snap := snapAtOffset(t, fx.PTYCapture, off)
		turn, _ := evaluateGridWithRules(snap, rules)
		if turn == status.TurnActive || turn == status.TurnIdle {
			t.Fatalf("opencode settled offset %d classified %s, want unknown (no idle rule declared, busy marker absent)", off, turn)
		}
	}
}
