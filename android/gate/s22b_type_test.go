package gate

// FAILING-FIRST (TDD RED, GG-5) for PB-DS-2 (typography scale) and PB-DS-3 (font substitution),
// S22b.
//
// PB-DS-2: "18 distinct product text styles are in use across 12 sizes, 4 weights and 5 tracking
// values; the app currently expresses zero of them. Each style is one named TextAppearance
// carrying size, weight, tracking, family, line-height and colour token -- not six attributes
// re-specified per call site." Criterion: "a gate joins the style set to the recorded scale
// BIDIRECTIONALLY (an unlisted style fails, an unimplemented row fails)."
//
// PB-DS-3: "Decision: the platform families -- sans-serif and monospace, zero bundled assets."
// Criterion: "A gate asserts the mono style's family is the one recorded."
//
// ADR-009 D7 (phase O5) MOVED HALF OF PB-DS-3's DECISION and left the other half alone. The sans
// half is unchanged: platform `sans-serif`, no bundled display face. The MONO half is taken --
// JetBrains Mono is bundled, because the residual B134 recorded and MonoBoxDrawingTest measured
// (box drawing resolving through fallback at 0.71em against the family's own 0.60em, an 18%
// mismatch in the one place the app draws a frame) has no other fix. So this file now carries two
// records rather than one, and `zero bundled assets` survives as a claim about the SANS family
// only. See s22bFontRecord.
//
// THE STATE OF THE WORLD, verified before these assertions were written:
//
//	res/values/                                colors.xml, strings.xml, themes.xml -- no type.xml
//	production Kotlin, all 1582 lines           one Typeface.MONOSPACE, one Typeface.BOLD
//	                                            zero setTextSize, zero setLetterSpacing
//
// COLOUR IS DELIBERATELY NOT IN THESE STYLES, and that is a scoping decision rather than an
// omission. PB-DS-2's sentence lists "colour token" among the six attributes; the same style is
// used in several colours (Label.Button is --p-cta-ink on the hero variant, --p-err on the deny
// variant and --p-ink on the tertiary one -- one CSS rule, three colours), so a TextAppearance
// that carried one would be wrong at two of its three call sites. Colour is applied by the
// component kit (PB-DS-6, S23). TestPBDS2_NoTextStyleCarriesAColour makes that a checked
// property rather than a habit.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// PB-DS-3: the recorded substitution.
// ---------------------------------------------------------------------------

// s22bFontSubstitution is the recorded substitution, as a map: what Android renders for a token
// whose CSS stack names a face the platform cannot supply.
//
// IT IS A CONSTANT HERE AND IT HAS TO BE. Unlike every other number in this slice, this one
// cannot be computed from the artifact -- the artifact names fonts that do not exist on the
// target platform, which is the whole reason a decision was required. So the constant is the
// DECISION, and TestPBDS3_TheSubstitutionIsTheOneTheADRRecords joins each row back to the ADR
// that records it, so that changing it here without changing the record fails.
//
// AUTHORIZED REWRITE, ADR-009 D8.3 / D7 (phase O5). What this table said before, quoted so the
// pin's move is visible rather than inferred:
//
//	var s22bFontSubstitution = map[string]string{
//		"--p-font": "sans-serif",
//		"--p-mono": "monospace",
//	}
//
// The sans row does not move. The mono row does: `monospace` is Droid Sans Mono, which does not
// cover U+2500-257F, and the app draws terminal frames out of that block. ADR-007 B134 itself set
// the condition for taking the upgrade ("until the peek is seen to need it"), recorded that the
// condition was met, and deferred the asset-weight decision to whoever owned the peek's screen;
// ADR-009 D7 is that decision, and O5 is where it lands.
//
// THE TOKEN VALUE IN tokens.json DOES NOT MOVE, and the reason is worth stating because ADR-009
// D3's row for `--p-mono` predicted it would ("O5 prepends bundled JetBrains Mono"). The maquette
// is the normative design source (D2) and internal/design/tokens_test.go joins tokens.json to it
// in BOTH directions with no exception mechanism, so prepending a family name to the JSON without
// editing the owner-signed maquette is a drift failure, and editing the maquette to satisfy a
// gate is the tail wagging the dog. The token states the design's intent (a mono stack); THIS
// TABLE is the layer that says what Android renders for it, which is precisely the layer PB-DS-3
// exists to be. ADR-009's D7 amendment records the same reasoning.
var s22bFontSubstitution = map[string]string{
	"--p-font": "sans-serif",
	"--p-mono": "@font/jetbrains_mono",
}

// s22bMonoFontFeatures is ADR-009 D7's other half: "`tnum` + `zero` + `calt` enabled wherever
// machine data renders".
//
// WHERE MACHINE DATA RENDERS IS THE MONO FAMILY, and that is not a paraphrase -- it is what the
// design already says. Every rule the artifact sets in `var(--p-mono)` is a machine's own
// register: the LIVE counter, the section labels, the agent name, the command line, the binding
// line, the toast's body, the terminal peek. Nothing proportional in this app renders a number
// the machine produced. So the rule is exactly "mono styles carry the features, sans styles do
// not", asserted in both directions in s22bStyleFaults.
//
// WHY EACH FEATURE. `tnum` gives digits one advance, so a counter that ticks 9 -> 10 does not
// reflow the line it sits in; `zero` slashes the zero, which is the one glyph pair a person
// reading a session id or a hash has to disambiguate; `calt` is ON BY DEFAULT and is stated
// anyway, because Android's fontFeatureSettings is a full override -- naming two features
// without it would silently DISABLE the contextual alternates the family ships, which in
// JetBrains Mono is what keeps `->` and `!=` legible.
const s22bMonoFontFeatures = "tnum, zero, calt"

// s22bFontDecision is one row of the substitution table, joined to the record that decides it.
type s22bFontDecision struct {
	ADR    string // repo-relative path to the ADR of record
	Anchor string // the heading the decision lives under, so the search is scoped to it
	Wants  []string
}

// s22bFontRecord is where each half of the substitution is written down. Two ADRs, because
// ADR-009 D7 supersedes ADR-007 B134 for the mono family and leaves the sans family standing;
// a single-record join would have to pick one and would then be reading a superseded decision or
// a decision that never mentioned sans.
var s22bFontRecord = map[string]s22bFontDecision{
	"--p-font": {
		ADR:    "docs/adr/ADR-007-remote-access.md",
		Anchor: "## B134.",
		Wants:  []string{"zero bundled assets"},
	},
	"--p-mono": {
		ADR:    "docs/adr/ADR-009-obsidian-visual-direction.md",
		Anchor: "### D7.",
		Wants: []string{
			"`JetBrainsMono-Regular.ttf`",
			"`JetBrainsMono-Medium.ttf`",
			"`" + s22bMonoFontFeatures + "`",
		},
	},
}

// s22bBundledFonts is the exact set of font files ADR-009 D7 authorizes, repo-relative.
//
// TWO FACES AND NOT A VARIABLE FONT. The type scale asks for 400, 500 and 600 on the mono
// family; a two-file static pair covers 400 and 500 exactly and resolves 600 to the nearest, and
// the variable `JetBrainsMono[wght].ttf` would cost 60% more bytes to serve one weight nobody
// asked for at full fidelity. Recorded in D7 so the count is a decision rather than whatever was
// convenient.
var s22bBundledFonts = map[string]string{
	"android/app/src/main/res/font/jetbrains_mono.xml":         "the family: which face answers which weight",
	"android/app/src/main/res/font/jetbrains_mono_regular.ttf": "JetBrains Mono Regular, weight 400",
	"android/app/src/main/res/font/jetbrains_mono_medium.ttf":  "JetBrains Mono Medium, weight 500",
}

// s22bFontLicence is where OFL-1.1's own condition lands: the licence travels with the font.
const s22bFontLicence = "docs/design/fonts/JetBrainsMono-OFL.txt"

// s22bDocChromeSelectors are rules in the shared block that style the DOCUMENTATION, not the
// product, and therefore get no TextAppearance.
//
// There is exactly one, and it is not excluded on assertion: `.panelframe .cap` is the little
// uppercase caption above each loose panel in the directions artifact -- and it names
// `var(--mono)`, a variable NO skin declares, which is the evidence that it was never wired into
// the product's token set. TestPBDS2_TheDocChromeExclusionIsStillTrue checks that evidence
// rather than trusting this list, so the day someone fixes the typo the exclusion has to be
// re-argued instead of silently swallowing a 19th style.
var s22bDocChromeSelectors = map[string]string{
	".panelframe .cap": "documentation chrome: the panel caption, which names the undeclared " +
		"var(--mono) and appears on no product screen",
}

// s22bUnimplementedRule is a PRODUCT rule the design draws and this app deliberately does not
// implement. It is a third thing, and the two that already exist could not express it.
//
// It is not doc chrome: `.ptime` and `.tcard .h` style the product, they parse cleanly against the
// token origin, and TestPBDS2_TheDocChromeExclusionIsStillTrue would fail them on exactly that
// evidence. It is not a derived style either -- that class is for a style the ARTIFACT never drew,
// and these are rules the artifact draws that the APP does not spend.
//
// SO THE ENTRY IS A RECORD RATHER THAN AN EXCLUSION, and the difference is where the authority
// lives. A selector listed here names the ADR that decided not to implement it and the anchor that
// decision lives under; TestPBDS2_TheUnimplementedRulesAreTheOnesTheRecordDecides follows the
// citation and fails when the decision is not there, when the rule has left the design source, or
// -- for a merge -- when the two rules stop rendering identically. A list nobody could disagree
// with would be a hole in the join with a comment over it.
type s22bUnimplementedRule struct {
	Why        string
	MergedInto string   // the selector whose style renders it instead; "" when nothing does
	ADR        string   // repo-relative path to the record
	Anchor     string   // the heading the decision lives under, so the search is scoped to it
	Wants      []string // phrases the decision must contain, beyond the selector itself
}

