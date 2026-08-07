package gate

// FAILING-FIRST (TDD RED, GG-5) for PB-DS-1 (spacing scale) and PB-DS-4 (shape scale), S22b.
//
// PB-DS-1: "The design has none -- 16 distinct literal values in Substrate alone ... Decision: a
// 2dp grid of ten steps (2, 4, 6, 8, 10, 12, 14, 16, 18, 24) plus three named frame constants
// (screen_top 54dp, screen_bottom 76dp, tabbar_height 74dp)."
//
// PB-DS-4: "A shape scale from the radius tokens, with the degeneracy recorded: --p-dot-r: 4px
// is declared against a 7px box, so 4 >= 3.5 renders a full circle and the literal 4 is
// unreachable. A token whose declared value never renders must say so where an implementer
// reads it, or it will be re-derived as a rounded rect."
//
// THE STATE OF THE WORLD, verified before these assertions were written:
//
//	android/app/src/main/res/values/           colors.xml, strings.xml, themes.xml -- no dimens.xml
//	production Kotlin                          zero R.dimen references
//	PhoneSurface.kt:743                        const val PADDING = 24   (raw px, not dp)
//
// WHERE THE EXPECTED NUMBERS COME FROM. Not from here. The ten scale steps are the decision and
// are named in the requirement, so they are named here; everything they are checked AGAINST is
// computed at test time by s22b_designsource_test.go. The drift ledger reads the phone-kit block
// of docs/research/obsidian-maquette.html, which ADR-009 D2 makes the normative design source;
// the four radii read internal/design/tokens.json, which transcribes the same maquette; the three
// frame constants and the dot's degeneracy still read the older directions artifact, for the two
// reasons set out at s22bMaquetteRelPath -- neither is a skin value and the maquette states
// neither.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// s22bScale is the DECISION recorded in PB-DS-1's own text, restated as the ten dimen names the
// app must carry, each with the design literals it absorbs.
//
// IT IS A TABLE AND NOT AN ARITHMETIC, and the first version of this gate got that wrong. The
// obvious implementation -- round each literal to the nearest step -- cannot reproduce the
// recorded decision, because two of the movers are exactly equidistant and they go opposite
// ways: 5 sits between 4 and 6 and is absorbed DOWN to 4, while 7 sits between 6 and 8 and is
// absorbed UP to 8. No tie-break rule gives both. The assignment is a design judgement (a 7px
// gap is the layout's tightest rhythm and tightening it further reads as a bug; a 5px dot margin
// is decoration and loses nothing), so it is recorded here where a reviewer can disagree with
// it, and what the gate COMPUTES is the drift each assignment costs.
//
// EVERY STEP NOW ABSORBS SOMETHING, which is new and is the maquette's doing. Against the
// Substrate artifact this table read
//
//	{"swarm_space_6", 6, nil},
//	{"swarm_space_24", 24, nil},
//
// with the recorded reason that those two steps existed only for screens not yet built --
// swarm_space_6 for the mock's badge padding, swarm_space_24 for its pairing scaffold, neither
// declared by Substrate itself. The Obsidian maquette draws all of those screens and spends both:
// 6px on the badge, the chip gap, the field label and the stale notice; 24px on the nav, the
// drill header, the tab bar's bottom inset and the empty state. The scale is now justified by the
// design end to end rather than by an argument about the future, and the "absorbs nothing" case
// is asserted below in both directions so it cannot come back unnoticed.
//
// THE THIRD ENTRY THAT MOVED IS A TIE, and ties are why this is a table. 3px joins swarm_space_2
// rather than swarm_space_4: it is `.arow .ab { margin-top }`, the gap between an activity row's
// timestamp and its body, and the two other sub-label gaps in the maquette
// (`.trow .lbl .l2` and `.mrow .m1 .s`) are both 2px. Absorbing down makes the three consistent;
// absorbing up would leave one of them alone at 4dp for no reason a reader could name.
var s22bScale = []struct {
	Name    string
	Dp      float64
	Absorbs []float64
}{
	{"swarm_space_2", 2, []float64{2, 3}},
	{"swarm_space_4", 4, []float64{4, 5}},
	{"swarm_space_6", 6, []float64{6}},
	{"swarm_space_8", 8, []float64{7, 8, 9}},
	{"swarm_space_10", 10, []float64{10, 11}},
	{"swarm_space_12", 12, []float64{12, 13}},
	{"swarm_space_14", 14, []float64{14, 15}},
	{"swarm_space_16", 16, []float64{16}},
	{"swarm_space_18", 18, []float64{18}},
	{"swarm_space_24", 24, []float64{24}},
}

