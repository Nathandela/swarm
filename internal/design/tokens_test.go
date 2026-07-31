// Package design pins the shared design-token contract for the Phase B
// Android theme (PB-TOK-1..3, docs/specifications/remote-phaseB-requirements.md
// section 6.13, scoped by the section 5 decision: one skin, fixed dark theme,
// light mode deferred to Phase C).
//
// Contract these tests pin: internal/design/tokens.json is the single
// machine-readable origin of truth for the theme. Its schema is:
//
//	{
//	  "schema": 1,
//	  "source": "docs/research/remote-control-design-directions.html",
//	  "skin": "substrate",                  // pinned; switching to void needs a spec/ADR change
//	  "mode": "dark",                       // light mode is deferred to Phase C
//	  "tokens": {
//	    "--p-bg": "#08090a",
//	    ...                                 // every --p-* token the chosen skin
//	  },                                    // defines in the design HTML, verbatim
//	  "terminal_peek": {
//	    "fg": "--p-hero",                   // token refs, not duplicated values:
//	    "font": "--p-mono"                  // fg must be --p-hero (the phosphor
//	  }                                     // green), font a monospace stack
//	}
//
// Token names keep the full CSS custom-property name so the drift check
// against the design HTML is an exact string comparison (values compared
// whitespace-normalized). Unknown JSON fields and trailing data are rejected.
package design

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	tokenSourcePath = "tokens.json"
	designHTMLPath  = "../../docs/research/remote-control-design-directions.html"
	designHTMLRef   = "docs/research/remote-control-design-directions.html"
	// htmlTokenCount is the verified count from requirements section 6.13:
	// each direction block in the design HTML defines 31 distinct --p-* tokens.
	htmlTokenCount = 31
)

// skinClass maps the recorded skin name to its CSS class in the design HTML.
// Only the two retained directions are valid: d3 Signal (purple) and
// d4 Instrument are marked "Not retained" in the artifact.
var skinClass = map[string]string{
	"substrate": "d1",
	"void":      "d2",
}

type tokenSource struct {
	Schema int               `json:"schema"`
	Source string            `json:"source"`
	Skin   string            `json:"skin"`
	Mode   string            `json:"mode"`
	Tokens map[string]string `json:"tokens"`
	// Kinds is PB-TOK-6's addition: what each token IS, stated by the origin instead of
	// guessed from the shape of its value by whoever happens to be reading it. See
	// TestPBTOK6_EveryTokenDeclaresAKindAndTheKindMatchesTheValue for why it is a sibling
	// object rather than a field on each token -- the "tokens" object stays a flat
	// name -> value map, so the drift check above and the Kotlin reader that both already
	// consume it are untouched.
	Kinds        map[string]string `json:"kinds"`
	TerminalPeek terminalPeek      `json:"terminal_peek"`
}

type terminalPeek struct {
	Fg   string `json:"fg"`
	Font string `json:"font"`
}