// s22bTypeConsolidationADR is ADR-012, which is where a rule stops being implemented.
const s22bTypeConsolidationADR = "docs/adr/ADR-012-type-ladder-consolidation-phase-1.md"

// s22bUnimplementedRules is that list, and it has exactly two entries (ADR-012, 2026-08-09).
//
// `.tcard .h` IS A MERGE AND `.ptime` IS A DELETION, which is why the struct carries MergedInto
// rather than a flag. The merge is the one that needs asserting: it is only safe while the design
// keeps the two rules on the same numbers AND the bundled family keeps resolving both declared
// weights to one face, and neither of those is this app's to guarantee.
var s22bUnimplementedRules = map[string]s22bUnimplementedRule{
	".tcard .h": {
		Why: "10.5/600 mono, which ADR-009 D7's two-face bundle resolves to the 500 face -- the " +
			"same pixels as `.sheet2 .ctx`, whose style survives the merge",
		MergedInto: ".sheet2 .ctx",
		ADR:        s22bTypeConsolidationADR,
		Anchor:     "### T1.",
		Wants:      []string{"`Label.CardHead`", "`Mono.Meta`"},
	},
	".ptime": {
		Why: "the mock's simulated iOS status-bar clock. The system draws the status bar and this " +
			"app never does, so the style had zero call sites from the day it landed",
		ADR:    s22bTypeConsolidationADR,
		Anchor: "### T2.",
		Wants:  []string{"`Label.StatusBar`"},
	},
}

// s22bFontFamilyWeightRe reads the weights a bundled family answers, out of the family resource.
var s22bFontFamilyWeightRe = regexp.MustCompile(`android:fontWeight="([0-9]+)"`)

// s22bBundledFaceWeights is which weights the mono family actually ships, read from
// res/font/jetbrains_mono.xml rather than written down here.
//
// THE MERGE'S PREMISE IS THIS FILE'S CONTENT. `.tcard .h` asks for 600 and `.sheet2 .ctx` asks for
// 500; they render the same glyphs because the family answers 400 and 500 and Android picks the
// nearest. Add a 600 face -- which ADR-009 D7 priced at 273 KB and declined -- and the two rules
// stop being the same pixels, the merge stops being safe, and this gate has to say so.
func s22bBundledFaceWeights(t *testing.T) []int {
	t.Helper()
	family := s22bFontSubstitution["--p-mono"]
	name := strings.TrimPrefix(family, "@font/")
	if name == family {
		t.Fatalf("PB-DS-3: the mono substitution is %q, which is not a bundled `@font/` resource, "+
			"so there is no family table to resolve a declared weight through", family)
	}
	path := filepath.Join(appModule(t), "src", "main", "res", "font", name+".xml")
	raw := readFileOrFail(t, path, "ADR-009 D7")

	var out []int
	for _, m := range s22bFontFamilyWeightRe.FindAllStringSubmatch(raw, -1) {
		w, err := strconv.Atoi(m[1])
		if err != nil {
			t.Errorf("ADR-009 D7: %s declares android:fontWeight=%q, which is not a number",
				mustRel(t, path), m[1])
			continue
		}
		out = append(out, w)
	}
	if len(out) == 0 {
		t.Fatalf("ADR-009 D7: %s declares no android:fontWeight at all. Every weight would then "+
			"resolve to nothing and the render-equality comparison below would compare two "+
			"absences and agree.", mustRel(t, path))
	}
	sort.Ints(out)
	return out
}