// The three frame constants, each with the CSS fact it must equal. `.pscreen`'s padding is the
// safe-area frame the whole app draws inside; `.ptabs`'s height is the bar that occupies the
// bottom of it. They are NOT scale steps -- 54, 76 and 74 are not on a 2dp grid anyone would
// recognise -- so they are named separately rather than rounded into the scale.
var s22bFrame = []struct {
	Name     string
	Selector string
	Property string
	Index    int // which value of a shorthand: `padding: 54px 0 76px` is top/x/bottom
	Why      string
}{
	{"swarm_screen_top", ".pscreen", "padding", 0, "the status-bar inset every screen starts below"},
	{"swarm_screen_bottom", ".pscreen", "padding", 2, "the inset the tab bar occupies"},
	{"swarm_tabbar_height", ".ptabs", "height", 0, "the tab bar itself"},
}

// The four radii, each with the token that IS its value. --p-dot-r is deliberately absent; see
// TestPBDS4_TheDotRadiusTokenIsNotTranscribedAsARadius.
var s22bRadii = []struct {
	Name  string
	Token string
}{
	{"swarm_radius_card", "--p-card-r"},
	{"swarm_radius_sheet", "--p-sheet-r"},
	{"swarm_radius_button", "--p-btn-r"},
	{"swarm_radius_chip", "--p-chip-r"},
}

// s22bSpacingProps are the CSS properties that place things RELATIVE to each other, which is
// what a spacing scale governs. Deliberately not `top`/`left`/`width`/`height`/`inset`: those
// are absolute placement, and folding them in would drag `.ptime`'s `left: 30px` -- a status-bar
// clock position, 6dp from the nearest step -- into a scale it has no business being in.
var s22bSpacingProps = map[string]bool{
	"padding": true, "padding-top": true, "padding-right": true,
	"padding-bottom": true, "padding-left": true,
	"margin": true, "margin-top": true, "margin-right": true,
	"margin-bottom": true, "margin-left": true,
	"gap": true, "row-gap": true, "column-gap": true,
}

