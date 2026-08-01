package design

// FAILING-FIRST (TDD RED, GG-5) for PB-TOK-7.
//
// "Derived values are computed, never transcribed. The artifact resolves four colours with
//  color-mix(in srgb, ...) -- the attention-row border #6D5220, the deny fill #21FF6369, and the
//  two dot glows #B3F1A10D / #8C00C2D7. Transcribing a resolved hex creates exactly the
//  third-copy-of-the-palette defect PB-TOK-1 was written to catch, one indirection further out
//  where the existing gate cannot see it."
//
// THE STATE OF THE WORLD, verified before these assertions were written: internal/design carries
// tokens.json and one test file that compares it against the design HTML. There is no blend
// function anywhere in the repository -- `grep -ri color-mix` finds the four values only in
// documents, as prose. Every consumer that needs one of them today has no choice but to type the
// hex, and a hex typed into Kotlin is a value the PB-TOK-1 join cannot see, because that join
// only knows about token NAMES.
//
// WHY THE TWO FORMS OF color-mix ARE THE WHOLE POINT. `color-mix(in srgb, X 36%, --p-hair)`
// blends X's RGB toward a second colour. `color-mix(in srgb, X 70%, transparent)` does NOT blend
// toward black: transparent is rgba(0,0,0,0), CSS interpolates in PREMULTIPLIED space, and
// un-premultiplying by the resulting alpha gives X's RGB back untouched with alpha 0.70. An
// implementation that interpolated un-premultiplied would return #A9700 9-ish mud for the
// attention glow instead of #B3F1A10D, and it would look plausible in a diff. One function has to
// get both right or the four values are four separate hand-transcriptions again.
//
// WHY EVERY EXPECTATION BELOW IS COMPUTED TWICE. The four hexes come from the design artifact, so
// asserting only that Mix reproduces them would pin a transcription with a second transcription.
// Each is therefore ALSO produced by mixLonghand/alphaLonghand -- deliberately separate integer
// implementations written from the CSS rule rather than from Mix -- and both routes must agree.
// A bug that lives in Mix alone fails the cross-check; a bug in the artifact's own numbers fails
// the pin.