func loadTokenSource(t *testing.T) tokenSource {
	t.Helper()
	data, err := os.ReadFile(tokenSourcePath)
	if err != nil {
		t.Fatalf("PB-TOK-1: token source internal/design/%s must exist and be checked in: %v", tokenSourcePath, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var src tokenSource
	if err := dec.Decode(&src); err != nil {
		t.Fatalf("PB-TOK-1: token source does not match the pinned schema: %v", err)
	}
	if dec.More() {
		t.Fatalf("PB-TOK-1: token source carries data after the JSON document")
	}
	return src
}

var declRe = regexp.MustCompile(`(--p-[a-z0-9-]+)\s*:\s*([^;]+);`)

// extractSkinTokens reads the design HTML and returns every --p-* declaration
// of the given direction block (".dN { ... }"), values whitespace-normalized.
func extractSkinTokens(t *testing.T, class string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(designHTMLPath)
	if err != nil {
		t.Fatalf("design HTML %s not readable: %v", designHTMLRef, err)
	}
	blockRe := regexp.MustCompile(`(?s)\.` + class + `\s*\{(.*?)\}`)
	m := blockRe.FindSubmatch(data)
	if m == nil {
		t.Fatalf("design HTML defines no .%s token block", class)
	}
	tokens := make(map[string]string)
	for _, d := range declRe.FindAllStringSubmatch(string(m[1]), -1) {
		tokens[d[1]] = normalize(d[2])
	}
	if len(tokens) != htmlTokenCount {
		t.Fatalf("extractor sanity: .%s defines %d --p-* tokens, requirements section 6.13 verified %d", class, len(tokens), htmlTokenCount)
	}
	return tokens
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// PB-TOK-1: a single machine-readable token source exists and matches the schema.
func TestTokenSourceExistsAndMatchesSchema(t *testing.T) {
	src := loadTokenSource(t)
	if src.Schema != 1 {
		t.Errorf("PB-TOK-1: schema must be 1, got %d", src.Schema)
	}
	if src.Source != designHTMLRef {
		t.Errorf("PB-TOK-1: source must reference %q, got %q", designHTMLRef, src.Source)
	}
	if len(src.Tokens) == 0 {
		t.Error("PB-TOK-1: tokens must not be empty")
	}
	for name := range src.Tokens {
		if !strings.HasPrefix(name, "--p-") {
			t.Errorf("PB-TOK-1: token %q is not a --p-* product token", name)
		}
	}
}

// PB-TOK-1 (no drift) and PB-TOK-2 (completeness): the JSON carries exactly
// the tokens the chosen skin defines in the design HTML, with equal values.
func TestTokenSourceMatchesChosenSkinInDesignHTML(t *testing.T) {
	// Exercise the extractor against both retained skins first, so any
	// failure below can only mean the JSON is wrong, not the extractor.
	for _, class := range skinClass {
		extractSkinTokens(t, class)
	}

	src := loadTokenSource(t)
	class, ok := skinClass[src.Skin]
	if !ok {
		t.Fatalf("PB-TOK-2: recorded skin %q is not a retained direction (want substrate or void)", src.Skin)
	}
	want := extractSkinTokens(t, class)

	for name, wantVal := range want {
		gotVal, ok := src.Tokens[name]
		if !ok {
			t.Errorf("PB-TOK-2: token %s defined by skin %q in the design HTML is missing from tokens.json", name, src.Skin)
			continue
		}
		if normalize(gotVal) != wantVal {
			t.Errorf("PB-TOK-1: token %s drifted: json %q, html %q", name, gotVal, wantVal)
		}
	}
	for name := range src.Tokens {
		if _, ok := want[name]; !ok {
			t.Errorf("PB-TOK-1: token %s in tokens.json is not defined by skin %q in the design HTML", name, src.Skin)
		}
	}
}

// PB-TOK-2: the chosen skin is Substrate (d1), and the theme is pinned dark
// (light mode is deferred to Phase C per requirements section 5). Void (d2)
// remains a legal future choice, but switching to it requires a spec/ADR
// change, not a silent edit here.
func TestChosenSkinIsSubstrateAndPinnedDark(t *testing.T) {
	src := loadTokenSource(t)
	if src.Skin != "substrate" {
		t.Errorf("PB-TOK-2: skin must be \"substrate\", got %q", src.Skin)
	}
	if src.Mode != "dark" {
		t.Errorf("PB-TOK-2: mode must be pinned to \"dark\", got %q", src.Mode)
	}
}

// PB-TOK-3: the terminal peek keeps the phosphor-green monospace treatment.
// The foreground must be --p-hero itself: in both retained skins --p-hero IS
// the phosphor green, and pinning the ref forbids every mis-wiring a hue
// classifier would let through (near-black inks like --p-hero-ink, off-greens
// like --p-ok). Purple needs no separate check: the drift test enforces exact
// HTML<->JSON equality and skinClass admits only d1/d2, so the retired purple
// direction (d3) cannot enter the token set without failing those tests --
// do not reintroduce an HSV classifier for it.
func TestTerminalPeekIsPhosphorGreenMonospace(t *testing.T) {
	src := loadTokenSource(t)

	if src.TerminalPeek.Fg != "--p-hero" {
		t.Errorf("PB-TOK-3: terminal_peek.fg must be \"--p-hero\", got %q", src.TerminalPeek.Fg)
	}
	if _, ok := src.Tokens[src.TerminalPeek.Fg]; !ok {
		t.Fatalf("PB-TOK-3: terminal_peek.fg %q does not name a token in the set", src.TerminalPeek.Fg)
	}

	fontVal, ok := src.Tokens[src.TerminalPeek.Font]
	if !ok {
		t.Fatalf("PB-TOK-3: terminal_peek.font %q does not name a token in the set", src.TerminalPeek.Font)
	}
	if !strings.Contains(strings.ToLower(fontVal), "monospace") {
		t.Errorf("PB-TOK-3: terminal peek font %s=%q must be a monospace stack", src.TerminalPeek.Font, fontVal)
	}
}

// ---------------------------------------------------------------------------
// PB-TOK-6 -- the typed conversion path.
// ---------------------------------------------------------------------------

// FAILING-FIRST (TDD RED, GG-5) for PB-TOK-6.
//
// "The non-colour tokens get a typed conversion path. android/gate/s16_tokens_test.go and
//  DesignTokens.kt currently REFUSE any token without an ARGB form rather than inventing a
//  conversion -- correct when nothing consumed them, and the reason 15 of 31 tokens (5 radii,
//  5 typographic, 5 effects) can never ship. The refusal is replaced by per-kind converters."
//
// THE STATE OF THE WORLD, verified before these assertions were written: tokens.json is a flat
// name -> value map and nothing anywhere says what a token IS. Both readers infer it from the
// value: the Go gate calls strings.HasPrefix(v, "#") and the Kotlin reader matches a hex regex,
// and both then treat "not a colour" as "not convertible", full stop.
//
// INFERENCE FROM THE VALUE IS THE DEFECT, not just an inelegance. `--p-grain: 0.05` and
// `--p-display-wt: 650` are both bare numbers and mean nothing alike. `--p-tabbg:
// rgba(8,9,10,0.88)` IS a colour and fails the hex sniff, so a value-sniffing reader would
// quietly file the tab bar's background under "no conversion" forever. And the inference is
// silent in both directions: nothing today would notice a font stack acquiring a hex-looking
// value, or a colour being retyped as a gradient.
//
// So the origin states the kind, and these tests join the two in BOTH directions: every token
// has a kind, every kind is one of the six the requirement names, and -- the part that gives it
// teeth -- the value must have the SHAPE its kind implies and must not have the shape of any
// other kind. A colour mislabelled `effect` to dodge a converter fails here.

// tokenKinds are the six PB-TOK-6 names, and the list is closed: a seventh kind is a decision
// about how the design reaches the platform, so it belongs in a spec change and not in a JSON
// edit that nothing objects to.
var tokenKinds = []string{"color", "dimen", "font", "weight", "tracking", "effect"}

// colourTokenCount is PB-TOK-5's number: the Substrate skin declares 16 hex colours out of its
// 31 tokens. Pinned here so that retyping a colour as something else -- the cheapest way to make
// a stubborn token stop failing a converter -- shows up as a count that no longer matches the
// requirement rather than as a green run.
const colourTokenCount = 16

var (
	dimenRe    = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?px$`)
	trackingRe = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?em$`)
	weightRe   = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)
)

// genericFontFamilies are the CSS generic families. A font stack must end in one, which is what
// makes it a stack rather than a single unavailable face: ADR-007 B134 decides the app renders
// the platform families, and the generic tail is the part that survives that decision.
var genericFontFamilies = map[string]bool{
	"serif": true, "sans-serif": true, "monospace": true, "cursive": true,
	"fantasy": true, "system-ui": true, "ui-serif": true, "ui-sans-serif": true,
	"ui-monospace": true, "ui-rounded": true,
}

// shapeOf reports which kinds a value could plausibly be, judged from the value alone. It is the
// inverse of the declared kind and exists only to contradict it: a value that matches no kind is
// unclassifiable, and one that matches a kind other than its own is mislabelled.
func shapeOf(value string) []string {
	v := strings.TrimSpace(value)
	var kinds []string
	if _, err := ParseColor(v); err == nil {
		kinds = append(kinds, "color")
	}
	if dimenRe.MatchString(v) {
		kinds = append(kinds, "dimen")
	}
	if trackingRe.MatchString(v) {
		kinds = append(kinds, "tracking")
	}
	if weightRe.MatchString(v) {
		kinds = append(kinds, "weight")
	}
	if fields := strings.Split(v, ","); len(fields) > 1 {
		last := strings.Trim(strings.TrimSpace(fields[len(fields)-1]), `"'`)
		if genericFontFamilies[last] {
			kinds = append(kinds, "font")
		}
	}
	return kinds
}

// TestPBTOK6_EveryTokenDeclaresAKindAndTheKindMatchesTheValue is PB-TOK-6's first criterion.
func TestPBTOK6_EveryTokenDeclaresAKindAndTheKindMatchesTheValue(t *testing.T) {
	src := loadTokenSource(t)
	if len(src.Kinds) == 0 {
		t.Fatalf("PB-TOK-6: %s declares no \"kinds\" object. Every reader today infers a token's "+
			"kind from the shape of its value, which files rgba(8,9,10,0.88) under \"not a colour\" "+
			"and cannot tell 0.05 from 650. The origin must say what a token IS.", tokenSourcePath)
	}

	known := map[string]bool{}
	for _, k := range tokenKinds {
		known[k] = true
	}

	// Both directions, because a kinds map that covers half the tokens is worse than none: the
	// converters would silently skip whatever it omits, which is the behaviour being replaced.
	for name := range src.Tokens {
		if _, ok := src.Kinds[name]; !ok {
			t.Errorf("PB-TOK-6: token %s has no kind. A token whose kind is unstated is one whose "+
				"converter is chosen by guessing at its value.", name)
		}
	}
	for name := range src.Kinds {
		if _, ok := src.Tokens[name]; !ok {
			t.Errorf("PB-TOK-6: \"kinds\" declares %s, which is not a token", name)
		}
	}

	byKind := map[string]int{}
	for name, kind := range src.Kinds {
		value, ok := src.Tokens[name]
		if !ok {
			continue
		}
		if !known[kind] {
			t.Errorf("PB-TOK-6: token %s has kind %q; the six kinds are %s",
				name, kind, strings.Join(tokenKinds, ", "))
			continue
		}
		byKind[kind]++

		shapes := shapeOf(value)
		matches := false
		for _, s := range shapes {
			if s == kind {
				matches = true
			}
		}
		switch {
		case kind == "effect" && len(shapes) > 0:
			// `effect` is the kind with no converter, so it is the one a stuck token would be
			// retyped INTO. It must therefore be the residual and nothing else: a value that
			// looks like a colour, a radius, a weight or a tracking is not an effect.
			t.Errorf("PB-TOK-6: token %s = %q is typed \"effect\" but its value is a valid %s. "+
				"\"effect\" is the kind with no converter; a token parked there because a "+
				"converter was inconvenient is exactly the silent skip PB-TOK-6 removes.",
				name, value, strings.Join(shapes, "/"))
		case kind == "effect":
			// Correct: an effect is a value no converter shape claims.
		case !matches:
			t.Errorf("PB-TOK-6: token %s = %q is typed %q, but that value has the shape of %s. "+
				"The declared kind and the value disagree, so whichever converter runs will "+
				"produce a resource nobody designed.",
				name, value, kind, describeShapes(shapes))
		}
	}

	if byKind["color"] != colourTokenCount {
		t.Errorf("PB-TOK-6/PB-TOK-5: %d tokens are typed \"color\"; the Substrate skin declares %d. "+
			"PB-TOK-5's whole scope is those %d reaching the app, so a different count means "+
			"either a colour has been retyped or the skin changed without a spec change.",
			byKind["color"], colourTokenCount, colourTokenCount)
	}
	if total := len(src.Tokens); total != htmlTokenCount {
		t.Errorf("PB-TOK-6: %d tokens, requirements section 6.13 verified %d", total, htmlTokenCount)
	}
}

func describeShapes(shapes []string) string {
	if len(shapes) == 0 {
		return "no kind at all"
	}
	return strings.Join(shapes, "/")
}

// TestPBTOK6_TheKindCheckCanActuallyFail is the NEGATIVE CONTROL.
//
// The assertions above are satisfiable by a shapeOf that returns every kind for every value
// (nothing is ever mislabelled) or the empty slice for every value (nothing is ever checked,
// and only the `effect` branch stays live). Both would be green on the real token set. So the
// classifier is exercised against values chosen to be each other's near misses.
func TestPBTOK6_TheKindCheckCanActuallyFail(t *testing.T) {
	cases := []struct {
		value string
		want  []string
	}{
		{"#08090a", []string{"color"}},
		{"#21FF6369", []string{"color"}},
		{"9px", []string{"dimen"}},
		{"-0.025em", []string{"tracking"}},
		{"650", []string{"weight"}},
		{`-apple-system, BlinkMacSystemFont, "SF Pro Text", sans-serif`, []string{"font"}},
		{`ui-monospace, "SF Mono", Menlo, monospace`, []string{"font"}},
		// The five real effect values: each must be classified as nothing, because each has to
		// fall through to the kind that has no converter.
		{"inset 0 1px 0 rgba(255,255,255,0.045)", nil},
		{"0 0 18px rgba(83,206,124,0.20)", nil},
		{"rgba(8,9,10,0.88)", nil},
		{"0.05", nil},
		{"linear-gradient(90deg, #00c2d7, transparent 85%)", nil},
		// Near misses that a sloppy classifier folds together.
		{"9", []string{"weight"}}, // a radius that lost its unit is not a radius
		{"0.9px", []string{"dimen"}},
		{"#fff", nil},       // the three-digit shorthand is refused, not expanded
		{"sans-serif", nil}, // one family is not a stack
		{"9dp", nil},        // Android's unit is an output, never an input
	}
	for _, tc := range cases {
		got := shapeOf(tc.value)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("shapeOf(%q) = %v, want %v; a classifier that cannot separate these cannot "+
				"contradict a mislabelled kind, and every kind assertion above is vacuous",
				tc.value, got, tc.want)
		}
	}
	// And the classifier must not be constant.
	if a, b := shapeOf("#08090a"), shapeOf("9px"); strings.Join(a, ",") == strings.Join(b, ",") {
		t.Fatal("shapeOf returns the same answer for a colour and a radius")
	}
}