// s22bDesignSpacings returns every non-zero spacing literal the maquette's phone-kit CSS
// declares, value -> the selectors that declare it. ADR-009 D2: the maquette is the design.
//
// The frame constants are excluded, and they are excluded BY THE SAME TABLE that asserts them
// rather than by a second list of numbers: `.pscreen { padding: 54px 0 76px }` is a spacing
// declaration by every syntactic test, and it is the safe-area frame, which the requirement
// names separately precisely because it is not on the grid. Deriving the exclusion from
// s22bFrame means a frame constant cannot be dropped from one place and survive in the other.
func s22bDesignSpacings(t *testing.T) map[float64][]string {
	t.Helper()
	frame := map[string]bool{}
	for _, f := range s22bFrame {
		frame[f.Selector+" "+f.Property] = true
	}
	out := map[float64][]string{}
	for sel, rule := range s22bMaquetteKitCSS(t) {
		for prop, value := range rule.Decls {
			if !s22bSpacingProps[prop] || frame[sel+" "+prop] {
				continue
			}
			for _, field := range strings.Fields(value) {
				px, ok := s22bPx(field)
				if !ok || px == 0 {
					continue
				}
				out[px] = append(out[px], sel+" {"+prop+"}")
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("PB-DS-1: no spacing literals parsed from the design source; the drift ledger " +
			"below would be computed over an empty set and pass saying nothing")
	}
	return out
}

// TestPBDS1_TheSpacingScaleIsDeclared is the requirement's first half.
func TestPBDS1_TheSpacingScaleIsDeclared(t *testing.T) {
	dimens := s22bDimens(t)

	previous := 0.0
	for _, step := range s22bScale {
		got, ok := dimens[step.Name]
		if !ok {
			t.Errorf("PB-DS-1: <dimen name=%q> is missing. The decision is a 2dp grid of ten "+
				"steps (2, 4, 6, 8, 10, 12, 14, 16, 18, 24); a scale with a hole in it is one "+
				"every future screen rounds around.", step.Name)
			continue
		}
		if got.Unit != "dp" {
			t.Errorf("PB-DS-1: <dimen name=%q> is %g%s. A spacing step must be dp: the defect "+
				"this requirement names by name is PhoneSurface's `PADDING = 24` in raw PIXELS, "+
				"which renders at 8dp on a 3x handset.", step.Name, got.Value, got.Unit)
		}
		if got.Value != step.Dp {
			t.Errorf("PB-DS-1: <dimen name=%q> is %gdp, want %gdp.", step.Name, got.Value, step.Dp)
		}
		// The grid property itself, rather than only the ten values: a scale is a RULE, and an
		// odd or non-ascending step is the point at which it stops being one.
		if math.Mod(step.Dp, 2) != 0 {
			t.Errorf("PB-DS-1: %gdp is not on the 2dp grid the requirement decided", step.Dp)
		}
		if step.Dp <= previous {
			t.Errorf("PB-DS-1: the scale is not ascending at %q (%gdp after %gdp)",
				step.Name, step.Dp, previous)
		}
		previous = step.Dp
	}

	// The other direction: a `swarm_space_*` that is not a step is a value that entered the
	// scale without passing through the decision, which is how "a scale" decays into "a scale
	// plus a few extras" -- the same shape PB-TOK-1 caught in colors.xml.
	declared := map[string]bool{}
	for _, step := range s22bScale {
		declared[step.Name] = true
	}
	var extra []string
	for name := range dimens {
		if strings.HasPrefix(name, "swarm_space_") && !declared[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("PB-DS-1: %s declares scale step(s) the decision does not contain: %s",
			mustRel(t, s22bDimensPath(t)), strings.Join(extra, ", "))
	}
}

// TestPBDS1_TheFrameConstantsAreTheDesignsOwn checks the three non-scale constants against the
// artifact rather than against the requirement's prose, because prose is where a number gets
// retyped. 54/76/74 appear in the requirement AND in the CSS; only one of those is the design.
func TestPBDS1_TheFrameConstantsAreTheDesignsOwn(t *testing.T) {
	css := s22bSharedCSS(t)
	dimens := s22bDimens(t)

	for _, frame := range s22bFrame {
		rule, ok := css[frame.Selector]
		if !ok {
			t.Errorf("PB-DS-1: the design source no longer declares `%s`, so %q has no origin",
				frame.Selector, frame.Name)
			continue
		}
		raw, ok := rule.Decls[frame.Property]
		if !ok {
			t.Errorf("PB-DS-1: `%s` declares no %s, so %q has no origin",
				frame.Selector, frame.Property, frame.Name)
			continue
		}
		fields := strings.Fields(raw)
		if frame.Index >= len(fields) {
			t.Errorf("PB-DS-1: `%s { %s: %s }` has no value at position %d",
				frame.Selector, frame.Property, raw, frame.Index)
			continue
		}
		want, ok := s22bPx(fields[frame.Index])
		if !ok {
			t.Errorf("PB-DS-1: `%s { %s }` value %q is not a px length",
				frame.Selector, frame.Property, fields[frame.Index])
			continue
		}
		got, ok := dimens[frame.Name]
		if !ok {
			t.Errorf("PB-DS-1: <dimen name=%q> is missing. It is %s -- %gdp in the design "+
				"(`%s { %s: %s }`) -- and it is NOT a scale step, which is why it is named "+
				"rather than rounded onto the grid.",
				frame.Name, frame.Why, want, frame.Selector, frame.Property, raw)
			continue
		}
		if got.Unit != "dp" {
			t.Errorf("PB-DS-1: <dimen name=%q> is %g%s, want dp", frame.Name, got.Value, got.Unit)
		}
		if got.Value != want {
			t.Errorf("PB-DS-1: <dimen name=%q> is %gdp; the design says %gpx "+
				"(`%s { %s: %s }`). CSS px in a 386x812 mock is Android dp at 1:1 -- there is no "+
				"scaling factor to explain a difference away.",
				frame.Name, got.Value, want, frame.Selector, frame.Property, raw)
		}
	}
}

// TestPBDS1_EveryDesignSpacingIsAbsorbedByTheScale is the claim the decision rests on.
//
// PB-DS-1 rejected a 4dp grid because it "uniformly loosens the layout by ~10%", and it accepted
// a 2dp grid because the drift is at most 1dp on the Substrate artifact. That is an arithmetic
// claim about a specific set of literals and it is checked rather than believed: every spacing
// value the design declares is rounded onto the scale here, and the ledger of what moves is
// asserted exactly.
func TestPBDS1_EveryDesignSpacingIsAbsorbedByTheScale(t *testing.T) {
	spacings := s22bDesignSpacings(t)

	// The recorded assignment, inverted: design literal -> the step that absorbs it.
	absorbedBy := map[float64]float64{}
	for _, step := range s22bScale {
		for _, v := range step.Absorbs {
			if prior, dup := absorbedBy[v]; dup {
				t.Errorf("PB-DS-1: %gpx is absorbed by both %gdp and %gdp; the assignment is "+
					"ambiguous and one of the two is what a screen will actually use",
					v, prior, step.Dp)
			}
			absorbedBy[v] = step.Dp
		}
	}

	values := make([]float64, 0, len(spacings))
	for v := range spacings {
		values = append(values, v)
	}
	sort.Float64s(values)

	var movers []string
	worst := 0.0
	for _, v := range values {
		step, ok := absorbedBy[v]
		if !ok {
			t.Errorf("PB-DS-1: the design declares %gpx (%s) and no scale step absorbs it. The "+
				"scale's claim is that it absorbs every spacing literal in the artifact; a value "+
				"with no step is one every screen will round by eye.",
				v, strings.Join(uniqueSorted(spacings[v]), ", "))
			continue
		}
		drift := math.Abs(v - step)
		if drift > worst {
			worst = drift
		}
		if drift > 0 {
			movers = append(movers, fmt.Sprintf("%g->%g (%+g)", v, step, step-v))
		}
	}

	// The other direction: a step that claims to absorb a literal the design does not declare is
	// a decision recorded against nothing. This is the assertion that keeps "every step is
	// justified" honest -- an Absorbs entry for a value the maquette stopped drawing fails here.
	for _, step := range s22bScale {
		for _, v := range step.Absorbs {
			if _, ok := spacings[v]; !ok {
				t.Errorf("PB-DS-1: %q claims to absorb %gpx, which the Obsidian maquette does "+
					"not declare as a spacing value", step.Name, v)
			}
		}
	}

	// The ledger, against the maquette. This assertion previously read
	//
	//	const wantMovers = 6
	//	"The requirement's ledger is six 1dp movers here plus 26->24, which lives in the
	//	 mock rather than in this artifact."
	//
	// -- six, because PB-DS-1's own ledger said "seven of sixteen values move, six by 1dp and one
	// by 2dp (26->24)" and the 26px lived only in a mock the gate did not read. The maquette is a
	// complete design rather than four candidate skins plus a mock, so the seventh mover is no
	// longer somewhere else: it declares 3px, and the count and the requirement's ledger agree
	// without a footnote for the first time. The 2dp mover is gone -- nothing in the maquette
	// drifts further than 1dp -- so seven movers, all of them by one.
	const wantMovers = 7
	if len(movers) != wantMovers {
		t.Errorf("PB-DS-1: %d of %d maquette spacing values move onto the scale, want %d.\n"+
			"\tmoved: %s\n"+
			"PB-DS-1's ledger is seven movers; a different count means the design moved and "+
			"nobody re-took the decision.",
			len(movers), len(values), wantMovers, strings.Join(movers, ", "))
	}
	if worst > 1 {
		t.Errorf("PB-DS-1: the worst drift over the maquette is %gdp, want at most 1dp. That "+
			"bound is the reason the 4dp grid was rejected; if it does not hold, the decision "+
			"was made on a number nobody computed.\n\tmoved: %s",
			worst, strings.Join(movers, ", "))
	}
	t.Logf("PB-DS-1 drift ledger over %d distinct maquette spacing values, worst %gdp:\n\t%s",
		len(values), worst, strings.Join(movers, "\n\t"))
}

// TestPBDS1_TheAbsorptionLedgerCanActuallyFail is the negative control for the ledger above.
//
// "Every value is absorbed" is trivially true of a table that maps each literal to itself, and
// the drift bound is trivially true of a subtraction that always returns zero. Both would leave
// the test above green over a scale that absorbs nothing, which is the shape this repository has
// had to reject before. So the two mechanisms are exercised against known answers here.
func TestPBDS1_TheAbsorptionLedgerCanActuallyFail(t *testing.T) {
	absorbedBy := map[float64]float64{}
	for _, step := range s22bScale {
		for _, v := range step.Absorbs {
			absorbedBy[v] = step.Dp
		}
	}

	// The two equidistant movers that go OPPOSITE ways. They are the reason this is a recorded
	// table rather than a rounding, and a table that had silently become "nearest step" would
	// send 7 to 6 and fail here.
	if got := absorbedBy[7]; got != 8 {
		t.Errorf("7px is absorbed by %gdp, want 8dp: the design's tightest rhythm is a 7px gap "+
			"and tightening it further is what the 4dp grid was rejected for", got)
	}
	if got := absorbedBy[5]; got != 4 {
		t.Errorf("5px is absorbed by %gdp, want 4dp", got)
	}
	if absorbedBy[7] == 7 || absorbedBy[5] == 5 {
		t.Fatal("the ledger maps a literal to itself, so every drift below is zero by " +
			"construction and the 1dp bound asserts nothing")
	}
	// A value the scale does NOT absorb must report absent rather than resolving to something.
	// 30px is 6dp from any step and the maquette does declare it -- `.markrow { gap }`, the
	// spacing between the icon tiles in the mark gallery. It is excluded by the block boundary
	// rather than by the property filter, which is exactly the boundary worth testing: gallery
	// furniture is not the app, and a scale that swallowed a 30px gap would be a scale sized by
	// the page the design was reviewed on.
	if step, ok := absorbedBy[30]; ok {
		t.Errorf("the ledger absorbs 30px into %gdp; it is the mark gallery's own gap, not a gap "+
			"in the app, and a scale that swallows it has been sized by the review page", step)
	}
	// And the drift arithmetic itself.
	if d := math.Abs(7 - absorbedBy[7]); d != 1 {
		t.Errorf("drift(7px -> %gdp) = %g, want 1", absorbedBy[7], d)
	}
	if d := math.Abs(12 - absorbedBy[12]); d != 0 {
		t.Errorf("drift(12px -> %gdp) = %g, want 0", absorbedBy[12], d)
	}
}

// TestPBDS1_NoRawPixelPaddingSurvives is PB-DS-1's last sentence, which names its subject: "The
// current `PADDING = 24` raw-pixel constant is deleted (it is px, not dp, and renders at ~8dp on
// a 3x handset)."
//
// IT IS NARROWER THAN PB-DS-11's FENCE ON PURPOSE. That requirement scans all production Kotlin
// and XML for any colour literal, any raw dimension and any Typeface reference, and it belongs to
// the component-kit slice, which is where the surface code that would trip it gets rewritten.
// What is asserted here is only the one constant this requirement deletes by name, plus the
// shape it would come back in: a layout dimension expressed as a bare integer.
func TestPBDS1_NoRawPixelPaddingSurvives(t *testing.T) {
	root := kotlinMainRoot(t)
	// Bare integers, or an identifier that is a compile-time constant standing in for one.
	// `setPadding(a, b, c, d)` where the arguments came from a dimen resource or from window
	// insets is the passing case, so the check is for a NUMERIC LITERAL argument specifically.
	call := regexp.MustCompile(`set(?:Padding|Margins)\s*\(([^)]*)\)`)
	literalArg := regexp.MustCompile(`(?:^|,)\s*-?\d+\s*(?:,|$)`)
	constant := regexp.MustCompile(`(?m)^\s*(?:private\s+)?const\s+val\s+([A-Z_]*(?:PADDING|MARGIN|INSET|GAP)[A-Z_]*)\s*(?::\s*Int\s*)?=\s*-?\d+`)

	found := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || !strings.HasSuffix(path, ".kt") {
			return nil //nolint:nilerr // an unreadable tree is reported by the count check below
		}
		found++
		src := kotlinCodeOnly(readFileOrFail(t, path, "PB-DS-1"))
		for _, m := range constant.FindAllStringSubmatch(src, -1) {
			t.Errorf("PB-DS-1: %s declares `const val %s` as a bare number. A layout dimension "+
				"written as an Int is in PIXELS, and pixels are not a unit any design states: "+
				"the constant this replaces rendered at 8dp on a 3x handset and at 24dp on a "+
				"1x one. Every spacing value comes from res/values/dimens.xml.",
				mustRel(t, path), m[1])
		}
		for _, m := range call.FindAllStringSubmatch(src, -1) {
			if literalArg.MatchString(m[1]) {
				t.Errorf("PB-DS-1: %s calls %s with a numeric literal. Use "+
					"resources.getDimensionPixelSize(R.dimen.swarm_space_*).",
					mustRel(t, path), strings.TrimSpace(m[0]))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PB-DS-1: walking %s: %v", mustRel(t, root), err)
	}
	if found == 0 {
		t.Fatalf("PB-DS-1: no Kotlin files found under %s; the scan above is vacuous",
			mustRel(t, root))
	}

	// The scan must be able to see the thing it is looking for. Without this, a regexp that
	// stopped matching -- or a kotlinCodeOnly that swallowed the file -- would report a clean
	// tree, which is indistinguishable from a clean tree.
	if !constant.MatchString("    const val PADDING = 24\n") {
		t.Fatal("the constant scan does not match the exact declaration PB-DS-1 names, so a " +
			"clean report above means nothing")
	}
	if !literalArg.MatchString("24, 24, 24, 24") {
		t.Fatal("the literal-argument scan does not match `setPadding(24, 24, 24, 24)`")
	}
	if literalArg.MatchString("pad, pad, pad, pad") {
		t.Fatal("the literal-argument scan matches a call whose arguments are identifiers, so it " +
			"would fail on the correct implementation as readily as on the wrong one")
	}
}

// TestPBDS4_TheRadiiAreTheRadiusTokens is PB-DS-4's first clause: four radii in dimens.xml.
func TestPBDS4_TheRadiiAreTheRadiusTokens(t *testing.T) {
	tokens := s22bTokenValues(t)
	dimens := s22bDimens(t)

	for _, r := range s22bRadii {
		raw, ok := tokens[r.Token]
		if !ok {
			t.Errorf("PB-DS-4: the token origin declares no %s, so %q has no origin", r.Token, r.Name)
			continue
		}
		want, ok := s22bPx(raw)
		if !ok {
			t.Errorf("PB-DS-4: %s = %q is not a px length", r.Token, raw)
			continue
		}
		got, ok := dimens[r.Name]
		if !ok {
			t.Errorf("PB-DS-4: <dimen name=%q> is missing; %s = %s has no Android form",
				r.Name, r.Token, raw)
			continue
		}
		if got.Unit != "dp" {
			t.Errorf("PB-DS-4: <dimen name=%q> is %g%s, want dp", r.Name, got.Value, got.Unit)
		}
		if got.Value != want {
			t.Errorf("PB-DS-4: <dimen name=%q> is %gdp and %s is %s. A radius that disagrees "+
				"with its token is the same defect class as a colour that does.",
				r.Name, got.Value, r.Token, raw)
		}
	}

	// The other direction. A swarm_radius_* with no token is a corner somebody chose by eye.
	known := map[string]bool{}
	for _, r := range s22bRadii {
		known[r.Name] = true
	}
	var extra []string
	for name := range dimens {
		if strings.HasPrefix(name, "swarm_radius_") && !known[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("PB-DS-4: %s declares radius dimen(s) with no token behind them: %s",
			mustRel(t, s22bDimensPath(t)), strings.Join(extra, ", "))
	}
}

// TestPBDS4_TheDotRadiusTokenIsNotTranscribedAsARadius is PB-DS-4's real content.
//
// --p-dot-r is 4px and it is declared on a 7x7px box. 2*4 >= 7, so CSS clamps it and the dot
// renders as a full circle: the literal 4 NEVER reaches a screen. The failure this guards is
// specific and it is the obvious thing to do -- transcribe the token into
// <dimen name="swarm_radius_dot">4dp</dimen>, hand it to a <shape android:shape="rectangle">,
// and ship a rounded square nobody designed.
//
// The degeneracy is COMPUTED from the artifact rather than asserted from the requirement's
// prose, so a design that later grew the dot to 12px would fail here and force the decision to
// be retaken instead of inherited.
func TestPBDS4_TheDotRadiusTokenIsNotTranscribedAsARadius(t *testing.T) {
	css := s22bSharedCSS(t)
	tokens := s22bTokenValues(t)

	dot, ok := css[".pdot"]
	if !ok {
		t.Fatalf("PB-DS-4: the design source no longer declares `.pdot`; the degeneracy this " +
			"test records has no subject and the assertion below would say nothing")
	}
	width, okW := s22bPx(dot.Decls["width"])
	height, okH := s22bPx(dot.Decls["height"])
	if !okW || !okH {
		t.Fatalf("PB-DS-4: `.pdot` no longer declares a px width and height (%q x %q)",
			dot.Decls["width"], dot.Decls["height"])
	}
	resolved, err := s22bResolveVars(dot.Decls["border-radius"], tokens)
	if err != nil {
		t.Fatalf("PB-DS-4: `.pdot { border-radius }`: %v", err)
	}
	radius, okR := s22bPx(resolved)
	if !okR {
		t.Fatalf("PB-DS-4: `.pdot { border-radius: %s }` did not resolve to a px length (%q)",
			dot.Decls["border-radius"], resolved)
	}

	shortest := math.Min(width, height)
	if 2*radius < shortest {
		t.Fatalf("PB-DS-4: the dot is %gx%gpx with radius %gpx, so 2r < %g and the token's "+
			"declared value DOES render. The degeneracy this requirement records no longer "+
			"holds; the oval and its comment must be re-decided rather than inherited.",
			width, height, radius, shortest)
	}
	t.Logf("PB-DS-4: --p-dot-r = %gpx on a %gx%gpx box; 2r = %g >= %g, so the corner is clamped "+
		"and the dot is a full circle. The literal %g is unreachable.",
		radius, width, height, 2*radius, shortest, radius)

	// The token must therefore have NO dimen. This is the assertion, and it is the one an
	// implementer trips by doing the obvious thing.
	dimens := s22bDimens(t)
	for _, forbidden := range []string{"swarm_radius_dot", "swarm_dot_radius"} {
		if d, ok := dimens[forbidden]; ok {
			t.Errorf("PB-DS-4: <dimen name=%q>%g%s</dimen> transcribes --p-dot-r as a radius. "+
				"It is not one: on a %gx%gpx box that value is clamped to a circle and never "+
				"renders. The dot is an oval shape; a radius resource for it can only be used to "+
				"draw a rounded rectangle the design does not contain.",
				forbidden, d.Value, d.Unit, width, height)
		}
	}

	// And the reason must be where an implementer READS it, which is the requirement's own
	// wording: "must say so where an implementer reads it, or it will be re-derived as a
	// rounded rect". dimens.xml is that place -- it is the file whose four radii sit one line
	// away from the fifth token that has none.
	//
	// The token is looked for WITHOUT its leading dashes, and that is a constraint rather than a
	// looseness: XML forbids a literal `--` inside a comment, so `--p-dot-r` cannot be written in
	// the only place this record belongs. (The same rule broke the whole Android build once
	// during this slice, from an em-dash-as-double-hyphen in colors.xml's prose.)
	src := readFileOrFail(t, s22bDimensPath(t), "PB-DS-4")
	for _, needle := range []string{"p-dot-r", "oval"} {
		if !strings.Contains(src, needle) {
			t.Errorf("PB-DS-4: %s does not mention %q. Four radii are declared there and the "+
				"fifth radius token is silently absent; absence is not a record, and the next "+
				"implementer reads the file rather than the ADR.",
				mustRel(t, s22bDimensPath(t)), needle)
		}
	}
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