import (
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// Two independent longhand implementations, used only to cross-check Mix.
// Integer arithmetic throughout, so they share no floating-point behaviour with
// the implementation under test.
// ---------------------------------------------------------------------------

// mixLonghand is one channel of `color-mix(in srgb, x pct%, y)` where BOTH inputs are opaque.
// With both alphas 1 the premultiplied and un-premultiplied forms coincide, so this is the plain
// weighted average, rounded half-up.
func mixLonghand(x, y int, pct int) uint8 {
	n := x*pct + y*(100-pct)
	return uint8((2*n + 100) / 200)
}

// alphaLonghand is the alpha of `color-mix(in srgb, x pct%, transparent)` for an opaque x:
// a = pct/100, serialised to 8 bits, rounded half-up.
func alphaLonghand(pct int) uint8 {
	return uint8((2*255*pct + 100) / 200)
}

// ---------------------------------------------------------------------------
// The requirement.
// ---------------------------------------------------------------------------

// TestPBTOK7_TheFourArtifactDerivationsAreComputedFromTheTokens is PB-TOK-7's first half:
// "every derived colour is produced by a single documented blend function over token inputs".
//
// Each case states the artifact's resolved value AND the longhand arithmetic. The test fails if
// Mix disagrees with either, so neither number is trusted on its own.
func TestPBTOK7_TheFourArtifactDerivationsAreComputedFromTheTokens(t *testing.T) {
	src := loadTokenSource(t)

	cases := []struct {
		name string
		// artifact is the value docs/research/remote-control-design-directions.html resolves
		// to, recorded so a change in Mix that silently moves a shipped colour is caught.
		artifact string
	}{
		{"attention-row-border", "#6D5220"},
		{"deny-fill", "#21FF6369"},
		{"needs-input-dot-glow", "#B3F1A10D"},
		{"working-dot-glow", "#8C00C2D7"},
	}

	byName := map[string]Derivation{}
	for _, d := range Derivations() {
		if _, dup := byName[d.Name]; dup {
			t.Fatalf("PB-TOK-7: two derivations are both named %q", d.Name)
		}
		byName[d.Name] = d
	}
	if len(byName) != len(cases) {
		t.Fatalf("PB-TOK-7: the artifact resolves %d colours with color-mix and Derivations() "+
			"returns %d; a derivation that is not in the table is one a consumer must transcribe",
			len(cases), len(byName))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := byName[tc.name]
			if !ok {
				t.Fatalf("PB-TOK-7: Derivations() has no entry named %q", tc.name)
			}
			got, err := d.Resolve(src.Tokens)
			if err != nil {
				t.Fatalf("PB-TOK-7: resolving %s: %v", d.Name, err)
			}
			if got.Hex() != tc.artifact {
				t.Errorf("PB-TOK-7: %s resolves to %s, the design artifact renders %s.\n"+
					"\tcolor-mix(in srgb, %s %d%%, %s) over the tokens in %s",
					d.Name, got.Hex(), tc.artifact, d.Base, d.Percent, d.Over, tokenSourcePath)
			}

			// The cross-check: the same value, from the CSS rule, by integer arithmetic that
			// shares no code with Mix.
			base, err := ParseColor(src.Tokens[d.Base])
			if err != nil {
				t.Fatalf("PB-TOK-7: token %s is not a colour: %v", d.Base, err)
			}
			var want RGBA
			if d.Over == Transparent {
				// Mixing with transparent scales ALPHA and leaves RGB alone.
				want = RGBA{R: base.R, G: base.G, B: base.B, A: alphaLonghand(d.Percent)}
			} else {
				over, err := ParseColor(src.Tokens[d.Over])
				if err != nil {
					t.Fatalf("PB-TOK-7: token %s is not a colour: %v", d.Over, err)
				}
				// Mixing with an opaque colour blends RGB and stays opaque.
				want = RGBA{
					R: mixLonghand(int(base.R), int(over.R), d.Percent),
					G: mixLonghand(int(base.G), int(over.G), d.Percent),
					B: mixLonghand(int(base.B), int(over.B), d.Percent),
					A: 255,
				}
			}
			if got != want {
				t.Errorf("PB-TOK-7: %s: Mix produced %s, the longhand CSS arithmetic produces %s. "+
					"Two implementations of one rule disagree, so at least one of them is wrong "+
					"and the artifact's %s cannot be trusted to come from the tokens.",
					d.Name, got.Hex(), want.Hex(), tc.artifact)
			}
		})
	}
}

