package engine

// R-C1/R-C3 (Phase C): the descriptor-driven grid-rule evaluator. An adapter
// declares "heuristic" SignalSources carrying Descriptor{"grid": name, "value":
// v}; RegisterSession parses these ONCE into an immutable gridRules struct (so a
// caller mutating its descriptor maps afterward cannot change evaluation), and
// OnOutput evaluates via evaluateGridWithRules with precedence busy-contains >
// idle-line-equals > the generic evaluateGrid fallback ("prompt-marker" is
// always declarative-only). This file is the unit battery: pure
// parseGridRules/evaluateGridWithRules cases, plus the two engine-level
// integration tests that need RegisterSession/OnOutput (grid-rule wiring,
// descriptor-map mutation safety).

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// heuristicSource builds a single heuristic SignalSource with the given grid
// rule name/value, mirroring how a real adapter declares one (e.g. claude.go's
// SignalSources). An empty value omits the "value" key entirely (as
// "prompt-marker" declares it today).
func heuristicSource(grid, value string) adapter.SignalSource {
	d := map[string]string{"grid": grid}
	if value != "" {
		d["value"] = value
	}
	return adapter.SignalSource{Kind: "heuristic", Descriptor: d}
}

// gridByRow builds an 80-column snapshot from rows given TOP TO BOTTOM (row 0
// first, matching snapFromLines' convention): the LAST argument is the
// bottommost row, i.e. content-index 0 in the R-C1 sense (content lines are
// scanned upward from the bottom, skipping blank rows). The cursor is hidden,
// since grid rules never require it.
func gridByRow(rows ...string) *vt.Snap {
	return snapFromLines(80, 0, 0, false, rows)
}

// --- parseGridRules ---------------------------------------------------------

func TestParseGridRules_UnknownNameIgnored(t *testing.T) {
	r := parseGridRules([]adapter.SignalSource{heuristicSource("bogus-rule", "x")})
	if len(r.busy) != 0 || len(r.idle) != 0 {
		t.Fatalf("unknown grid name produced rules: %+v", r)
	}
}

func TestParseGridRules_EmptyValueIgnored(t *testing.T) {
	r := parseGridRules([]adapter.SignalSource{heuristicSource("busy-contains", "")})
	if len(r.busy) != 0 {
		t.Fatalf("empty value produced a busy rule: %+v", r)
	}
}

func TestParseGridRules_MissingGridKeyIgnored(t *testing.T) {
	src := adapter.SignalSource{Kind: "heuristic", Descriptor: map[string]string{"value": "x"}}
	r := parseGridRules([]adapter.SignalSource{src})
	if len(r.busy) != 0 || len(r.idle) != 0 {
		t.Fatalf("a descriptor with no grid key produced rules: %+v", r)
	}
}

func TestParseGridRules_PromptMarkerIsDeclarativeOnly(t *testing.T) {
	r := parseGridRules([]adapter.SignalSource{heuristicSource("prompt-marker", "")})
	if len(r.busy) != 0 || len(r.idle) != 0 {
		t.Fatalf("prompt-marker produced rules: %+v", r)
	}
}

func TestParseGridRules_NonHeuristicKindIgnored(t *testing.T) {
	src := adapter.SignalSource{Kind: "hook", Descriptor: map[string]string{"grid": "busy-contains", "value": "x"}}
	r := parseGridRules([]adapter.SignalSource{src})
	if len(r.busy) != 0 {
		t.Fatalf("a non-heuristic SignalSource contributed a grid rule: %+v", r)
	}
}

func TestParseGridRules_DeclarationOrderPreserved(t *testing.T) {
	r := parseGridRules([]adapter.SignalSource{
		heuristicSource("busy-contains", "first"),
		heuristicSource("busy-contains", "second"),
		heuristicSource("idle-line-equals", "a"),
		heuristicSource("idle-line-equals", "b"),
	})
	if len(r.busy) != 2 || r.busy[0] != "first" || r.busy[1] != "second" {
		t.Fatalf("busy rules not in declaration order: %v", r.busy)
	}
	if len(r.idle) != 2 || r.idle[0] != "a" || r.idle[1] != "b" {
		t.Fatalf("idle rules not in declaration order: %v", r.idle)
	}
}

