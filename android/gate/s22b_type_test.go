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

// s22bFontSubstitution is ADR-007 B134 decision 2, as a map. The CSS font stacks name SF Pro and
// SF Mono, neither of which is licensable off Apple; the decision is the platform families and
// zero bundled assets.
//
// IT IS A CONSTANT HERE AND IT HAS TO BE. Unlike every other number in this slice, this one
// cannot be computed from the artifact -- the artifact names fonts that do not exist on the
// target platform, which is the whole reason a decision was required. So the constant is the
// DECISION, and TestPBDS3_TheSubstitutionIsTheOneTheADRRecords joins it back to the ADR text so
// that changing it here without changing the record fails.
var s22bFontSubstitution = map[string]string{
	"--p-font": "sans-serif",
	"--p-mono": "monospace",
}

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
	const wantStyles = 18
	if len(design) != wantStyles {
		var got []string
		for sel := range design {
			got = append(got, sel)
		}
		sort.Strings(got)
		t.Errorf("PB-DS-2: the design source declares %d product text styles, and the "+
			"requirement is written against %d. Either a rule was added to the artifact or the "+
			"doc-chrome exclusion list is wrong.\n\t%s",
			len(design), wantStyles, strings.Join(got, "\n\t"))
	}
	if len(styles) != wantStyles {
		t.Errorf("PB-DS-2: %s defines %d TextAppearance.Swarm.* styles citing `origin:`, want %d",
			mustRel(t, s22bTypePath(t)), len(styles), wantStyles)
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

	// UNIMPLEMENTED ROW FAILS.
	var orphan []string
	for sel := range design {
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
			"decision 2). Every text style in this app renders a substitute for a font that is "+
			"not licensable off Apple; the point of the decision is that the substitute is "+
			"chosen once and written down.", where, raw, spec.Family, wantFamily)
	}

	// LINE HEIGHT, where the design gives one. CSS's unitless multiplier has no Android form --
	// android:lineHeight is an absolute dimension -- so the product is the value, computed here
	// rather than transcribed.
	raw, declared := style.Items["android:lineHeight"]
	switch {
	case spec.LineHeight == 0 && declared:
		fault("PB-DS-2: %s declares android:lineHeight=%q, and the design fact it descends from "+
			"declares no line-height at all. An invented leading is a design decision made in an "+
			"XML file.", where, raw)
	case spec.LineHeight != 0 && !declared:
		fault("PB-DS-2: %s declares no android:lineHeight; the design says %g x %gpx = %gsp",
			where, spec.LineHeight, spec.SizePx, spec.LineHeight*spec.SizePx)
	case spec.LineHeight != 0:
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
		"the mono family":                 {"android:fontFamily": "monospace"},
		"no size at all":                  {"android:textSize": ""},
		"no tracking at all":              {"android:letterSpacing": ""},
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

// ---------------------------------------------------------------------------
// PB-DS-3.
// ---------------------------------------------------------------------------

// TestPBDS3_TheSubstitutionIsTheOneTheADRRecords joins the constant in this file to the record.
func TestPBDS3_TheSubstitutionIsTheOneTheADRRecords(t *testing.T) {
	adr := readFileOrFail(t,
		filepath.Join(repoRoot(t), filepath.FromSlash("docs/adr/ADR-007-remote-access.md")),
		"PB-DS-3")

	start := strings.Index(adr, "## B134.")
	if start < 0 {
		t.Fatalf("PB-DS-3: ADR-007 has no B134 entry; the font decision has no record and the " +
			"substitution table in this file would be a choice made in a test")
	}
	entry := adr[start:]
	for token, family := range s22bFontSubstitution {
		if !strings.Contains(entry, "`"+family+"`") {
			t.Errorf("PB-DS-3: ADR-007 B134 does not record %q as the substitute for %s. The "+
				"gate's table and the record must be the same decision, or the decision is "+
				"whatever the gate happens to say.", family, token)
		}
	}
	if !strings.Contains(entry, "zero bundled assets") {
		t.Errorf("PB-DS-3: ADR-007 B134 no longer records `zero bundled assets`, which is the " +
			"half of the decision the assertion below enforces")
	}
}

// TestPBDS3_NoFontIsBundled is the other half of the decision, and the only half with a
// mechanical form: "zero bundled assets".
//
// IT SCANS res/ AND assets/ WHOLE rather than res/font/ alone, and that is the difference
// between this assertion and a vacuous one. `res/font/` does not exist, so a scan pointed at it
// walks nothing, finds nothing, and is green -- and would be equally green if the walk were
// broken, if the extension list were empty, or if a font were sitting one directory away. Walking
// the trees that DO have files gives the check a population to be right about, and the file count
// below is asserted so an empty walk reports itself.
func TestPBDS3_NoFontIsBundled(t *testing.T) {
	roots := []string{
		filepath.Join(appModule(t), "src", "main", "res"),
		filepath.Join(appModule(t), "src", "main", "assets"),
	}
	// A binary face anywhere, or ANYTHING under res/font/ -- that directory exists for exactly
	// one purpose, so an XML family definition in it counts as a bundled font while the identical
	// extension under res/values/ does not.
	binary := map[string]bool{".ttf": true, ".otf": true, ".ttc": true, ".woff": true, ".woff2": true}
	fontDir := string(filepath.Separator) + "font" + string(filepath.Separator)

	var found []string
	walked := 0
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil //nolint:nilerr // a missing assets/ IS part of the passing state
			}
			walked++
			if binary[strings.ToLower(filepath.Ext(path))] || strings.Contains(path, fontDir) {
				found = append(found, mustRel(t, path))
			}
			return nil
		})
	}
	if walked == 0 {
		t.Fatalf("PB-DS-3: walked no files under %s; a clean report would mean nothing",
			mustRel(t, roots[0]))
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Errorf("PB-DS-3: the decision is the platform families with ZERO bundled assets, and "+
			"these are bundled:\n\t%s\nBundling JetBrains Mono is the recorded upgrade path for "+
			"the box-drawing residual, and taking it is a decision that belongs in ADR-007 B134 "+
			"rather than in a resource directory.", strings.Join(found, "\n\t"))
	}
	t.Logf("PB-DS-3: %d files under res/ and assets/, %d of them fonts", walked, len(found))
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