// TestPBTOK7_MixingWithAColourBlendsRGBAndMixingWithTransparentScalesAlpha is the distinction
// that separates a correct implementation from a plausible one.
//
// The naive implementation treats `transparent` as opaque black and interpolates un-premultiplied.
// It gets the alpha right and the hue wrong, and the error is invisible in a code review because
// the result is still "a darker version of the token". This asserts the two behaviours directly
// rather than only through the four artifact values, so the reason a value is right is pinned and
// not just the value.
func TestPBTOK7_MixingWithAColourBlendsRGBAndMixingWithTransparentScalesAlpha(t *testing.T) {
	white := RGBA{R: 255, G: 255, B: 255, A: 255}
	black := RGBA{R: 0, G: 0, B: 0, A: 255}
	transparent, err := ParseColor(Transparent)
	if err != nil {
		t.Fatalf("ParseColor(%q): %v", Transparent, err)
	}
	if transparent.A != 0 {
		t.Fatalf("PB-TOK-7: %q parsed to alpha %d; CSS defines it as rgba(0,0,0,0)",
			Transparent, transparent.A)
	}

	// Over a colour: RGB moves, alpha stays opaque.
	overBlack := Mix(white, 0.50, black)
	if overBlack.A != 255 {
		t.Errorf("PB-TOK-7: mixing two opaque colours produced alpha %d, want 255", overBlack.A)
	}
	if overBlack.R == white.R {
		t.Errorf("PB-TOK-7: 50%% white over black left R at %d; mixing with a COLOUR must blend "+
			"RGB", overBlack.R)
	}

	// Over transparent: RGB is preserved exactly, alpha scales.
	overNothing := Mix(white, 0.50, transparent)
	if overNothing.R != white.R || overNothing.G != white.G || overNothing.B != white.B {
		t.Errorf("PB-TOK-7: 50%% white over transparent produced RGB (%d,%d,%d), want (255,255,255). "+
			"CSS interpolates color-mix in PREMULTIPLIED space: transparent contributes no colour, "+
			"only alpha. An implementation that blends toward transparent's nominal black darkens "+
			"every glow and tint in the design and still looks like a plausible colour.",
			overNothing.R, overNothing.G, overNothing.B)
	}
	if overNothing.A != 128 {
		t.Errorf("PB-TOK-7: 50%% over transparent produced alpha %d, want 128 (round(0.5*255))",
			overNothing.A)
	}
	if overNothing == overBlack {
		t.Error("PB-TOK-7: mixing with transparent and mixing with black produced the same colour, " +
			"so the implementation does not distinguish the two forms at all")
	}
}

// TestPBTOK7_TheBlendCanActuallyFail is the NEGATIVE CONTROL for every assertion above.
//
// "Every derived colour is produced by a single documented blend function" is satisfiable by a
// function that ignores its arguments and returns the four recorded hexes, and by one that
// returns its first argument unchanged. Both would pass the artifact comparison for at least some
// cases. So the blend is exercised on its own terms: the endpoints, the direction of travel, the
// symmetry the CSS rule requires, and a one-unit mutation of an input that must move the output.
func TestPBTOK7_TheBlendCanActuallyFail(t *testing.T) {
	x := RGBA{R: 241, G: 161, B: 13, A: 255} // --p-att
	y := RGBA{R: 35, G: 37, B: 42, A: 255}   // --p-hair

	if got := Mix(x, 1.0, y); got != x {
		t.Errorf("Mix at 100%% returned %s, want the first colour %s: a blend whose endpoint is "+
			"not an identity is not a blend", got.Hex(), x.Hex())
	}
	if got := Mix(x, 0.0, y); got != y {
		t.Errorf("Mix at 0%% returned %s, want the second colour %s", got.Hex(), y.Hex())
	}
	if Mix(x, 0.36, y) == x || Mix(x, 0.36, y) == y {
		t.Fatal("Mix at 36% returned one of its own inputs unchanged, so every derivation above " +
			"is vacuous: the function is not mixing anything")
	}
	// A change in an input must move the output. The probe is a two-unit change at 50%, which is
	// the smallest perturbation an 8-bit blend is REQUIRED to carry: at a weight of w a delta d
	// moves the result by w*d, so a one-unit delta at 36% moves it by 0.36 and is legitimately
	// absorbed by rounding. (This test asserted exactly that at first and failed against a
	// correct implementation -- the arithmetic, not the blend, was wrong.) Two units at 50% moves
	// it by a full unit, so only a blend that is discarding its inputs can fail here.
	flat := RGBA{R: 0, G: 0, B: 0, A: 255}
	nudged := RGBA{R: 0, G: 0, B: 2, A: 255}
	if Mix(x, 0.50, flat) == Mix(x, 0.50, nudged) {
		t.Error("Mix cannot distinguish #000000 from #000002 as the second colour; the blend is " +
			"discarding input precision and the derived values are not really derived")
	}
	// CSS's rule is symmetric: mix(x, p, y) == mix(y, 1-p, x). An implementation that had the
	// weights the wrong way round passes an equality against a single recorded hex only if that
	// hex was recorded from the same wrong implementation.
	if a, b := Mix(x, 0.36, y), Mix(y, 0.64, x); a != b {
		t.Errorf("Mix(x,36%%,y) = %s but Mix(y,64%%,x) = %s; color-mix is symmetric in its two "+
			"colours and this implementation is not", a.Hex(), b.Hex())
	}
	// And the weights are not swapped: 36% of the orange over the near-black hairline must land
	// nearer the hairline than the orange.
	got := Mix(x, 0.36, y)
	if dist(got, x) < dist(got, y) {
		t.Errorf("Mix(--p-att, 36%%, --p-hair) = %s sits nearer --p-att than --p-hair; the "+
			"percentage names the FIRST colour's share, so 36%% must land nearer the second",
			got.Hex())
	}
}