func TestParseGridRules_NilAndEmptySourcesSafe(t *testing.T) {
	if r := parseGridRules(nil); len(r.busy) != 0 || len(r.idle) != 0 {
		t.Fatalf("nil sources produced rules: %+v", r)
	}
	if r := parseGridRules([]adapter.SignalSource{}); len(r.busy) != 0 || len(r.idle) != 0 {
		t.Fatalf("empty sources produced rules: %+v", r)
	}
}

// --- evaluateGridWithRules: busy-contains -----------------------------------

func TestEvaluateGridWithRules_BusyContains_Match(t *testing.T) {
	rules := gridRules{busy: []string{"esc to cancel"}}
	snap := gridByRow("prose above", "footer: esc to cancel now")
	turn, inter := evaluateGridWithRules(snap, rules)
	if turn != status.TurnActive || inter != status.InteractionNone {
		t.Fatalf("busy-contains match -> (%s,%s), want (active,none)", turn, inter)
	}
}

func TestEvaluateGridWithRules_BusyContains_NoMatch(t *testing.T) {
	rules := gridRules{busy: []string{"esc to cancel"}}
	snap := gridByRow("prose above", "nothing relevant here")
	turn, inter := evaluateGridWithRules(snap, rules)
	if turn != status.TurnUnknown || inter != status.InteractionUnknown {
		t.Fatalf("no busy match -> (%s,%s), want the generic fallback (unknown,unknown)", turn, inter)
	}
}

func TestEvaluateGridWithRules_BusyContains_BottomSixBoundary(t *testing.T) {
	rules := gridRules{busy: []string{"MARK"}}
	// Exactly at content-index 5 (the 6th line up from the bottom, still within
	// bottom-6) must fire.
	within := gridByRow("MARK here", "c4", "c3", "c2", "c1", "c0")
	if turn, _ := evaluateGridWithRules(within, rules); turn != status.TurnActive {
		t.Fatalf("marker at content-index 5 (bottom-6 boundary) did not fire: turn=%s", turn)
	}
	// One content line further up (content-index 6, K+1) must NOT fire.
	outside := gridByRow("MARK here", "c5", "c4", "c3", "c2", "c1", "c0")
	if turn, _ := evaluateGridWithRules(outside, rules); turn != status.TurnUnknown {
		t.Fatalf("marker at content-index 6 (past bottom-6) fired: turn=%s, want unknown", turn)
	}
}

func TestEvaluateGridWithRules_BusyContains_BlankRowsSkipped(t *testing.T) {
	rules := gridRules{busy: []string{"MARK"}}
	// 6 content lines with 2 blank rows interspersed: MARK sits at content-index
	// 5 (the bottommost 6 CONTENT lines), so it must still fire despite being 8
	// physical rows up.
	snap := gridByRow("MARK here", "", "c4", "c3", "", "c2", "c1", "c0")
	turn, _ := evaluateGridWithRules(snap, rules)
	if turn != status.TurnActive {
		t.Fatalf("blank rows were not skipped when counting the bottom-6 content lines: turn=%s", turn)
	}
}

func TestEvaluateGridWithRules_BusyContains_Unicode(t *testing.T) {
	rules := gridRules{busy: []string{"生成中..."}}
	snap := gridByRow("footer 生成中... spinner")
	turn, _ := evaluateGridWithRules(snap, rules)
	if turn != status.TurnActive {
		t.Fatalf("unicode busy marker did not match: turn=%s", turn)
	}
}

func TestEvaluateGridWithRules_BusyContains_MultipleRulesAnyMatch(t *testing.T) {
	rules := gridRules{busy: []string{"esc to cancel", "Generating..."}}
	onlySecond := gridByRow("footer shows Generating... now")
	if turn, _ := evaluateGridWithRules(onlySecond, rules); turn != status.TurnActive {
		t.Fatalf("second busy rule alone did not fire: turn=%s", turn)
	}
	onlyFirst := gridByRow("footer shows esc to cancel now")
	if turn, _ := evaluateGridWithRules(onlyFirst, rules); turn != status.TurnActive {
		t.Fatalf("first busy rule alone did not fire: turn=%s", turn)
	}
}

func TestEvaluateGridWithRules_BusyContains_EmptyRuleValueNeverMatches(t *testing.T) {
	// Defense in depth: even a directly-constructed gridRules (bypassing
	// parseGridRules, which already filters empty values) must never let an
	// empty value match every line via strings.Contains(l, "").
	rules := gridRules{busy: []string{""}}
	snap := gridByRow("perfectly ordinary prose")
	turn, _ := evaluateGridWithRules(snap, rules)
	if turn != status.TurnUnknown {
		t.Fatalf("an empty busy-contains value matched: turn=%s, want unknown", turn)
	}
}