// s22bResolvedFace is the face a declared weight actually renders in, given the weights a family
// ships: the nearest one, ties to the lighter, which is Android's own rule.
func s22bResolvedFace(want int, faces []int) int {
	best := faces[0]
	for _, face := range faces[1:] {
		switch d, bd := abs(face-want), abs(best-want); {
		case d < bd, d == bd && face < best:
			best = face
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// s22bRenderDifferences reports every way two design rules do NOT render identically, given the
// faces the family ships. Empty means a style for one of them is a style for both.
//
// IT IS A FUNCTION RETURNING VALUES so the negative control can feed it perturbed pairs through the
// same comparison the real assertion calls -- the rule the rest of this file already follows.
//
// WEIGHT IS COMPARED AFTER RESOLUTION, and that is the entire point: 600 and 500 are different
// declarations and the same pixels on a family that ships no 600. On a family that ships one they
// are different pixels, and this is where that difference arrives.
func s22bRenderDifferences(a, b s22bTypeSpec, faces []int) []string {
	var diffs []string
	if a.SizePx != b.SizePx {
		diffs = append(diffs, fmt.Sprintf("size %gpx against %gpx", a.SizePx, b.SizePx))
	}
	if math.Abs(a.TrackingEm-b.TrackingEm) > 1e-9 {
		diffs = append(diffs, fmt.Sprintf("tracking %gem against %gem", a.TrackingEm, b.TrackingEm))
	}
	if a.Family != b.Family {
		diffs = append(diffs, fmt.Sprintf("family %s against %s", a.Family, b.Family))
	}
	if a.LineHeight != b.LineHeight {
		diffs = append(diffs, fmt.Sprintf("line-height %g against %g", a.LineHeight, b.LineHeight))
	}
	if fa, fb := s22bResolvedFace(a.Weight, faces), s22bResolvedFace(b.Weight, faces); fa != fb {
		diffs = append(diffs, fmt.Sprintf("weight %d resolves to the %d face and weight %d to the "+
			"%d face (the family ships %v)", a.Weight, fa, b.Weight, fb, faces))
	}
	return diffs
}

// ---------------------------------------------------------------------------
// The design's own typography, read out of the CSS.
// ---------------------------------------------------------------------------

// s22bTypeSpec is one CSS rule's resolved text properties.
type s22bTypeSpec struct {
	Selector   string
	SizePx     float64
	Weight     int
	TrackingEm float64
	Family     string  // the --p-* token name, never the stack
	LineHeight float64 // 0 when the rule declares none
}

// s22bReadTypeSpec resolves one rule's typography, inheriting what CSS inherits.
//
// FAMILY INHERITANCE IS REAL AND IS MODELLED. `.prow .pj`, `.pnav .big`, `.m2` and `.sheet2 h4`
// declare no font-family; they inherit it from `.pscreen` and `.panelframe`, which BOTH declare
// `font-family: var(--p-font)`. Treating "no family declared" as "no family" would leave four
// styles with nothing to assert and would quietly accept a mono display title.
//
// WEIGHT DEFAULTS TO 400 and tracking to 0, which is what a browser does with `normal`. Android
// agrees on both: textFontWeight's default is 400 and letterSpacing's is 0.
func s22bReadTypeSpec(sel string, rule s22bCSSRule, tokens map[string]string) (s22bTypeSpec, error) {
	spec := s22bTypeSpec{Selector: sel, Weight: 400, Family: "--p-font"}
	seenSize := false

	set := func(prop, value string) error {
		resolved, err := s22bResolveVars(value, tokens)
		if err != nil {
			return err
		}
		switch prop {
		case "font-size":
			px, ok := s22bPx(resolved)
			if !ok {
				return fmt.Errorf("font-size %q is not a px length", resolved)
			}
			spec.SizePx, seenSize = px, true
		case "font-weight":
			w, err := strconv.Atoi(strings.TrimSpace(resolved))
			if err != nil {
				return fmt.Errorf("font-weight %q is not a number", resolved)
			}
			spec.Weight = w
		case "letter-spacing":
			em, ok := s22bEm(resolved)
			if !ok {
				return fmt.Errorf("letter-spacing %q is not an em length", resolved)
			}
			spec.TrackingEm = em
		case "line-height":
			lh, err := strconv.ParseFloat(strings.TrimSpace(resolved), 64)
			if err != nil {
				return fmt.Errorf("line-height %q is not a unitless multiplier", resolved)
			}
			spec.LineHeight = lh
		case "font-family":
			name, err := s22bFamilyToken(value, tokens)
			if err != nil {
				return err
			}
			spec.Family = name
		case "font":
			// The shorthand: [<weight>] <size>[/<line-height>] <family>.
			for _, field := range strings.Fields(value) {
				switch {
				case s22bVarRe.MatchString(field):
					name, err := s22bFamilyToken(field, tokens)
					if err != nil {
						return err
					}
					spec.Family = name
				case strings.Contains(field, "px"):
					size, lh, hasLH := strings.Cut(field, "/")
					px, ok := s22bPx(size)
					if !ok {
						return fmt.Errorf("font shorthand size %q is not a px length", size)
					}
					spec.SizePx, seenSize = px, true
					if hasLH {
						v, err := strconv.ParseFloat(lh, 64)
						if err != nil {
							return fmt.Errorf("font shorthand line-height %q: %w", lh, err)
						}
						spec.LineHeight = v
					}
				default:
					if w, err := strconv.Atoi(field); err == nil {
						spec.Weight = w
					}
				}
			}
		}
		return nil
	}

	// Declaration order matters: `font:` is a shorthand and resets what a longhand before it
	// set, exactly as in a browser.
	for _, prop := range rule.Order {
		if err := set(prop, rule.Decls[prop]); err != nil {
			return spec, fmt.Errorf("`%s { %s: %s }`: %w", sel, prop, rule.Decls[prop], err)
		}
	}
	if !seenSize {
		return spec, os.ErrNotExist // "not a text style": no size of its own
	}
	return spec, nil
}

// s22bFamilyToken maps a CSS font-family value back to the --p-* token that declares it, so the
// gate compares TOKENS rather than stacks. `var(--p-mono)` is trivial; a stack written out in
// full is matched against the token values so a rule that inlined the stack still resolves.
func s22bFamilyToken(value string, tokens map[string]string) (string, error) {
	if m := s22bVarRe.FindStringSubmatch(value); m != nil {
		if _, ok := tokens[m[1]]; !ok {
			return "", fmt.Errorf("font-family names %s, which the token origin does not declare", m[1])
		}
		return m[1], nil
	}
	for name, v := range tokens {
		if strings.TrimSpace(v) == strings.TrimSpace(value) {
			return name, nil
		}
	}
	return "", fmt.Errorf("font-family %q matches no token in the origin", value)
}

// s22bDesignTypeScale is every PRODUCT text style the design declares, selector -> spec.
func s22bDesignTypeScale(t *testing.T) map[string]s22bTypeSpec {
	t.Helper()
	tokens := s22bTokenValues(t)
	out := map[string]s22bTypeSpec{}
	for sel, rule := range s22bSharedCSS(t) {
		if _, chrome := s22bDocChromeSelectors[sel]; chrome {
			continue
		}
		spec, err := s22bReadTypeSpec(sel, rule, tokens)
		if errors.Is(err, os.ErrNotExist) {
			continue // declares no size of its own: a container, not a text style
		}
		if err != nil {
			t.Errorf("PB-DS-2: the design source does not parse: %v", err)
			continue
		}
		out[sel] = spec
	}
	if len(out) == 0 {
		t.Fatalf("PB-DS-2: no text styles parsed from the design source; the bidirectional join " +
			"below would be empty in both directions and would pass saying nothing")
	}
	return out
}

// ---------------------------------------------------------------------------
// The requirement.
// ---------------------------------------------------------------------------

// TestPBDS2_TheTypeScaleJoinsTheDesignBidirectionally is PB-DS-2's criterion, executed.
func TestPBDS2_TheTypeScaleJoinsTheDesignBidirectionally(t *testing.T) {
	design := s22bDesignTypeScale(t)
	styles := s22bStyles(t)

	// PB-DS-2 counts 18 TRANSCRIBED styles, and the count is asserted on BOTH sides: a design that
	// grew a 19th rule and a type.xml that grew a 19th style are different failures with the same
	// number.
	//
	// IT IS THE TRANSCRIBED CLASS'S COUNT, NOT THE FILE'S. `s22bStyles` reads the styles that cite
	// `origin:`, and type.xml also carries a DERIVED class -- styles whose authority is a row in a
	// document rather than a rule in the artifact. Counting the file would make adding a derived
	// style look like an unlisted transcription, which is the failure this pair of numbers exists
	// to report and would then be reporting about the wrong thing.
	// TestPBDS2_EveryStyleIsClaimedByExactlyOneClass keeps the two classes exhaustive, so the
	// split cannot become a place for a style to hide.
	//
	// THE TWO NUMBERS SPLIT ON 2026-08-09 (ADR-012), AND THE SPLIT IS ARITHMETIC RATHER THAN A
	// SECOND OPINION. What this assertion said before, quoted so the move is visible:
	//
	//	const wantStyles = 18
	//	if len(design) != wantStyles { ... }
	//	if len(styles) != wantStyles { ... }
	//
	// The design still draws 18 -- ADR-012 edits no design source, which is the whole of its
	// safety argument. The app now implements 16 of them, because T1 merges `.tcard .h` into
	// `.sheet2 .ctx` and T2 deletes the style for `.ptime`. The implemented count is DERIVED from
	// the other two rather than typed, so a rule added to the unimplemented list without a style
	// leaving type.xml fails here instead of balancing.
	const wantDesignRules = 18
	wantStyles := wantDesignRules - len(s22bUnimplementedRules)
	if len(design) != wantDesignRules {
		var got []string
		for sel := range design {
			got = append(got, sel)
		}
		sort.Strings(got)
		t.Errorf("PB-DS-2: the design source declares %d product text styles, and the "+
			"requirement is written against %d. Either a rule was added to the artifact or the "+
			"doc-chrome exclusion list is wrong.\n\t%s",
			len(design), wantDesignRules, strings.Join(got, "\n\t"))
	}
	if len(styles) != wantStyles {
		t.Errorf("PB-DS-2: %s defines %d TextAppearance.Swarm.* styles citing `origin:`, want %d "+
			"(%d rules the design draws, less the %d ADR-012 records as deliberately "+
			"unimplemented)", mustRel(t, s22bTypePath(t)), len(styles), wantStyles,
			wantDesignRules, len(s22bUnimplementedRules))
	}

	claimed := map[string]string{} // CSS selector -> the style that claims it
	for _, name := range sortedStyleNames(styles) {
		style := styles[name]

		if !strings.HasPrefix(name, "TextAppearance.Swarm.") {
			t.Errorf("PB-DS-2: %s:%d style %q is not named TextAppearance.Swarm.*",
				mustRel(t, s22bTypePath(t)), style.Line, name)
		}
		spec, ok := design[style.Origin]
		if !ok {
			t.Errorf("PB-DS-2: %s:%d style %q declares origin `%s`, which is not a text style in "+
				"the design source. A style with no rule behind it is a size somebody chose.",
				mustRel(t, s22bTypePath(t)), style.Line, name, style.Origin)
			continue
		}
		if rule, decided := s22bUnimplementedRules[style.Origin]; decided {
			t.Errorf("PB-DS-2: %s:%d style %q cites origin `%s`, which %s records as deliberately "+
				"unimplemented (%s). A style for a rule the record says this app does not spend is "+
				"the decision being reversed in a resource file.",
				mustRel(t, s22bTypePath(t)), style.Line, name, style.Origin, rule.ADR, rule.Why)
			continue
		}
		// UNLISTED STYLE FAILS -- and so does a second style pointed at the same rule, which is
		// the same defect wearing the other hat: two names for one design fact drift apart on
		// the first edit.
		if first, dup := claimed[style.Origin]; dup {
			t.Errorf("PB-DS-2: %q and %q both claim origin `%s`", first, name, style.Origin)
			continue
		}
		claimed[style.Origin] = name

		s22bAssertStyleMatches(t, name, "origin `"+style.Origin+"`", style, spec)
	}

	// UNIMPLEMENTED ROW FAILS -- unless not implementing it is itself a recorded decision, which
	// is what s22bUnimplementedRules holds and TestPBDS2_TheUnimplementedRulesAreTheOnesTheRecord
	// Decides audits. A rule skipped here is skipped WITH a citation; the skip is not what makes
	// it acceptable, the record is.
	var orphan []string
	for sel := range design {
		if _, decided := s22bUnimplementedRules[sel]; decided {
			continue
		}
		if _, ok := claimed[sel]; !ok {
			orphan = append(orphan, fmt.Sprintf("`%s` (%gpx / %d / %gem / %s)",
				sel, design[sel].SizePx, design[sel].Weight, design[sel].TrackingEm, design[sel].Family))
		}
	}
	sort.Strings(orphan)
	if len(orphan) > 0 {
		t.Errorf("PB-DS-2: %d text style(s) in the design have no TextAppearance:\n\t%s\n"+
			"Each is a rule a screen will otherwise re-specify at its call site, which is the "+
			"failure mode the requirement's own sentence names.",
			len(orphan), strings.Join(orphan, "\n\t"))
	}
}

// s22bAssertStyleMatches compares one style against the design fact it descends from. `citation`
// is the style's own claim about where that fact lives -- `origin \`.pnav .big\“ for a rule the
// artifact draws, `derived \`... §7 Display.SAS\“ for a row a document adds.
func s22bAssertStyleMatches(t *testing.T, name, citation string, style s22bStyle, spec s22bTypeSpec) {
	t.Helper()
	where := fmt.Sprintf("%s:%d %s (%s)",
		mustRel(t, s22bTypePath(t)), style.Line, name, citation)
	for _, fault := range s22bStyleFaults(where, style, spec) {
		t.Error(fault)
	}
}

// s22bStyleFaults is the comparison itself, as a value.
//
// IT RETURNS FAULTS RATHER THAN CALLING t.Errorf, and that is what makes it testable. Five of the
// six properties below are asserted only through this function, so a bug in it -- a comparison
// written `!=` where the value is a float, a branch that reports nothing when the attribute is
// absent -- would make every style in the file green against nothing, and no assertion in this
// package would notice. TestPBDS2_TheStyleComparisonRefusesAPerturbedStyle feeds perturbed values
// to THIS function, which is the only way a negative control can be about the real comparison
// rather than about a copy of it written to be controlled.
func s22bStyleFaults(where string, style s22bStyle, spec s22bTypeSpec) []string {
	var faults []string
	fault := func(format string, args ...any) {
		faults = append(faults, fmt.Sprintf(format, args...))
	}

	// SIZE. sp rather than dp: PB-DS-12's floor is that text scales with the user's setting,
	// and a design system that ships dp text has decided against accessibility by default.
	if raw, ok := style.Items["android:textSize"]; !ok {
		fault("PB-DS-2: %s declares no android:textSize; the design says %gpx",
			where, spec.SizePx)
	} else if !strings.HasSuffix(raw, "sp") {
		fault("PB-DS-2: %s has android:textSize=%q. Text must be sp: dp text does not respond "+
			"to the user's font-size setting, and PB-DS-12 makes that a floor.", where, raw)
	} else if got, err := strconv.ParseFloat(strings.TrimSuffix(raw, "sp"), 64); err != nil {
		fault("PB-DS-2: %s has android:textSize=%q, which is not a number", where, raw)
	} else if got != spec.SizePx {
		fault("PB-DS-2: %s is %gsp; the design says %gpx. CSS px in the 386x812 mock is "+
			"Android dp/sp at 1:1 -- there is no scaling factor to explain the difference.",
			where, got, spec.SizePx)
	}

	// WEIGHT. --p-display-wt is 650, which is reachable only because minSdk 33 resolves
	// textFontWeight against the platform's variable Roboto (ADR-007 B134 decision 2 and 4).
	if raw, ok := style.Items["android:textFontWeight"]; !ok {
		fault("PB-DS-2: %s declares no android:textFontWeight; the design says %d",
			where, spec.Weight)
	} else if got, err := strconv.Atoi(raw); err != nil {
		fault("PB-DS-2: %s has android:textFontWeight=%q, which is not a number", where, raw)
	} else if got != spec.Weight {
		fault("PB-DS-2: %s is weight %d; the design says %d", where, got, spec.Weight)
	}

	// TRACKING. The one unit-identical row in the whole conversion: Android's letterSpacing is
	// em, as CSS's is. It is stated even when zero, so the join is total and a style that simply
	// forgot tracking is distinguishable from one whose design has none.
	if raw, ok := style.Items["android:letterSpacing"]; !ok {
		fault("PB-DS-2: %s declares no android:letterSpacing; the design says %gem. State it "+
			"even at 0, or a forgotten value and a deliberate `normal` look identical.",
			where, spec.TrackingEm)
	} else if got, err := strconv.ParseFloat(raw, 64); err != nil {
		fault("PB-DS-2: %s has android:letterSpacing=%q, which is not a number", where, raw)
	} else if math.Abs(got-spec.TrackingEm) > 1e-9 {
		fault("PB-DS-2: %s is %gem; the design says %gem", where, got, spec.TrackingEm)
	}

	// FAMILY, through PB-DS-3's recorded substitution.
	wantFamily, ok := s22bFontSubstitution[spec.Family]
	if !ok {
		fault("PB-DS-3: %s descends from a design fact whose family is %s, for which ADR-007 "+
			"B134 records no substitution", where, spec.Family)
	} else if raw, ok := style.Items["android:fontFamily"]; !ok {
		fault("PB-DS-3: %s declares no android:fontFamily; %s substitutes to %q",
			where, spec.Family, wantFamily)
	} else if raw != wantFamily {
		fault("PB-DS-3: %s has android:fontFamily=%q; %s substitutes to %q (ADR-007 B134 "+
			"decision 2, as amended for the mono family by ADR-009 D7). Every text style in this "+
			"app renders a substitute for a font that is not licensable off Apple; the point of "+
			"the decision is that the substitute is chosen once and written down.",
			where, raw, spec.Family, wantFamily)
	}

	// FONT FEATURES, ADR-009 D7. Asserted in BOTH directions: a mono style without them is the
	// defect the decision names, and a sans style WITH them is a feature string typed at a style
	// rather than derived from what that style renders.
	features, hasFeatures := style.Items["android:fontFeatureSettings"]
	switch {
	case spec.Family == "--p-mono" && !hasFeatures:
		fault("ADR-009 D7: %s renders machine data and declares no android:fontFeatureSettings; "+
			"it must be %q. Tabular figures and the slashed zero are the whole reason a mono face "+
			"was bundled at all, and a bundled face nobody switched them on for is 540 KB of APK "+
			"buying a slightly different letter shape.", where, s22bMonoFontFeatures)
	case spec.Family == "--p-mono" && features != s22bMonoFontFeatures:
		fault("ADR-009 D7: %s has android:fontFeatureSettings=%q, want %q. The attribute is a "+
			"full override rather than an addition, so a partial list silently turns off every "+
			"feature it omits -- including the contextual alternates the family ships on.",
			where, features, s22bMonoFontFeatures)
	case spec.Family != "--p-mono" && hasFeatures:
		fault("ADR-009 D7: %s is a %s style and declares android:fontFeatureSettings=%q. The "+
			"features are for machine data, which in this design is the mono family and nothing "+
			"else; on a proportional face `tnum` is a width nobody asked for.",
			where, spec.Family, features)
	}

	// LINE HEIGHT, where the design gives one. CSS's unitless multiplier has no Android form --
	// android:lineHeight is an absolute dimension -- so the product is the value, computed here
	// rather than transcribed.
	//
	// **A MULTIPLIER OF 1 TRANSCRIBES AS SILENCE, AND IT USED TO TRANSCRIBE AS `1 x size`**
	// (ADR-009 D7, amended 2026-08-08). `line-height: 1` on a single-line label states NO EXTRA
	// LEADING. Writing the same number into android:lineHeight states something else: the
	// attribute sets the line box's ABSOLUTE height, a font's natural line box is taller than its
	// em, and the platform pays the difference as a NEGATIVE lineSpacingExtra -- so the box
	// shrinks around the text. On the two styles that carried it the result was visible:
	// `Label.Button`'s words sat low inside their own CTA and `Label.Chip`'s descenders clipped.
	// So a `/1` rule is held to the same standard as a rule that states no leading at all, which
	// is the arm above it.
	leading := spec.LineHeight != 0 && spec.LineHeight != s22bNoExtraLeading
	raw, declared := style.Items["android:lineHeight"]
	switch {
	case spec.LineHeight == s22bNoExtraLeading && declared:
		fault("PB-DS-2 (ADR-009 D7 as amended 2026-08-08): %s declares android:lineHeight=%q "+
			"against a design line-height of 1, which states NO EXTRA LEADING. android:lineHeight "+
			"is the line box's absolute height, so that number SHRINKS the box the font asks for "+
			"and the platform spends the difference as a negative lineSpacingExtra -- the label "+
			"sits low and its descenders clip. The Android form of `/1` is to declare nothing.",
			where, raw)
	case spec.LineHeight == 0 && declared:
		fault("PB-DS-2: %s declares android:lineHeight=%q, and the design fact it descends from "+
			"declares no line-height at all. An invented leading is a design decision made in an "+
			"XML file.", where, raw)
	case leading && !declared:
		fault("PB-DS-2: %s declares no android:lineHeight; the design says %g x %gpx = %gsp",
			where, spec.LineHeight, spec.SizePx, spec.LineHeight*spec.SizePx)
	case leading:
		want := spec.LineHeight * spec.SizePx
		if !strings.HasSuffix(raw, "sp") {
			fault("PB-DS-2: %s has android:lineHeight=%q; it must be sp so leading scales "+
				"with the text it leads", where, raw)
		} else if got, err := strconv.ParseFloat(strings.TrimSuffix(raw, "sp"), 64); err != nil {
			fault("PB-DS-2: %s has android:lineHeight=%q, which is not a number", where, raw)
		} else if math.Abs(got-want) > 1e-9 {
			fault("PB-DS-2: %s has line height %gsp; the design says %g x %gpx = %gsp",
				where, got, spec.LineHeight, spec.SizePx, want)
		}
	}
	return faults
}

// s22bNoExtraLeading is the CSS line-height that states no leading at all. Named rather than
// written as a bare 1 in the switch above, because the whole of the amendment is that this ONE
// value means something different on Android from every other multiplier the design states.
const s22bNoExtraLeading = 1.0

// ---------------------------------------------------------------------------
// The DERIVED class: styles whose authority is a document's row, not the artifact's rule.
// ---------------------------------------------------------------------------
//
// WHY THE JOIN NEEDED A SECOND CLASS AT ALL. `Display.SAS` is the 34 sp the pairing screen's
// verification emoji are set in, and it is not in the artifact: the artifact has no `.sas` rule,
// because the artifact draws four candidate skins and not the pairing flow. The size comes from
// the mock, and docs/design/substrate-components.md is where that was decided -- its §7 says so in
// as many words: "the only *addition* to PB-DS-2's set is `Display.SAS`", and the row promises
// that "its bidirectional gate will fail until it does".
//
// A ONE-CLASS JOIN CANNOT EXPRESS THAT AND ITS TWO ESCAPE HATCHES ARE BOTH WRONG. Pointing the
// style's `origin:` at `.sas` claims a join to a rule the artifact does not have; adding `.sas` to
// the exclusion list next to `.panelframe .cap` says the opposite of what is true, that the design
// carries a text rule the product deliberately does not implement. The kit already made this
// distinction for the same reason and in the same words -- `EmptyState.kt` carries `derived:`
// rather than `origin:` because `.empty` is the MOCK's class and no Substrate rule -- so the type
// scale takes the kit's spelling rather than inventing a third one.

// s22bDerivationDoc is the document that derives what the Substrate artifact never drew. It is
// the same file the kit's `derived:` citations resolve into (s23_kit_test.go), by design: one
// document adds components and the styles those components need, or the two halves drift.
const s22bDerivationDoc = "docs/design/substrate-components.md"

// s22bTypeTableHeader is the first cell of the §7 table's header row. Requiring it is what stops
// this reader passing vacuously over a document that was restructured: with no rows found and no
// header found, "no style is added" and "the table moved" are the same silent green.
const s22bTypeTableHeader = "Mock size"

// s22bAddedType is one §7 row that ADDS a style to PB-DS-2's set, read out of the document.
type s22bAddedType struct {
	Style    string  // "Display.SAS" -- the suffix, so the resource name is derivable from it
	SizeSp   float64 // the row's own number, never a constant here
	Weight   int
	Family   string // the --p-* token the row's family word names
	MockSize float64
	Site     string // "SAS": the row's name for the place the size is measured at
	Line     int
}

// s22bFamilyWords is the shorthand §7 writes families in, mapped to the token names the rest of
// this file joins through. The document says `sans` and `mono` where the artifact says
// `var(--p-font)` and `var(--p-mono)`; PB-DS-3's substitution table is keyed by the token, so the
// two vocabularies meet here and only here.
var s22bFamilyWords = map[string]string{"sans": "--p-font", "mono": "--p-mono"}

var (
	// `Display.SAS` 34 sp / 400 / sans — new
	s22bAddedTakesRe = regexp.MustCompile(
		"^`([A-Za-z]+\\.[A-Za-z]+)` +([0-9.]+) sp */ *([0-9]+) */ *([a-z]+) +— +new$")
	// 34 (SAS)
	s22bMockSizeRe = regexp.MustCompile(`^([0-9.]+) +\(([^)]+)\)$`)
)

// s22bTableCells splits one Markdown table row into its cells, with the emphasis markers removed:
// §7 bolds the added row to make it visible to a reader, and a join that read `**34 (SAS)**` as a
// different value from `34 (SAS)` would be asserting the document's typography.
func s22bTableCells(line string) []string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		return nil
	}
	fields := strings.Split(trimmed, "|")
	out := make([]string, 0, len(fields))
	for _, cell := range fields[1 : len(fields)-1] {
		out = append(out, strings.TrimSpace(strings.ReplaceAll(cell, "**", "")))
	}
	return out
}

// s22bAddedTypeRows reads every §7 row that adds a style, from the document.
func s22bAddedTypeRows(t *testing.T) []s22bAddedType {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(s22bDerivationDoc))
	raw := readFileOrFail(t, path, "PB-DS-2")

	var out []s22bAddedType
	header := false
	for i, line := range strings.Split(raw, "\n") {
		cells := s22bTableCells(line)
		if len(cells) < 2 {
			continue
		}
		if cells[0] == s22bTypeTableHeader {
			header = true
			continue
		}
		takes := s22bAddedTakesRe.FindStringSubmatch(cells[1])
		if takes == nil {
			continue
		}
		row := s22bAddedType{Style: takes[1], Line: i + 1}
		var err error
		if row.SizeSp, err = strconv.ParseFloat(takes[2], 64); err != nil {
			t.Errorf("PB-DS-2: %s:%d states size %q for `%s`, which is not a number",
				mustRel(t, path), row.Line, takes[2], row.Style)
			continue
		}
		if row.Weight, err = strconv.Atoi(takes[3]); err != nil {
			t.Errorf("PB-DS-2: %s:%d states weight %q for `%s`, which is not a number",
				mustRel(t, path), row.Line, takes[3], row.Style)
			continue
		}
		family, known := s22bFamilyWords[takes[4]]
		if !known {
			t.Errorf("PB-DS-2: %s:%d states family %q for `%s`, and the design has two families "+
				"-- `sans` and `mono`. A third one is a decision, not a row.",
				mustRel(t, path), row.Line, takes[4], row.Style)
			continue
		}
		row.Family = family

		// The row's own two halves must agree. Every OTHER row in this table maps a mock size onto
		// a DIFFERENT existing style and records the move; an added style is added for exactly one
		// size, so a row whose "Takes" is not its "Mock size" is a resize wearing an addition's
		// clothes -- and the Move cell, which is prose, is where that would otherwise be argued.
		mock := s22bMockSizeRe.FindStringSubmatch(cells[0])
		if mock == nil {
			t.Errorf("PB-DS-2: %s:%d adds `%s` on a row whose first cell is %q, which is not a "+
				"`<size> (<site>)` measurement. The size a new style exists for is the whole "+
				"authority behind it.", mustRel(t, path), row.Line, row.Style, cells[0])
			continue
		}
		if row.MockSize, err = strconv.ParseFloat(mock[1], 64); err != nil {
			t.Errorf("PB-DS-2: %s:%d measures %q, which is not a number",
				mustRel(t, path), row.Line, mock[1])
			continue
		}
		row.Site = mock[2]
		out = append(out, row)
	}
	if !header {
		t.Fatalf("PB-DS-2: %s has no table whose first column is %q. §7 is where a style is added "+
			"to PB-DS-2's set, and a reader that cannot find it reports `no style is added` for a "+
			"document that was merely reorganised.", mustRel(t, path), s22bTypeTableHeader)
	}
	return out
}

