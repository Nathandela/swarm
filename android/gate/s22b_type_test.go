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

	// PB-DS-2 counts 18 product styles. It is asserted on BOTH sides: a design that grew a 19th
	// rule and a type.xml that grew a 19th style are different failures with the same count.
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
		t.Errorf("PB-DS-2: %s defines %d TextAppearance.Swarm.* styles, want %d",
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

		s22bAssertStyleMatches(t, name, style, spec)
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

// s22bAssertStyleMatches compares one style against the CSS rule it descends from.
func s22bAssertStyleMatches(t *testing.T, name string, style s22bStyle, spec s22bTypeSpec) {
	t.Helper()
	where := fmt.Sprintf("%s:%d %s (origin `%s`)",
		mustRel(t, s22bTypePath(t)), style.Line, name, spec.Selector)

	// SIZE. sp rather than dp: PB-DS-12's floor is that text scales with the user's setting,
	// and a design system that ships dp text has decided against accessibility by default.
	if raw, ok := style.Items["android:textSize"]; !ok {
		t.Errorf("PB-DS-2: %s declares no android:textSize; the design says %gpx",
			where, spec.SizePx)
	} else if !strings.HasSuffix(raw, "sp") {
		t.Errorf("PB-DS-2: %s has android:textSize=%q. Text must be sp: dp text does not respond "+
			"to the user's font-size setting, and PB-DS-12 makes that a floor.", where, raw)
	} else if got, err := strconv.ParseFloat(strings.TrimSuffix(raw, "sp"), 64); err != nil {
		t.Errorf("PB-DS-2: %s has android:textSize=%q, which is not a number", where, raw)
	} else if got != spec.SizePx {
		t.Errorf("PB-DS-2: %s is %gsp; the design says %gpx. CSS px in the 386x812 mock is "+
			"Android dp/sp at 1:1 -- there is no scaling factor to explain the difference.",
			where, got, spec.SizePx)
	}

	// WEIGHT. --p-display-wt is 650, which is reachable only because minSdk 33 resolves
	// textFontWeight against the platform's variable Roboto (ADR-007 B134 decision 2 and 4).
	if raw, ok := style.Items["android:textFontWeight"]; !ok {
		t.Errorf("PB-DS-2: %s declares no android:textFontWeight; the design says %d",
			where, spec.Weight)
	} else if got, err := strconv.Atoi(raw); err != nil {
		t.Errorf("PB-DS-2: %s has android:textFontWeight=%q, which is not a number", where, raw)
	} else if got != spec.Weight {
		t.Errorf("PB-DS-2: %s is weight %d; the design says %d", where, got, spec.Weight)
	}

	// TRACKING. The one unit-identical row in the whole conversion: Android's letterSpacing is
	// em, as CSS's is. It is stated even when zero, so the join is total and a style that simply
	// forgot tracking is distinguishable from one whose design has none.
	if raw, ok := style.Items["android:letterSpacing"]; !ok {
		t.Errorf("PB-DS-2: %s declares no android:letterSpacing; the design says %gem. State it "+
			"even at 0, or a forgotten value and a deliberate `normal` look identical.",
			where, spec.TrackingEm)
	} else if got, err := strconv.ParseFloat(raw, 64); err != nil {
		t.Errorf("PB-DS-2: %s has android:letterSpacing=%q, which is not a number", where, raw)
	} else if math.Abs(got-spec.TrackingEm) > 1e-9 {
		t.Errorf("PB-DS-2: %s is %gem; the design says %gem", where, got, spec.TrackingEm)
	}

	// FAMILY, through PB-DS-3's recorded substitution.
	wantFamily, ok := s22bFontSubstitution[spec.Family]
	if !ok {
		t.Errorf("PB-DS-3: %s descends from a rule whose family is %s, for which ADR-007 B134 "+
			"records no substitution", where, spec.Family)
	} else if raw, ok := style.Items["android:fontFamily"]; !ok {
		t.Errorf("PB-DS-3: %s declares no android:fontFamily; %s substitutes to %q",
			where, spec.Family, wantFamily)
	} else if raw != wantFamily {
		t.Errorf("PB-DS-3: %s has android:fontFamily=%q; %s substitutes to %q (ADR-007 B134 "+
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
		t.Errorf("PB-DS-2: %s declares android:lineHeight=%q, and the design rule declares no "+
			"line-height at all. An invented leading is a design decision made in an XML file.",
			where, raw)
	case spec.LineHeight != 0 && !declared:
		t.Errorf("PB-DS-2: %s declares no android:lineHeight; the design says %g x %gpx = %gsp",
			where, spec.LineHeight, spec.SizePx, spec.LineHeight*spec.SizePx)
	case spec.LineHeight != 0:
		want := spec.LineHeight * spec.SizePx
		if !strings.HasSuffix(raw, "sp") {
			t.Errorf("PB-DS-2: %s has android:lineHeight=%q; it must be sp so leading scales "+
				"with the text it leads", where, raw)
		} else if got, err := strconv.ParseFloat(strings.TrimSuffix(raw, "sp"), 64); err != nil {
			t.Errorf("PB-DS-2: %s has android:lineHeight=%q, which is not a number", where, raw)
		} else if math.Abs(got-want) > 1e-9 {
			t.Errorf("PB-DS-2: %s has line height %gsp; the design says %g x %gpx = %gsp",
				where, got, spec.LineHeight, spec.SizePx, want)
		}
	}
}

// TestPBDS2_NoTextStyleCarriesAColour protects the scoping decision this file's header states.
func TestPBDS2_NoTextStyleCarriesAColour(t *testing.T) {
	styles := s22bStyles(t)
	for _, name := range sortedStyleNames(styles) {
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