// --- evaluateGridWithRules: idle-line-equals --------------------------------

func TestEvaluateGridWithRules_IdleLineEquals_MatchWithBorderBelow(t *testing.T) {
	rules := gridRules{idle: []string{">"}}
	// content-index 0 = border (bottommost), content-index 1 = ">" prompt.
	snap := gridByRow(">", "──────────")
	turn, inter := evaluateGridWithRules(snap, rules)
	if turn != status.TurnIdle || inter != status.InteractionNone {
		t.Fatalf("idle-line-equals with a border below -> (%s,%s), want (idle,none)", turn, inter)
	}
}

func TestEvaluateGridWithRules_IdleLineEquals_NoMatchWithoutBorderBelow(t *testing.T) {
	rules := gridRules{idle: []string{">"}}
	snap := gridByRow(">", "just some prose")
	turn, _ := evaluateGridWithRules(snap, rules)
	if turn != status.TurnUnknown {
		t.Fatalf("idle-line-equals fired without a border line below: turn=%s, want unknown", turn)
	}
}

func TestEvaluateGridWithRules_IdleLineEquals_BottommostLineHasNoLineBelow(t *testing.T) {
	rules := gridRules{idle: []string{">"}}
	// ">" IS the bottommost content line (content-index 0): nothing physically
	// below it to corroborate, so it must not fire (and must not panic on the
	// idx-1 lookup).
	snap := gridByRow("prose", ">")
	turn, _ := evaluateGridWithRules(snap, rules)
	if turn != status.TurnUnknown {
		t.Fatalf("idle-line-equals fired at content-index 0 with no line below: turn=%s, want unknown", turn)
	}
}

func TestEvaluateGridWithRules_IdleLineEquals_BottomThreeBoundary(t *testing.T) {
	rules := gridRules{idle: []string{">"}}
	// ">" at content-index 2 (within bottom-3) with a border immediately below
	// it (content-index 1) must fire.
	within := gridByRow("filler", ">", "──────────", "filler0")
	if turn, _ := evaluateGridWithRules(within, rules); turn != status.TurnIdle {
		t.Fatalf("idle line at content-index 2 (bottom-3 boundary) did not fire: turn=%s", turn)
	}
	// ">" at content-index 3 (K+1, one past the bottom-3 window) with a genuine
	// border line immediately below it (content-index 2) must NOT fire: the
	// ONLY disqualifier is the window bound, so a widened idle window (K=4)
	// would be caught by this case.
	outside := gridByRow(">", "──────────", "filler1", "filler0")
	if turn, _ := evaluateGridWithRules(outside, rules); turn != status.TurnUnknown {
		t.Fatalf("idle line at content-index 3 (past bottom-3) fired: turn=%s, want unknown", turn)
	}
}

func TestEvaluateGridWithRules_IdleLineEquals_TrimSpaceOnLeadingWhitespace(t *testing.T) {
	rules := gridRules{idle: []string{">"}}
	// A leading-space-padded prompt line ("content line" only right-trims, so
	// the leading spaces survive into the stored content line); the
	// idle-line-equals comparison additionally TrimSpace's before comparing.
	snap := gridByRow("   >", "──────────")
	turn, _ := evaluateGridWithRules(snap, rules)
	if turn != status.TurnIdle {
		t.Fatalf("leading-whitespace-padded idle line did not TrimSpace-match: turn=%s", turn)
	}
}

func TestEvaluateGridWithRules_IdleLineEquals_BrailleSuppressesIdle(t *testing.T) {
	rules := gridRules{idle: []string{">"}}
	// A braille spinner rune elsewhere in the bottom-6 (not on the idle line
	// itself) must suppress idle-line-equals as defense in depth (R-C1), even
	// though no busy-contains rule is declared here.
	snap := gridByRow("⣷ still animating", "filler", ">", "──────────")
	turn, _ := evaluateGridWithRules(snap, rules)
	if turn != status.TurnUnknown {
		t.Fatalf("idle fired despite a braille rune in bottom-6: turn=%s, want unknown (braille-suppressed)", turn)
	}
}