// s22bClassified is one style in type.xml with the CLASS of authority it claims.
type s22bClassified struct {
	Kind string // "origin" or "derived"
	Ref  string // the CSS selector, or the document row citation
	s22bStyle
}

// s22bClassifiedStyleRe reads BOTH annotations, so a style that cites neither is invisible to it
// and is caught by the count against s22bAnyStyleRe rather than by being quietly skipped.
var s22bClassifiedStyleRe = regexp.MustCompile(
	`(?s)<!--\s*(origin|derived):\s*(.*?)\s*-->\s*<style\s+name="([^"]+)"[^>]*>(.*?)</style>`)

var s22bAnyStyleRe = regexp.MustCompile(`<style\s+name="([^"]+)"`)

func s22bClassifiedStyles(t *testing.T) map[string]s22bClassified {
	t.Helper()
	path := s22bTypePath(t)
	text := readFileOrFail(t, path, "PB-DS-2")

	out := map[string]s22bClassified{}
	for _, m := range s22bClassifiedStyleRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[m[6]:m[7]]
		items := map[string]string{}
		for _, im := range s22bItemRe.FindAllStringSubmatch(text[m[8]:m[9]], -1) {
			items[im[1]] = im[2]
		}
		// THE CITATION IS A LINE, not the comment. `origin:` annotations are one line and nothing
		// else, but a derived style's authority is a document row rather than a selector a reader
		// recognises on sight, so its comment carries the reasoning underneath -- and a reader
		// that swallowed the prose would compare a paragraph against a row name.
		first, _, _ := strings.Cut(text[m[4]:m[5]], "\n")
		ref := strings.Join(strings.Fields(first), " ")
		out[name] = s22bClassified{
			Kind: text[m[2]:m[3]],
			Ref:  ref,
			s22bStyle: s22bStyle{
				Name:   name,
				Origin: ref,
				Items:  items,
				Line:   1 + strings.Count(text[:m[0]], "\n"),
			},
		}
	}
	return out
}