func dist(a, b RGBA) float64 {
	dr, dg, db := float64(a.R)-float64(b.R), float64(a.G)-float64(b.G), float64(a.B)-float64(b.B)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

// TestPBTOK7_ChangingABaseTokenMovesTheDerivedValue is PB-TOK-7's second criterion, verbatim.
//
// This is the property that makes the derivations real rather than decorative: if editing
// tokens.json does not move the derived colour, then the derivation is a constant wearing a
// function's clothes and the shipped value is still a transcription.
func TestPBTOK7_ChangingABaseTokenMovesTheDerivedValue(t *testing.T) {
	src := loadTokenSource(t)
	for _, d := range Derivations() {
		before, err := d.Resolve(src.Tokens)
		if err != nil {
			t.Fatalf("PB-TOK-7: resolving %s: %v", d.Name, err)
		}

		// A copy with exactly one base token perturbed by one unit of blue.
		moved := map[string]string{}
		for k, v := range src.Tokens {
			moved[k] = v
		}
		base, err := ParseColor(src.Tokens[d.Base])
		if err != nil {
			t.Fatalf("PB-TOK-7: token %s is not a colour: %v", d.Base, err)
		}
		base.B ^= 0x01
		moved[d.Base] = base.Hex()

		after, err := d.Resolve(moved)
		if err != nil {
			t.Fatalf("PB-TOK-7: resolving %s over the perturbed tokens: %v", d.Name, err)
		}
		if after == before {
			t.Errorf("PB-TOK-7: %s stayed %s after %s moved from %s to %s. The derived value does "+
				"not depend on the token it claims to be derived from, so it is a hard-coded "+
				"colour with a function wrapped round it.",
				d.Name, before.Hex(), d.Base, src.Tokens[d.Base], moved[d.Base])
		}
	}
}

// TestPBTOK7_EveryDerivationNamesRealTokens joins the table back to the origin.
//
// A derivation naming a token that tokens.json does not declare is the same defect as an
// unmapped colour resource in PB-TOK-1: a value that entered the design without passing through
// the single origin. Percentages are bounded here too, because a table is only reviewable if a
// nonsense row is rejected rather than silently clamped.
func TestPBTOK7_EveryDerivationNamesRealTokens(t *testing.T) {
	src := loadTokenSource(t)
	derivations := Derivations()
	if len(derivations) == 0 {
		t.Fatal("PB-TOK-7: Derivations() is empty; every assertion over it is vacuous")
	}
	for _, d := range derivations {
		if d.Site == "" {
			t.Errorf("PB-TOK-7: derivation %q does not say where the artifact uses it; an "+
				"unattributed derived colour cannot be reviewed against the design", d.Name)
		}
		if d.Percent <= 0 || d.Percent >= 100 {
			t.Errorf("PB-TOK-7: derivation %q mixes at %d%%; 0 and 100 are not mixes and anything "+
				"outside them is not a CSS percentage", d.Name, d.Percent)
		}
		v, ok := src.Tokens[d.Base]
		if !ok {
			t.Errorf("PB-TOK-7: derivation %q is based on %s, which %s does not declare",
				d.Name, d.Base, tokenSourcePath)
			continue
		}
		if _, err := ParseColor(v); err != nil {
			t.Errorf("PB-TOK-7: derivation %q is based on %s = %q, which is not a colour: %v",
				d.Name, d.Base, v, err)
		}
		if d.Over == Transparent {
			continue
		}
		w, ok := src.Tokens[d.Over]
		if !ok {
			t.Errorf("PB-TOK-7: derivation %q mixes over %s, which %s does not declare",
				d.Name, d.Over, tokenSourcePath)
			continue
		}
		if _, err := ParseColor(w); err != nil {
			t.Errorf("PB-TOK-7: derivation %q mixes over %s = %q, which is not a colour: %v",
				d.Name, d.Over, w, err)
		}
	}
}

// TestPBTOK7_TheColourCodecRoundTrips guards the two functions every assertion above is built on.
//
// Hex() is how a derived colour reaches an Android resource and how the gate recognises a
// transcription of one, so a serialiser that dropped alpha or folded case would make the
// literal-scan gate blind to exactly the values it exists to catch.
func TestPBTOK7_TheColourCodecRoundTrips(t *testing.T) {
	cases := []struct {
		in   string
		want RGBA
	}{
		{"#08090a", RGBA{R: 0x08, G: 0x09, B: 0x0A, A: 0xFF}},
		{"#08090A", RGBA{R: 0x08, G: 0x09, B: 0x0A, A: 0xFF}},
		{"#21FF6369", RGBA{R: 0xFF, G: 0x63, B: 0x69, A: 0x21}},
		{Transparent, RGBA{}},
	}
	for _, tc := range cases {
		got, err := ParseColor(tc.in)
		if err != nil {
			t.Errorf("ParseColor(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseColor(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}

	// Opaque serialises to six digits (the form tokens.json carries), anything else to eight in
	// #AARRGGBB order (the form an Android colour resource carries).
	if got := (RGBA{R: 0x6D, G: 0x52, B: 0x20, A: 0xFF}).Hex(); got != "#6D5220" {
		t.Errorf("Hex() of an opaque colour = %q, want %q", got, "#6D5220")
	}
	if got := (RGBA{R: 0xFF, G: 0x63, B: 0x69, A: 0x21}).Hex(); got != "#21FF6369" {
		t.Errorf("Hex() of a translucent colour = %q, want %q (alpha first, upper case)",
			got, "#21FF6369")
	}

	// rgba(8,9,10,0.88) is NOT in this list. It used to be, and that was the defect: the skin
	// writes --p-tabbg in rgba() notation, so a parser that refused it forced the token to be
	// typed `effect`, which in turn made PB-TOK-5's "all the colours reach the app" true by
	// construction. An audit committee found it. The parser reads the notation now; the
	// round-trip is asserted just below, and strictness is unchanged for everything else.
	if c, err := ParseColor("rgba(8,9,10,0.88)"); err != nil || c.Hex() != "#E008090A" {
		t.Errorf("ParseColor(rgba(8,9,10,0.88)) = %v, %v; want #E008090A", c.Hex(), err)
	}
	for _, bad := range []string{"", "#fff", "0x08090a", "#08090", "rgba(8,9,10)", "rgba(8,9,300,0.5)", "#zzzzzz"} {
		if c, err := ParseColor(bad); err == nil {
			t.Errorf("ParseColor(%q) accepted the value and returned %s; a lenient colour parser "+
				"is how a non-colour token gets a colour conversion invented for it", bad, c.Hex())
		}
	}

	// Round trip, so the two halves cannot drift apart.
	for _, s := range []string{"#08090A", "#21FF6369", "#B3F1A10D", "#8C00C2D7"} {
		c, err := ParseColor(s)
		if err != nil {
			t.Fatalf("ParseColor(%q): %v", s, err)
		}
		if got := c.Hex(); got != s {
			t.Errorf("ParseColor -> Hex round trip: %q became %q", s, got)
		}
	}
}