func TestEvaluateGridWithRules_IdleLineEquals_NoCursorRequirement(t *testing.T) {
	rules := gridRules{idle: []string{">"}}
	// Cursor hidden and not parked on the prompt row: unlike the generic
	// evaluator's sentinel+cursor rule, idle-line-equals has no cursor
	// requirement (R-C1 — these TUIs hide the cursor).
	snap := snapFromLines(80, 0, 0, false, []string{">", "──────────"})
	turn, _ := evaluateGridWithRules(snap, rules)
	if turn != status.TurnIdle {
		t.Fatalf("idle-line-equals required a parked cursor: turn=%s, want idle regardless of cursor", turn)
	}
}

func TestEvaluateGridWithRules_IdleLineEquals_EmptyRuleValueNeverMatches(t *testing.T) {
	rules := gridRules{idle: []string{""}}
	// A tab-only row: TrimRight(text, " ") does not strip tabs, so this line
	// survives as a "content line" (non-blank after right-trim), but
	// strings.TrimSpace DOES reduce it to "" — the exact landmine an unguarded
	// empty rule value would trip on (with a border line right below it).
	snap := gridByRow("\t", "──────────")
	turn, _ := evaluateGridWithRules(snap, rules)
	if turn != status.TurnUnknown {
		t.Fatalf("an empty idle-line-equals value matched: turn=%s, want unknown", turn)
	}
}

// --- precedence + fallback ---------------------------------------------------

func TestEvaluateGridWithRules_Precedence_BusyBeatsIdle(t *testing.T) {
	rules := gridRules{busy: []string{"esc to cancel"}, idle: []string{">"}}
	// Both a busy marker AND an idle-shaped prompt+border are present (the
	// offset~6132 hard-frame class): busy-contains must win.
	snap := gridByRow("esc to cancel", ">", "──────────")
	turn, inter := evaluateGridWithRules(snap, rules)
	if turn != status.TurnActive || inter != status.InteractionNone {
		t.Fatalf("busy-contains did not take precedence over idle-line-equals: (%s,%s)", turn, inter)
	}
}

func TestEvaluateGridWithRules_NoRuleMatch_GenericFallbackStands(t *testing.T) {
	rules := gridRules{busy: []string{"esc to cancel"}, idle: []string{">"}}
	// Neither declared rule matches this grid, but the GENERIC evaluator's own
	// spinner rule does: its verdict must stand (the fallback is a real
	// evaluation, not a forced unknown).
	snap := gridByRow("⠋ Working")
	turn, inter := evaluateGridWithRules(snap, rules)
	if turn != status.TurnActive || inter != status.InteractionNone {
		t.Fatalf("generic fallback verdict did not stand: (%s,%s), want (active,none) via the spinner rule", turn, inter)
	}
}

func TestEvaluateGridWithRules_NoMatchAnywhere_Unknown(t *testing.T) {
	rules := gridRules{busy: []string{"esc to cancel"}, idle: []string{">"}}
	snap := gridByRow("just some ordinary prose")
	turn, inter := evaluateGridWithRules(snap, rules)
	if turn != status.TurnUnknown || inter != status.InteractionUnknown {
		t.Fatalf("inconclusive grid -> (%s,%s), want (unknown,unknown)", turn, inter)
	}
}

func TestEvaluateGridWithRules_RuleOrderDoesNotChangeVerdict(t *testing.T) {
	snap := gridByRow("Generating... footer")
	forward := gridRules{busy: []string{"esc to cancel", "Generating..."}}
	backward := gridRules{busy: []string{"Generating...", "esc to cancel"}}
	t1, i1 := evaluateGridWithRules(snap, forward)
	t2, i2 := evaluateGridWithRules(snap, backward)
	if t1 != t2 || i1 != i2 || t1 != status.TurnActive {
		t.Fatalf("declaration order changed the verdict: forward=(%s,%s) backward=(%s,%s)", t1, i1, t2, i2)
	}
}

func TestEvaluateGridWithRules_DuplicateRulesHarmless(t *testing.T) {
	rules := gridRules{busy: []string{"esc to cancel", "esc to cancel"}}
	snap := gridByRow("esc to cancel footer")
	turn, _ := evaluateGridWithRules(snap, rules)
	if turn != status.TurnActive {
		t.Fatalf("a duplicate busy rule broke matching: turn=%s", turn)
	}
}

// --- nil/zero safety + determinism ------------------------------------------