// TestPBDS2_EveryStyleIsClaimedByExactlyOneClass makes the two classes exhaustive.
//
// A JOIN SPLIT IN TWO IS A JOIN WITH A GAP DOWN THE MIDDLE unless something closes it. Both
// readers key off an annotation comment, so a style with NO comment above it is read by neither:
// it would not be counted against the design's 18, would not be counted against §7's additions,
// and would ship a size nobody joined to anything -- which is the exact defect the whole file
// exists to make impossible, reintroduced by the mechanism meant to prevent it.
func TestPBDS2_EveryStyleIsClaimedByExactlyOneClass(t *testing.T) {
	path := s22bTypePath(t)
	text := readFileOrFail(t, path, "PB-DS-2")
	classified := s22bClassifiedStyles(t)

	var unclaimed []string
	for _, m := range s22bAnyStyleRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[m[2]:m[3]]
		if _, ok := classified[name]; !ok {
			unclaimed = append(unclaimed, fmt.Sprintf("%s:%d %s",
				mustRel(t, path), 1+strings.Count(text[:m[0]], "\n"), name))
		}
	}
	sort.Strings(unclaimed)
	if len(unclaimed) > 0 {
		t.Errorf("PB-DS-2: %d style(s) in %s carry neither an `origin:` nor a `derived:` "+
			"annotation:\n\t%s\nA style with no annotation is read by neither half of the join, so "+
			"its size is a number somebody chose and no assertion in this package is about it.",
			len(unclaimed), mustRel(t, path), strings.Join(unclaimed, "\n\t"))
	}

	// The transcribed class's own reader must see exactly the styles classified as `origin`. Two
	// readers over one file is a hazard the moment they disagree: `s22bStyles` is what the
	// bidirectional join above counts, and if it silently stopped seeing a style, that count would
	// balance at 18 while the file held 19.
	transcribed := s22bStyles(t)
	for name, c := range classified {
		_, seen := transcribed[name]
		switch {
		case c.Kind == "origin" && !seen:
			t.Errorf("PB-DS-2: %s cites `origin: %s` and the transcribed-class reader does not "+
				"see it. The two readers disagree about what is in the file.", name, c.Ref)
		case c.Kind != "origin" && seen:
			t.Errorf("PB-DS-2: %s cites `%s:` and the transcribed-class reader counts it among "+
				"the 18 the design draws.", name, c.Kind)
		}
	}
	for name := range transcribed {
		if _, ok := classified[name]; !ok {
			t.Errorf("PB-DS-2: the transcribed-class reader sees %s and the classifier does not",
				name)
		}
	}
}

// TestPBDS2_TheAddedStylesAreTheOnesTheDocumentAdds is the derived class's join, in both
// directions, exactly as the transcribed class's is: a style citing a row §7 does not add fails,
// and a row §7 adds with no style fails.
func TestPBDS2_TheAddedStylesAreTheOnesTheDocumentAdds(t *testing.T) {
	rows := s22bAddedTypeRows(t)
	if len(rows) == 0 {
		t.Fatalf("PB-DS-2: %s adds no style to PB-DS-2's set. §7 states that it adds exactly one "+
			"(`Display.SAS`, for the 34 sp SAS display) and that the bidirectional gate must fail "+
			"until it exists; a reader that finds none makes that promise unkeepable.",
			s22bDerivationDoc)
	}

	byStyle := map[string]s22bAddedType{}
	for _, row := range rows {
		if first, dup := byStyle[row.Style]; dup {
			t.Errorf("PB-DS-2: %s adds `%s` twice, at lines %d and %d",
				s22bDerivationDoc, row.Style, first.Line, row.Line)
			continue
		}
		byStyle[row.Style] = row

		if row.SizeSp != row.MockSize {
			t.Errorf("PB-DS-2: %s:%d adds `%s` at %g sp for a %s measured at %g. An added style "+
				"exists for one size; a row that adds one AND moves it is two decisions written "+
				"as one, and the second is the one nobody reviewed.",
				s22bDerivationDoc, row.Line, row.Style, row.SizeSp, row.Site, row.MockSize)
		}
	}

	claimed := map[string]string{}
	for name, style := range s22bClassifiedStyles(t) {
		if style.Kind != "derived" {
			continue
		}
		// The citation names the document and the style the row adds, so following it is a
		// lookup rather than a search: `derived: docs/design/substrate-components.md §7 <Style>`.
		want := s22bDerivationDoc + " §7 "
		suffix, ok := strings.CutPrefix(style.Ref, want)
		if !ok {
			t.Errorf("PB-DS-2: %s:%d %s cites `derived: %s`, and a derived style's authority is a "+
				"row of §7, cited as `%s<Style>`. A citation that does not resolve to a row is the "+
				"word `derived` doing the work the row was supposed to do.",
				mustRel(t, s22bTypePath(t)), style.Line, name, style.Ref, want)
			continue
		}
		row, added := byStyle[suffix]
		if !added {
			var known []string
			for s := range byStyle {
				known = append(known, s)
			}
			sort.Strings(known)
			t.Errorf("PB-DS-2: %s:%d %s cites §7's row for `%s`, and §7 adds no such style (it "+
				"adds %s). PB-DS-2's set grows only where the document says it grows.",
				mustRel(t, s22bTypePath(t)), style.Line, name, suffix, strings.Join(known, ", "))
			continue
		}
		claimed[suffix] = name

		// The resource name is DERIVED from the row's style name rather than compared to a
		// constant: `Display.SAS` is `TextAppearance.Swarm.Display.SAS` and can be nothing else,
		// so a style that cites the row and calls itself something else is caught here instead of
		// living on as a second name for one design fact.
		if wantName := "TextAppearance.Swarm." + suffix; name != wantName {
			t.Errorf("PB-DS-2: %s:%d cites §7's `%s` row and is named %q, want %q",
				mustRel(t, s22bTypePath(t)), style.Line, suffix, name, wantName)
		}

		s22bAssertStyleMatches(t, name, "derived `"+style.Ref+"`", style.s22bStyle,
			s22bAddedTypeSpec(row))
	}

	for suffix, row := range byStyle {
		if _, ok := claimed[suffix]; !ok {
			t.Errorf("PB-DS-2: %s:%d adds `%s` (%g sp / %d / %s) to PB-DS-2's set and %s defines "+
				"no style for it. The row states that the bidirectional gate fails until it does, "+
				"and this is that failure.",
				s22bDerivationDoc, row.Line, row.Style, row.SizeSp, row.Weight, row.Family,
				mustRel(t, s22bTypePath(t)))
		}
	}
}

// s22bAddedTypeSpec turns a §7 row into the same spec a CSS rule resolves to, so the derived class
// is held to the transcribed class's comparison rather than to a gentler one of its own.
//
// TRACKING AND LINE HEIGHT COME FROM THE ROW'S SILENCE, and that is a reading rather than an
// omission. §7 states three properties for an added style; CSS's own defaults for the two it does
// not state are `letter-spacing: normal` (0 em) and no line-height at all, which is exactly what
// s22bReadTypeSpec resolves for a rule that declares neither. So the derived style must state
// tracking 0 -- the same "state it even at zero" rule the transcribed styles are held to -- and
// must declare no lineHeight, because a leading nobody wrote down is one the XML file invented.
func s22bAddedTypeSpec(row s22bAddedType) s22bTypeSpec {
	return s22bTypeSpec{
		Selector:   fmt.Sprintf("§7 `%s`", row.Style),
		SizePx:     row.SizeSp,
		Weight:     row.Weight,
		TrackingEm: 0,
		Family:     row.Family,
		LineHeight: 0,
	}
}

// TestPBDS2_TheDerivedReadersRefusePerturbedInput is the negative control for everything above.
//
// Each case feeds a perturbed value to THE FUNCTION THE REAL ASSERTION CALLS. A control written
// against a private copy of the comparison proves the copy works.
func TestPBDS2_TheDerivedReadersRefusePerturbedInput(t *testing.T) {
	// 1. The row reader. The real row parses; six perturbations of it must not, because each is a
	//    different way for a document edit to mean something the gate would otherwise assert.
	rowAsWritten := "| **34 (SAS)** | **`Display.SAS` 34 sp / 400 / sans — new** | PB-DS-2's set |"
	cells := s22bTableCells(rowAsWritten)
	if len(cells) != 3 {
		t.Fatalf("the table reader splits the §7 row into %d cells, want 3: %q", len(cells), cells)
	}
	if m := s22bAddedTakesRe.FindStringSubmatch(cells[1]); m == nil {
		t.Errorf("the added-row pattern does not match §7's own spelling %q. Every assertion in "+
			"TestPBDS2_TheAddedStylesAreTheOnesTheDocumentAdds is over rows this pattern found, "+
			"so a pattern that matches nothing reports that nothing was added.", cells[1])
	}
	for _, perturbed := range []string{
		"`Display.SAS` 34 sp / 400 / sans",       // no `new`: a mapping, not an addition
		"`Display.SAS` 34 sp / 400 — new",        // no family
		"`Display.SAS` 34 sp / sans — new",       // no weight
		"`Display.SAS` / 400 / sans — new",       // no size
		"`DisplaySAS` 34 sp / 400 / sans — new",  // not a Group.Name
		"`Display.SAS` 34 sp / 400 / sans - new", // a hyphen, so not this document's row
	} {
		if m := s22bAddedTakesRe.FindStringSubmatch(perturbed); m != nil {
			t.Errorf("the added-row pattern reads %q as a style addition (%v). A pattern this "+
				"loose finds additions in rows that state none, and the join then asserts a size "+
				"against a row that never named one.", perturbed, m[1:])
		}
	}

	// 2. The comparison. The spec below is §7's row; the style is what type.xml would have to hold
	//    for it, and each perturbation of that style must be reported by s22bStyleFaults itself.
	spec := s22bAddedTypeSpec(s22bAddedType{
		Style: "Display.SAS", SizeSp: 34, Weight: 400, Family: "--p-font", MockSize: 34, Site: "SAS",
	})
	correct := map[string]string{
		"android:textSize":       "34sp",
		"android:textFontWeight": "400",
		"android:letterSpacing":  "0",
		"android:fontFamily":     "sans-serif",
	}
	if faults := s22bStyleFaults("control", s22bStyle{Items: correct}, spec); len(faults) != 0 {
		t.Errorf("PB-DS-2: the style §7's row describes is reported as a fault: %v", faults)
	}
	for what, items := range map[string]map[string]string{
		"a size the row does not state":   {"android:textSize": "32sp"},
		"the size in dp":                  {"android:textSize": "34dp"},
		"a weight the row does not state": {"android:textFontWeight": "650"},
		"tracking the row does not state": {"android:letterSpacing": "-0.025"},
		"the mono family":                 {"android:fontFamily": "@font/jetbrains_mono"},
		"no size at all":                  {"android:textSize": ""},
		"no tracking at all":              {"android:letterSpacing": ""},
		// ADR-009 D7, the sans direction: features belong to machine data, and this row is sans.
		"font features on a proportional face": {"android:fontFeatureSettings": s22bMonoFontFeatures},
	} {
		perturbed := map[string]string{}
		for k, v := range correct {
			perturbed[k] = v
		}
		for k, v := range items {
			if v == "" {
				delete(perturbed, k)
				continue
			}
			perturbed[k] = v
		}
		if faults := s22bStyleFaults("control", s22bStyle{Items: perturbed}, spec); len(faults) == 0 {
			t.Errorf("PB-DS-2: a derived style carrying %s passes the comparison §7's row is "+
				"asserted through. The row is then a citation the gate follows and never reads.",
				what)
		}
	}
	// An invented leading is the one fault that is about an attribute being PRESENT, so it cannot
	// be expressed by perturbing a value above.
	withLeading := map[string]string{"android:lineHeight": "47.6sp"}
	for k, v := range correct {
		withLeading[k] = v
	}
	if faults := s22bStyleFaults("control", s22bStyle{Items: withLeading}, spec); len(faults) == 0 {
		t.Error("PB-DS-2: a derived style carrying a line height passes against a row that states " +
			"none. §7 states three properties for an added style, and a fourth appearing in " +
			"type.xml is a design decision taken in a resource file.")
	}

	// AND THE `/1` ARM, WHICH IS THE 2026-08-08 AMENDMENT'S OWN. It cannot be reached by
	// perturbing a value either: what it judges is a design fact whose multiplier is exactly 1,
	// against a style that either transcribes it as a number or -- correctly -- says nothing.
	// Both directions, because an arm that only refused would pass a green that comes from
	// refusing everything.
	singleLine := spec
	singleLine.LineHeight = s22bNoExtraLeading
	if faults := s22bStyleFaults("control", s22bStyle{Items: correct}, singleLine); len(faults) != 0 {
		t.Errorf("PB-DS-2: a style declaring NO android:lineHeight against a design line-height "+
			"of 1 is reported as a fault: %v. `line-height: 1` states no extra leading, and "+
			"silence is its Android form.", faults)
	}
	transcribedOne := map[string]string{"android:lineHeight": "34sp"}
	for k, v := range correct {
		transcribedOne[k] = v
	}
	if faults := s22bStyleFaults("control", s22bStyle{Items: transcribedOne}, singleLine); len(faults) == 0 {
		t.Error("PB-DS-2: a style whose android:lineHeight equals its own text size passes " +
			"against a design line-height of 1. That is the shrunken line box the amendment " +
			"exists to refuse -- the platform pays for it with a negative lineSpacingExtra, and " +
			"the label sits low in its own control.")
	}

	// 3. ADR-009 D7's mono direction. The perturbations above all run against a SANS spec, so the
	//    "a mono style must carry the features" branch would be unexercised by every one of them
	//    -- and an unexercised branch that reports nothing is exactly how a feature string ends up
	//    declared on no style at all while this file stays green.
	monoSpec := s22bAddedTypeSpec(s22bAddedType{
		Style: "Mono.Control", SizeSp: 11.5, Weight: 400, Family: "--p-mono", MockSize: 11.5,
		Site: "control",
	})
	monoCorrect := map[string]string{
		"android:textSize":            "11.5sp",
		"android:textFontWeight":      "400",
		"android:letterSpacing":       "0",
		"android:fontFamily":          s22bFontSubstitution["--p-mono"],
		"android:fontFeatureSettings": s22bMonoFontFeatures,
	}
	if faults := s22bStyleFaults("control", s22bStyle{Items: monoCorrect}, monoSpec); len(faults) != 0 {
		t.Errorf("ADR-009 D7: the mono style the decision describes is reported as a fault: %v",
			faults)
	}
	for what, items := range map[string]map[string]string{
		"no font features at all":  {"android:fontFeatureSettings": ""},
		"only two of the three":    {"android:fontFeatureSettings": "tnum, zero"},
		"the platform mono family": {"android:fontFamily": "monospace"},
	} {
		perturbed := map[string]string{}
		for k, v := range monoCorrect {
			perturbed[k] = v
		}
		for k, v := range items {
			if v == "" {
				delete(perturbed, k)
				continue
			}
			perturbed[k] = v
		}
		if faults := s22bStyleFaults("control", s22bStyle{Items: perturbed}, monoSpec); len(faults) == 0 {
			t.Errorf("ADR-009 D7: a mono style carrying %s passes the comparison. The decision "+
				"is then a paragraph the gate cites and never checks.", what)
		}
	}
}