func TestEvaluateGridWithRules_NilSnap(t *testing.T) {
	rules := gridRules{busy: []string{"esc to cancel"}, idle: []string{">"}}
	turn, inter := evaluateGridWithRules(nil, rules)
	if turn != status.TurnUnknown || inter != status.InteractionUnknown {
		t.Fatalf("nil snap -> (%s,%s), want (unknown,unknown)", turn, inter)
	}
}

func TestEvaluateGridWithRules_ZeroValueSnap(t *testing.T) {
	rules := gridRules{busy: []string{"esc to cancel"}, idle: []string{">"}}
	turn, inter := evaluateGridWithRules(&vt.Snap{}, rules)
	if turn != status.TurnUnknown || inter != status.InteractionUnknown {
		t.Fatalf("zero-value snap -> (%s,%s), want (unknown,unknown)", turn, inter)
	}
}

func TestEvaluateGridWithRules_ZeroValueRules(t *testing.T) {
	// A zero-value gridRules (no rules declared, e.g. only prompt-marker) must
	// behave exactly like the generic evaluator.
	snap := gridByRow("⠋ Working")
	turn, inter := evaluateGridWithRules(snap, gridRules{})
	wantTurn, wantInter := evaluateGrid(snap)
	if turn != wantTurn || inter != wantInter {
		t.Fatalf("zero-value rules -> (%s,%s), want the generic verdict (%s,%s)", turn, inter, wantTurn, wantInter)
	}
}

func TestEvaluateGridWithRules_Determinism(t *testing.T) {
	rules := gridRules{busy: []string{"esc to cancel"}, idle: []string{">"}}
	snap := gridByRow("esc to cancel footer")
	t1, i1 := evaluateGridWithRules(snap, rules)
	t2, i2 := evaluateGridWithRules(snap, rules)
	if t1 != t2 || i1 != i2 {
		t.Fatalf("evaluateGridWithRules is not deterministic: (%s,%s) vs (%s,%s)", t1, i1, t2, i2)
	}
}

// --- isBorderLine classifier -------------------------------------------------

func TestIsBorderLine(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"three-border-runes-full", "───", true},
		{"two-runes-below-minimum", "──", false},
		{"five-runes-eighty-percent", "────x", true},
		{"five-runes-sixty-percent", "───xy", false},
		{"box-drawing-corners", "┌──┐", true},
		{"ordinary-prose", "hello", false},
		{"leading-spaces-below-threshold", "  ──x", false},
		{"empty", "", false},
		{"pure-underscore-rule", "____", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := isBorderLine(tc.text); got != tc.want {
				t.Fatalf("isBorderLine(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// --- engine wiring (R-C2) ----------------------------------------------------

func TestEngine_OnOutput_UsesDeclaredGridRules(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	sources := []adapter.SignalSource{
		heuristicSource("prompt-marker", ""),
		heuristicSource("busy-contains", "esc to cancel"),
	}
	e.RegisterSession("s1", "tok1", 1, sources)

	snap := gridByRow("footer: esc to cancel now")
	e.OnOutput("s1", snap)
	got, ok := rec.last()
	if !ok || got.s.Turn != status.TurnActive {
		t.Fatalf("engine did not apply the declared busy-contains rule: got=%+v ok=%v", got, ok)
	}
}

func TestEngine_RegisterSession_DescriptorMapMutationAfterRegisterHasNoEffect(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)

	desc := map[string]string{"grid": "busy-contains", "value": "ORIGINAL"}
	sources := []adapter.SignalSource{{Kind: "heuristic", Descriptor: desc}}
	e.RegisterSession("s1", "tok1", 1, sources)

	// Mutate the descriptor map in place after registration: R-C2 requires the
	// session's rules were parsed ONCE and copied out of it, so this must not
	// be observable.
	desc["value"] = "MUTATED"

	// The mutated value does NOT match (proving the mutation had no effect): from
	// the unknown seed, a frame carrying only the MUTATED value is inconclusive
	// (no rule fires, generic reads prose), which ADR-007 preserves without an
	// emit. An effective mutation would fire busy-contains and emit active here.
	e.OnOutput("s1", gridByRow("footer shows MUTATED now"))
	if got, ok := rec.last(); ok && got.s.Turn == status.TurnActive {
		t.Fatalf("descriptor mutation after RegisterSession took effect: turn=%s", got.s.Turn)
	}

	// The original value still matches.
	e.OnOutput("s1", gridByRow("footer shows ORIGINAL now"))
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("post-registration mutation lost the ORIGINAL rule: turn=%s", got.s.Turn)
	}
}