// TestPBDS2_NoTextStyleCarriesAColour protects the scoping decision this file's header states.
func TestPBDS2_NoTextStyleCarriesAColour(t *testing.T) {
	styles := s22bClassifiedStyles(t)
	for _, name := range sortedClassifiedNames(styles) {
		for item, value := range styles[name].Items {
			if strings.Contains(strings.ToLower(item), "color") {
				t.Errorf("PB-DS-2: style %q carries %s=%q. Colour is applied by the component "+
					"kit (PB-DS-6): the same style renders in several colours -- Label.Button is "+
					"the hero ink, the error red and the primary ink at its three call sites -- "+
					"so a TextAppearance that carried one would be wrong at two of them.",
					name, item, value)
			}
			if strings.HasPrefix(value, "@color/") {
				t.Errorf("PB-DS-2: style %q sets %s to the colour resource %q", name, item, value)
			}
		}
	}
}

// TestPBDS2_TheDocChromeExclusionIsStillTrue keeps the one exclusion honest.
//
// A gate with an exclusion list is a gate with a hole in it, and the hole is only safe while the
// reason for it holds. `.panelframe .cap` is excluded because it is documentation chrome, and
// the evidence is that it names `var(--mono)` -- a variable no skin in the artifact declares, so
// it has never rendered in a product font. That evidence is checked here rather than assumed.
func TestPBDS2_TheDocChromeExclusionIsStillTrue(t *testing.T) {
	css := s22bSharedCSS(t)
	tokens := s22bTokenValues(t)

	for sel, why := range s22bDocChromeSelectors {
		rule, ok := css[sel]
		if !ok {
			t.Errorf("PB-DS-2: the exclusion list names `%s`, which the design source no longer "+
				"declares. An exclusion for a rule that is gone hides nothing and confuses the "+
				"next reader: delete it.", sel)
			continue
		}
		if _, err := s22bReadTypeSpec(sel, rule, tokens); err == nil {
			t.Errorf("PB-DS-2: `%s` now resolves cleanly against the token origin, so the "+
				"evidence for excluding it (%s) no longer holds. It must either get a "+
				"TextAppearance or be excluded for a reason that is still true.", sel, why)
		}
	}
}

// TestPBDS2_TheUnimplementedRulesAreTheOnesTheRecordDecides audits the one list in this file that
// lets a design rule go unimplemented.
//
// THE BIDIRECTIONAL JOIN IS ONLY AS STRONG AS THIS TEST. Every selector in s22bUnimplementedRules
// is a rule the join above stops asking for, so the list is the join's own escape hatch and the
// three things checked here are what keep it from being one:
//
//   - the rule is STILL DRAWN by the design source. An entry for a rule that is gone is dead
//     bookkeeping, and worse, it silently lowers the implemented count by one forever.
//   - the DECISION IS ON RECORD, under the anchor the entry names, naming the style it applies to.
//     Following the citation is what makes "deliberately" a claim a reviewer can check.
//   - a MERGE STILL RENDERS IDENTICALLY. `.tcard .h` is dropped because `.sheet2 .ctx` draws the
//     same pixels; the day the design moves either rule, or the day the mono family ships a 600
//     face, that stops being true and the merge becomes a size change nobody reviewed.
func TestPBDS2_TheUnimplementedRulesAreTheOnesTheRecordDecides(t *testing.T) {
	design := s22bDesignTypeScale(t)
	faces := s22bBundledFaceWeights(t)

	for _, sel := range sortedKeys(s22bUnimplementedRules) {
		rule := s22bUnimplementedRules[sel]

		spec, drawn := design[sel]
		if !drawn {
			t.Errorf("PB-DS-2: %s records `%s` as deliberately unimplemented and the design source "+
				"declares no such text rule. An entry for a rule that is gone holds the implemented "+
				"count down by one and explains nothing: delete it.", rule.ADR, sel)
			continue
		}

		adr := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(rule.ADR)), "PB-DS-2")
		start := strings.Index(adr, rule.Anchor)
		if start < 0 {
			t.Errorf("PB-DS-2: %s has no %q section, so `%s` is unimplemented on the authority of a "+
				"map in a test file", rule.ADR, rule.Anchor, sel)
			continue
		}
		entry := adr[start:]
		if end := strings.Index(entry[len(rule.Anchor):], "\n### "); end >= 0 {
			entry = entry[:len(rule.Anchor)+end]
		}
		for _, want := range append([]string{"`" + sel + "`"}, rule.Wants...) {
			if !strings.Contains(entry, want) {
				t.Errorf("PB-DS-2: %s %s does not mention %s. The record and this list must be the "+
					"same decision, or the decision is whatever the gate happens to say.",
					rule.ADR, strings.TrimSuffix(rule.Anchor, "."), want)
			}
		}

		if rule.MergedInto == "" {
			continue
		}
		into, ok := design[rule.MergedInto]
		if !ok {
			t.Errorf("PB-DS-2: `%s` is recorded as merged into `%s`, which the design source does "+
				"not declare. The rule it merged into is the whole reason dropping it costs "+
				"nothing.", sel, rule.MergedInto)
			continue
		}
		if diffs := s22bRenderDifferences(spec, into, faces); len(diffs) > 0 {
			t.Errorf("PB-DS-2 (%s %s): `%s` and `%s` no longer render identically -- %s. The merge "+
				"was authorized on the ground that no pixel moves; it is now a size, weight or "+
				"family change carried by a deletion.",
				rule.ADR, strings.TrimSuffix(rule.Anchor, "."), sel, rule.MergedInto,
				strings.Join(diffs, "; "))
		}
	}
}

// TestPBDS2_TheRenderEqualityRefusesAPerturbedPair is the negative control for the merge's premise.
//
// The pair below is the real one, read out of the design source, so a green here is about the rules
// this app actually merged. Each perturbation is IN MEMORY -- a copy of the spec with one field
// moved -- because a control that edited a file on disk would be proving something about a file
// nobody ships.
func TestPBDS2_TheRenderEqualityRefusesAPerturbedPair(t *testing.T) {
	design := s22bDesignTypeScale(t)
	faces := s22bBundledFaceWeights(t)

	merged, into := ".tcard .h", ".sheet2 .ctx"
	a, ok := design[merged]
	if !ok {
		t.Fatalf("PB-DS-2: the design source no longer declares `%s`; this control would be about "+
			"a zero value", merged)
	}
	b, ok := design[into]
	if !ok {
		t.Fatalf("PB-DS-2: the design source no longer declares `%s`; this control would be about "+
			"a zero value", into)
	}
	if diffs := s22bRenderDifferences(a, b, faces); len(diffs) > 0 {
		t.Errorf("PB-DS-2: the pair ADR-012 T1 merges is reported as different: %v", diffs)
	}

	// A 600 FACE IS THE PERTURBATION THAT MATTERS, because it is the one that could arrive without
	// anybody touching the design: 600 and 500 are the pair's declared weights, and they are the
	// same pixels only while the family answers neither with its own face.
	if diffs := s22bRenderDifferences(a, b, []int{400, 500, 600}); len(diffs) == 0 {
		t.Errorf("PB-DS-2: `%s` (weight %d) and `%s` (weight %d) still compare equal against a "+
			"family shipping a 600 face. The merge's whole premise is the resolution, so a "+
			"comparison blind to it would certify a bold-against-medium substitution as no change.",
			merged, a.Weight, into, b.Weight)
	}

	for what, perturb := range map[string]func(s22bTypeSpec) s22bTypeSpec{
		"half a point of size": func(s s22bTypeSpec) s22bTypeSpec { s.SizePx += 0.5; return s },
		"tracking the rule does not state": func(s s22bTypeSpec) s22bTypeSpec {
			s.TrackingEm = 0.01
			return s
		},
		"the sans family": func(s s22bTypeSpec) s22bTypeSpec { s.Family = "--p-font"; return s },
		"a leading":       func(s s22bTypeSpec) s22bTypeSpec { s.LineHeight = 1.4; return s },
	} {
		if diffs := s22bRenderDifferences(perturb(a), b, faces); len(diffs) == 0 {
			t.Errorf("PB-DS-2: a rule differing by %s compares equal to `%s`. The merge is then "+
				"authorized by a comparison that agrees with everything.", what, into)
		}
	}
}

// ---------------------------------------------------------------------------
// PB-DS-3.
// ---------------------------------------------------------------------------

// TestPBDS3_TheSubstitutionIsTheOneTheADRRecords joins each row of the constant in this file to
// the ADR that decides it.
//
// AUTHORIZED REWRITE, ADR-009 D8.3 / D7. What this test asserted before, quoted so the pin's move
// is visible rather than inferred:
//
//	adr := readFileOrFail(t, ...("docs/adr/ADR-007-remote-access.md"), "PB-DS-3")
//	start := strings.Index(adr, "## B134.")
//	entry := adr[start:]
//	for token, family := range s22bFontSubstitution {
//		if !strings.Contains(entry, "`"+family+"`") {
//			t.Errorf("PB-DS-3: ADR-007 B134 does not record %q as the substitute for %s...")
//		}
//	}
//	if !strings.Contains(entry, "zero bundled assets") {
//		t.Errorf("PB-DS-3: ADR-007 B134 no longer records `zero bundled assets`...")
//	}
//
// It read ONE entry because there was one decision. There are now two, and the rewrite is a
// widening rather than a weakening: `zero bundled assets` is still required, of the sans row that
// still claims it, and the mono row has to name its bundled family, both faces AND the feature
// string in the ADR that decided them. A single-entry reader could not have said that.
func TestPBDS3_TheSubstitutionIsTheOneTheADRRecords(t *testing.T) {
	for _, token := range sortedKeys(s22bFontSubstitution) {
		record, ok := s22bFontRecord[token]
		if !ok {
			t.Errorf("PB-DS-3: %s substitutes to %q and no ADR is named for it, so the "+
				"substitution is a choice made in a test", token, s22bFontSubstitution[token])
			continue
		}
		adr := readFileOrFail(t,
			filepath.Join(repoRoot(t), filepath.FromSlash(record.ADR)), "PB-DS-3")
		start := strings.Index(adr, record.Anchor)
		if start < 0 {
			t.Errorf("PB-DS-3: %s has no %q section; %s's substitution has no record and the "+
				"table in this file would be a choice made in a test",
				record.ADR, record.Anchor, token)
			continue
		}
		entry := adr[start:]
		if end := strings.Index(entry[len(record.Anchor):], "\n## "); end >= 0 {
			entry = entry[:len(record.Anchor)+end]
		}
		wants := append([]string{"`" + s22bFontSubstitution[token] + "`"}, record.Wants...)
		for _, want := range wants {
			if !strings.Contains(entry, want) {
				t.Errorf("PB-DS-3: %s %s does not record %s for %s. The gate's table and the "+
					"record must be the same decision, or the decision is whatever the gate "+
					"happens to say.", record.ADR, strings.TrimSuffix(record.Anchor, "."), want, token)
			}
		}
	}

	// The supersession has to be visible from the OLD record too. B134 still says "zero bundled
	// assets" -- correctly, of the sans family -- and a reader who stops there would conclude the
	// app bundles nothing, which is now false. Requiring the pointer is what stops two records
	// disagreeing in silence.
	b134 := readFileOrFail(t,
		filepath.Join(repoRoot(t), filepath.FromSlash("docs/adr/ADR-007-remote-access.md")),
		"PB-DS-3")
	if !strings.Contains(b134, "ADR-009 D7") {
		t.Error("PB-DS-3: ADR-007 does not name ADR-009 D7 anywhere. B134 decision 2 still reads " +
			"`zero bundled assets`, which is true of the sans family and false of the app; the " +
			"entry must carry the pointer to the decision that superseded its mono half.")
	}
}

// TestPBDS3_ExactlyTheDecidedFontsAreBundled is the mechanical half of the decision.
//
// AUTHORIZED REWRITE, ADR-009 D8.3 / D7. This test was TestPBDS3_NoFontIsBundled, and what it
// asserted, quoted so the pin's move is visible rather than inferred:
//
//	sort.Strings(found)
//	if len(found) > 0 {
//		t.Errorf("PB-DS-3: the decision is the platform families with ZERO bundled assets, and "+
//			"these are bundled:\n\t%s\nBundling JetBrains Mono is the recorded upgrade path for "+
//			"the box-drawing residual, and taking it is a decision that belongs in ADR-007 B134 "+
//			"rather than in a resource directory.", strings.Join(found, "\n\t"))
//	}
//
// "Zero" becomes "exactly these three", which is a STRONGER assertion and not a relaxed one: the
// old test could not tell a second mono weight, a bundled sans, or an italic nobody uses from
// nothing at all, and each of those is a quarter-megabyte of APK arriving without a decision.
// Set equality reports a missing file and a surplus file as two different failures.
//
// IT STILL SCANS res/ AND assets/ WHOLE rather than res/font/ alone, for the reason the old test
// gave: a scan pointed at one directory is green when the walk is broken, when the extension list
// is empty, and when a face is sitting one directory away. The file count below is asserted so an
// empty walk reports itself.
func TestPBDS3_ExactlyTheDecidedFontsAreBundled(t *testing.T) {
	roots := []string{
		filepath.Join(appModule(t), "src", "main", "res"),
		filepath.Join(appModule(t), "src", "main", "assets"),
	}
	// A binary face anywhere, or ANYTHING under res/font/ -- that directory exists for exactly
	// one purpose, so an XML family definition in it counts as a bundled font while the identical
	// extension under res/values/ does not.
	binary := map[string]bool{".ttf": true, ".otf": true, ".ttc": true, ".woff": true, ".woff2": true}
	fontDir := string(filepath.Separator) + "font" + string(filepath.Separator)

	found := map[string]bool{}
	walked := 0
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil //nolint:nilerr // a missing assets/ IS part of the passing state
			}
			walked++
			if binary[strings.ToLower(filepath.Ext(path))] || strings.Contains(path, fontDir) {
				found[filepath.ToSlash(mustRel(t, path))] = true
			}
			return nil
		})
	}
	if walked == 0 {
		t.Fatalf("PB-DS-3: walked no files under %s; a clean report would mean nothing",
			mustRel(t, roots[0]))
	}

	for _, want := range sortedKeys(s22bBundledFonts) {
		if !found[want] {
			t.Errorf("ADR-009 D7: %s is not bundled (%s). The mono styles resolve %q, and an "+
				"android:fontFamily Android cannot resolve does not fail the build -- it falls "+
				"back to the default sans, silently, and the terminal peek stops being a terminal.",
				want, s22bBundledFonts[want], s22bFontSubstitution["--p-mono"])
		}
	}
	for _, got := range sortedKeys(found) {
		if _, ok := s22bBundledFonts[got]; !ok {
			t.Errorf("ADR-009 D7: %s is bundled and no decision names it. D7 authorizes exactly "+
				"two faces; every extra weight, italic or family is a quarter-megabyte of APK "+
				"that entered through a resource directory rather than through a record.", got)
		}
	}

	// OFL-1.1's own condition: the licence travels with the font. It is checked in beside the
	// design docs rather than under res/, because res/font/ takes font resources and nothing
	// else -- a .txt there is an aapt error, not a licence.
	if _, err := os.Stat(filepath.Join(repoRoot(t), filepath.FromSlash(s22bFontLicence))); err != nil {
		t.Errorf("ADR-009 D7: %s is missing. JetBrains Mono ships under OFL-1.1, which requires "+
			"the licence and the copyright notice to travel with the font; bundling the bytes "+
			"without them is the one part of this decision that is not ours to make.",
			s22bFontLicence)
	}

	t.Logf("PB-DS-3 / ADR-009 D7: %d files under res/ and assets/, %d of them fonts",
		walked, len(found))
}

// TestPBDS3_EveryMonoRuleBecomesAMonoStyle is the criterion's exact words -- "a gate asserts the
// mono style's family is the one recorded" -- with the join in both directions.
//
// The one-directional version ("every style whose family is monospace descends from a --p-mono
// rule") is the weaker half and is the one that passes over an empty set: a type.xml with no
// mono styles at all satisfies it.
func TestPBDS3_EveryMonoRuleBecomesAMonoStyle(t *testing.T) {
	design := s22bDesignTypeScale(t)
	styles := s22bStyles(t)

	byOrigin := map[string]s22bStyle{}
	for _, s := range styles {
		byOrigin[s.Origin] = s
	}

	monoRules := 0
	for sel, spec := range design {
		if spec.Family != "--p-mono" {
			continue
		}
		monoRules++
		style, ok := byOrigin[sel]
		if !ok {
			continue // reported by the bidirectional join above
		}
		if got := style.Items["android:fontFamily"]; got != s22bFontSubstitution["--p-mono"] {
			t.Errorf("PB-DS-3: `%s` is a --p-mono rule and %q renders it in %q. The terminal "+
				"peek, every timestamp and every command line are on this family; a sans "+
				"substitution there is the app silently abandoning the fixed advance the design "+
				"draws frames with.", sel, style.Name, got)
		}
	}
	// The design has 9 product mono rules: .pnav .live, .plabel, .prow .ag, .prow .ln b,
	// .sheet2 .ctx, .sheet2 .cmd, .sheet2 .bind, .tcard .h and .tcard .b. `.panelframe .cap`
	// looks like a tenth and is not -- it names the undeclared var(--mono) and is documentation
	// chrome (see s22bDocChromeSelectors).
	//
	// Asserting the count is what stops this test passing over a design source that stopped
	// parsing families -- in which case every rule would look sans-serif, the loop above would
	// iterate zero times, and the strongest-looking assertion in the file would be checking
	// nothing at all.
	const wantMonoRules = 9
	if monoRules != wantMonoRules {
		t.Errorf("PB-DS-3: %d of the design's text rules are --p-mono, want %d. If this is a "+
			"real design change the count moves with it; if it is 0, the family resolver stopped "+
			"working and every assertion in this test is vacuous.", monoRules, wantMonoRules)
	}
}

func sortedStyleNames(styles map[string]s22bStyle) []string {
	out := make([]string, 0, len(styles))
	for name := range styles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sortedClassifiedNames(styles map[string]s22bClassified) []string {
	out := make([]string, 0, len(styles))
	for name := range styles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
