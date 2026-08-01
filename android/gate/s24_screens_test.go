package gate

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// FAILING-FIRST (TDD RED, GG-5) for PB-DS-11 and the source half of PB-DS-6 / PB-DS-9.
//
// WHAT THIS FILE IS FOR. PB-DS-6 was recorded NOT MET on the ground that the kit had ZERO
// production call sites: twelve components, four rounds of audit behind them, and three surface
// files importing none of them. A requirement in that state cannot be closed by writing more
// components, and it cannot be closed by a claim either -- what closes it is a fence that fails
// when the app stops being built out of the kit. That is this file.
//
// PB-DS-11 IS THE PART THAT MAKES THE OTHERS CHECKABLE, and it was reassigned S23 -> S24 for a
// reason worth restating here: every existing violation lives in the three surface files S24
// rewrites, and the requirement forbids allowlisting them ("existing violations are fixed, not
// allowlisted"). Owned by S23 it was satisfiable only by the allowlist its own text prohibits.
// There is therefore NO exemption list below beyond the two directories the requirement itself
// names -- `theme/`, which is where visual constants are allowed to live, and `ui/kit/`, which
// android/gate/s23_kit_test.go fences with a stricter rule of its own.
//
// THE THREE SCANS ARE DELIBERATELY DIFFERENT STRENGTHS, because the requirements are:
//
//   - PB-DS-11 over ALL production Kotlin and XML: no colour literal, no raw dimension, no
//     `Typeface.` and no `Color.`. This is the fence on the whole app.
//   - PB-DS-6 over the SCREEN package: fenced to component calls plus layout, which is stricter
//     -- a screen may not name a colour, a dimension, a radius or a typeface AT ALL, not even
//     through a resource. This is the fence that makes "the kit is the only way a screen is
//     built" mean something.
//   - PB-DS-9 over the inbox: every `status.Group` the roster can carry has a section, in the
//     order the model declares, with its own copy.
//
// EVERY SCAN CARRIES A NEGATIVE CONTROL that feeds a perturbed source to the SAME function the
// real assertion calls. A control that rebuilds the comparison inline proves something about the
// copy and nothing about the assertion; this package has shipped that mistake before.

// ---------------------------------------------------------------------------
// The tree, and the two directories the requirement exempts.
// ---------------------------------------------------------------------------

func s24KotlinRoot(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "kotlin")
}

func s24ResRoot(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "res")
}

// s24ScreenPackage is where a recomposed screen lives. PB-DS-9 puts the triage inbox first.
const s24ScreenPackage = "dev/swarm/phone/ui/screens"

// s24ThemeExempt and s24KitExempt are the ONLY exempt paths, and both are named by the
// requirements rather than chosen here. PB-DS-11: "a gate scans all production Kotlin and XML
// outside `theme/`". PB-DS-6 assigns the kit its own, stricter fence in s23_kit_test.go -- a hex
// literal, a `Typeface.` reference or a `setTextSize` call in any kit file already fails there,
// so re-scanning it would be a second opinion rather than a second fence.
const (
	s24ThemeExempt = "dev/swarm/phone/theme"
	s24KitExempt   = "dev/swarm/phone/ui/kit"
)

// s24ProductionKotlin returns every production Kotlin source PB-DS-11 covers, repo-relative
// slash path -> source, with the two exempt directories removed.
//
// IT FAILS ON AN EMPTY RESULT. A scan that found no files would report every fence below as
// clean, which is the failure mode this whole slice exists to answer.
func s24ProductionKotlin(t *testing.T) map[string]string {
	t.Helper()
	root := s24KotlinRoot(t)
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".kt") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, s24ThemeExempt+"/") || strings.HasPrefix(rel, s24KitExempt+"/") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("PB-DS-11: walking %s: %v", mustRel(t, root), err)
	}
	if len(out) == 0 {
		t.Fatalf("PB-DS-11: no production Kotlin found under %s; every fence in this file "+
			"would pass vacuously", mustRel(t, root))
	}
	return out
}

// s24ProductionXML returns the production XML PB-DS-11 covers: everything under res/ EXCEPT
// res/values*, which IS the theme -- the one place a visual constant is allowed to be written.
func s24ProductionXML(t *testing.T) map[string]string {
	t.Helper()
	root := s24ResRoot(t)
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".xml") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		// res/values, res/values-night, ... -- the theme.
		if strings.HasPrefix(rel, "values") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("PB-DS-11: walking %s: %v", mustRel(t, root), err)
	}
	return out
}

// ---------------------------------------------------------------------------
// PB-DS-11: no visual constant may enter the app except through the theme.
// ---------------------------------------------------------------------------

// s24ColourLiteral is an ARGB or RGB literal in either of the two spellings Kotlin and XML use.
var s24ColourLiteral = regexp.MustCompile(`(?i)(?:#|0x)([0-9a-f]{6}|[0-9a-f]{8})\b`)

// s24TypefaceReach is a font NAMED in code. `Typeface.MONOSPACE` and `Typeface.BOLD` are the two
// this app shipped; `setTypeface`, `fontFamily` and `textStyle` are the other ways to say it.
var s24TypefaceReach = []*regexp.Regexp{
	regexp.MustCompile(`\bTypeface\.`),
	regexp.MustCompile(`\bsetTypeface\s*\(`),
	regexp.MustCompile(`\btypeface\s*=`),
	regexp.MustCompile(`android:fontFamily`),
	regexp.MustCompile(`android:textStyle`),
}

// s24XMLTypefaceReach is the same fault in the only two spellings XML has for it.
var s24XMLTypefaceReach = []*regexp.Regexp{
	regexp.MustCompile(`android:fontFamily`),
	regexp.MustCompile(`android:textStyle`),
}

// s24ColourReach is `android.graphics.Color` reached by name. It is separate from the literal
// scan because `Color.WHITE` carries no hex and is exactly as much a colour chosen in code.
//
// THE KIT USES `Color.WHITE` LEGITIMATELY and is exempt for it -- `--p-card-fx`'s RGB IS white,
// and s23_kit_test.go recomputes the alpha beside it from the token. Outside the kit there is no
// such join, so a named platform colour is a colour picked at a call site.
var s24ColourReach = regexp.MustCompile(`\bColor\.[A-Z]`)

// s24DimensionCall is a call whose arguments are LENGTHS. A literal in one is a raw pixel value:
// the constant PB-DS-1 replaced was `PADDING = 24` in pixels, rendering at 8 dp on a 3x handset,
// and `SCANNER_HEIGHT = 720` was still there when this gate was written.
//
// LAYOUT PARAMS ARE IN THE FAMILY, and they are the reason this list is not the s23 one. A
// reviewer found that `LinearLayout.LayoutParams(21, 21)` -- raw pixels straight into a layout
// param -- was invisible to every scan in this repository: s23's fence reads padding and margin
// setters only, so a component could size itself in pixels and stay green. Both halves of a size
// are dimensions, so the first two arguments of a LayoutParams constructor are checked and the
// rest are not: the third is a weight or a gravity, and `LayoutParams(0, WRAP, 1f)` is correct.
var s24DimensionCall = regexp.MustCompile(
	`\b(set(?:Padding|PaddingRelative|Margins|MarginStart|MarginEnd)|setTextSize|LayoutParams)\s*\(`)

// s24LayoutParamsCall is the subset whose trailing arguments are not lengths.
var s24LayoutParamsCall = "LayoutParams"

// s24AssignedDimension is a length written as an assignment rather than passed as an argument.
var s24AssignedDimension = []*regexp.Regexp{
	regexp.MustCompile(`\btextSize\s*=\s*(-?[0-9.]+f?)\b`),
	regexp.MustCompile(`\b(?:topMargin|bottomMargin|marginStart|marginEnd|leftMargin|rightMargin)\s*=\s*(-?[0-9.]+f?)\b`),
	regexp.MustCompile(`\b(?:cornerRadius|strokeWidth|letterSpacing)\s*=\s*(-?[0-9.]+f?)\b`),
}

// s24LiteralNumber is a whole argument that is nothing but a number -- `24`, `28f`, `-3`.
// Applied to one already-split, already-trimmed top-level argument.
var s24LiteralNumber = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?[fF]?$`)

// s24IsZero recognises the one literal the requirement allows anywhere a length is spent.
//
// A ZERO HAS NO UNIT. 0 px and 0 dp are the same distance, so it cannot be wrong, and the design
// states plenty of them (`.prows { padding: 0 12px }`). s23_kit_test.go arrived at the same rule
// for the same reason; it is restated rather than shared because the two gates fence different
// trees and a shared helper would couple them.
func s24IsZero(arg string) bool {
	switch strings.TrimSpace(arg) {
	case "0", "0f", "0F", "0.0", "0.0f", "0.0F", "-0":
		return true
	}
	return false
}

// xmlMarkupOnly strips `<!-- -->` comments, leaving attribute values intact.
//
// IT IS THE SAME RULE kotlinCodeOnly ENFORCES AND FOR THE SAME REASON, and this fence found out
// the hard way: the very commit that replaced `android:fillColor="#FFFFFFFF"` with a platform
// resource EXPLAINED the replacement in a comment that quoted the literal, and the scan reported
// the file still dirty. A fence a comment can trip is a fence that punishes documentation; a
// fence a comment can satisfy is one that documentation turns off. Both are the same defect.
func xmlMarkupOnly(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	for {
		start := strings.Index(src, "<!--")
		if start < 0 {
			out.WriteString(src)
			return out.String()
		}
		out.WriteString(src[:start])
		end := strings.Index(src[start:], "-->")
		if end < 0 {
			return out.String()
		}
		// Newlines survive so reported positions and the shape of the file do.
		out.WriteString(strings.Repeat("\n", strings.Count(src[start:start+end], "\n")))
		src = src[start+end+len("-->"):]
	}
}

// s24VisualConstantFaults reports every visual constant one source spends outside the theme.
//
// IT READS CODE AND NOT PROSE. kotlinCodeOnly strips comments first, for the reason that helper
// records: a fence a comment can satisfy is a fence the next thorough comment turns off. It is
// also what lets THIS file's own documentation quote `Typeface.MONOSPACE` and `#FF08090A`
// without the gate finding itself.
//
// @return one line per fault, sorted, empty when the source spends none.
func s24VisualConstantFaults(name, src string, kotlin bool) []string {
	var faults []string
	code := src
	if kotlin {
		code = kotlinCodeOnly(src)
	} else {
		code = xmlMarkupOnly(src)
	}

	for _, m := range s24ColourLiteral.FindAllString(code, -1) {
		faults = append(faults, name+": colour literal "+m+
			" -- a colour may only enter the app as an R.color the token join covers")
	}
	if kotlin {
		for _, re := range s24TypefaceReach {
			for _, m := range re.FindAllString(code, -1) {
				faults = append(faults, name+": font named in code (`"+strings.TrimSpace(m)+
					"`) -- a typeface is a TextAppearance in res/values/type.xml, never a call site's choice")
			}
		}
		for _, m := range s24ColourReach.FindAllString(code, -1) {
			faults = append(faults, name+": platform colour `"+m+
				"` -- a colour picked at a call site has no join to the token origin")
		}
	} else {
		// XML has no `Typeface.`; what it has is the attribute forms.
		for _, re := range s24XMLTypefaceReach {
			for _, m := range re.FindAllString(code, -1) {
				faults = append(faults, name+": font named in XML (`"+m+"`)")
			}
		}
	}

	if !kotlin {
		sort.Strings(faults)
		return faults
	}

	for _, loc := range s24DimensionCall.FindAllStringSubmatchIndex(code, -1) {
		call := code[loc[2]:loc[3]]
		args := s23CallArguments(code, loc[1]-1)
		if args == nil {
			faults = append(faults, name+": unbalanced parentheses after `"+call+
				"`, so the gate cannot read its arguments and would report it clean")
			continue
		}
		if call == s24LayoutParamsCall && len(args) > 2 {
			// Width and height are lengths; a weight or a gravity is not.
			args = args[:2]
		}
		for i, arg := range args {
			if !s24LiteralNumber.MatchString(arg) || s24IsZero(arg) {
				continue
			}
			faults = append(faults, name+": raw dimension `"+arg+"` in argument "+
				strconv.Itoa(i)+" of `"+call+"(...)` -- a length may only come from R.dimen, the "+
				"design scale, or a window inset")
		}
	}
	for _, re := range s24AssignedDimension {
		for _, m := range re.FindAllStringSubmatch(code, -1) {
			if s24IsZero(m[1]) {
				continue
			}
			faults = append(faults, name+": raw dimension `"+strings.TrimSpace(m[0])+
				"` -- a length may only come from R.dimen or the design scale")
		}
	}

	sort.Strings(faults)
	return faults
}

// TestPBDS11_NoVisualConstantEntersTheAppExceptThroughTheTheme is the fence itself.
func TestPBDS11_NoVisualConstantEntersTheAppExceptThroughTheTheme(t *testing.T) {
	var faults []string
	scanned := 0
	for name, src := range s24ProductionKotlin(t) {
		scanned++
		faults = append(faults, s24VisualConstantFaults(name, src, true)...)
	}
	for name, src := range s24ProductionXML(t) {
		scanned++
		faults = append(faults, s24VisualConstantFaults("res/"+name, src, false)...)
	}
	if scanned == 0 {
		t.Fatal("PB-DS-11: nothing was scanned")
	}
	sort.Strings(faults)
	if len(faults) > 0 {
		t.Errorf("PB-DS-11: %d visual constants enter the app outside the theme, across %d "+
			"production sources.\n\nThe requirement's own words: \"an ARGB literal, a dp literal, "+
			"a font name or a radius appearing in surface code is the defect, INDEPENDENT of "+
			"whether its value is currently correct\", and \"existing violations are FIXED, not "+
			"allowlisted\".\n\n%s",
			len(faults), scanned, strings.Join(faults, "\n"))
	}
}

// TestPBDS11_TheScanSeesEachClassOfViolation is the negative control.
//
// IT FEEDS A PERTURBED SOURCE TO THE SAME FUNCTION the assertion above calls, which is the whole
// requirement: a control that re-implements the scan proves the copy works. Each case is one
// violation class, and the `LayoutParams(21, 21)` row is there by name -- a reviewer found that
// exact call invisible to every scan in this repository.
func TestPBDS11_TheScanSeesEachClassOfViolation(t *testing.T) {
	cases := []struct {
		what string
		src  string
	}{
		{"an ARGB literal", `val ink = 0xFF53CE7C.toInt()`},
		{"a CSS-spelled colour", `val ink = "#53CE7C"`},
		{"a named font", `view.typeface = Typeface.MONOSPACE`},
		{"a bold style", `setTypeface(typeface, Typeface.BOLD)`},
		{"a platform colour", `paint.color = Color.WHITE`},
		{"a raw text size", `label.textSize = 28f`},
		{"a raw padding", `view.setPadding(24, 24, 24, 24)`},
		{"a raw layout param", `view.layoutParams = LinearLayout.LayoutParams(21, 21)`},
		{"a raw height beside a constant", `LinearLayout.LayoutParams(MATCH, 720)`},
		{"a raw margin assignment", `params.topMargin = 12`},
		{"a raw radius assignment", `shape.cornerRadius = 9f`},
	}
	for _, c := range cases {
		if got := s24VisualConstantFaults("perturbed.kt", c.src, true); len(got) == 0 {
			t.Errorf("the scan is blind to %s: `%s` produced no fault, so every clean run of "+
				"TestPBDS11_NoVisualConstantEntersTheAppExceptThroughTheTheme is about nothing",
				c.what, c.src)
		}
	}
}

// TestPBDS11_TheXMLScanSeesAttributesAndNotComments is the XML half's negative control, in both
// directions -- and the second direction is not hypothetical.
//
// A comment stripper is the easiest thing in this file to get catastrophically wrong: one that
// removed too much would silence the whole XML scan while every assertion stayed green, which is
// the "reads as coverage and is not" failure this package keeps meeting. So the control asserts
// that a literal in a real attribute is still found AFTER stripping, not merely that a commented
// one is ignored.
func TestPBDS11_TheXMLScanSeesAttributesAndNotComments(t *testing.T) {
	const dirty = `<vector>
    <!-- It used to be #FFFFFFFF, and the platform discards it anyway. -->
    <path android:fillColor="#FF53CE7C" />
</vector>`
	faults := s24VisualConstantFaults("dirty.xml", dirty, false)
	if len(faults) != 1 {
		t.Errorf("the XML scan reported %d faults over one attribute literal and one commented "+
			"literal; it must see exactly the attribute:\n%s", len(faults), strings.Join(faults, "\n"))
	}
	if len(faults) > 0 && !strings.Contains(faults[0], "#FF53CE7C") {
		t.Errorf("the XML scan found the literal in the COMMENT rather than the one in the "+
			"attribute: %s", faults[0])
	}

	const clean = `<vector>
    <!-- PB-DS-11: it was #FFFFFFFF. -->
    <path android:fillColor="@android:color/white" />
</vector>`
	if got := s24VisualConstantFaults("clean.xml", clean, false); len(got) > 0 {
		t.Errorf("the XML scan reports a file whose only literal is inside a comment:\n%s",
			strings.Join(got, "\n"))
	}

	// The stripper itself: markup out, comments gone, nothing else eaten.
	if got := xmlMarkupOnly(dirty); !strings.Contains(got, "android:fillColor") ||
		strings.Contains(got, "platform discards") {
		t.Errorf("xmlMarkupOnly does not leave markup and remove comments; it produced:\n%s", got)
	}
}

// TestPBDS11_TheScanAcceptsWhatTheRequirementAllows is the control in the other direction.
//
// A fence that fails on everything is as useless as one that fails on nothing, and this one has
// three shapes it MUST accept or the app cannot be written at all: a length read from R.dimen, a
// zero (which has no unit), and a window inset (which is a runtime measurement, not a constant).
func TestPBDS11_TheScanAcceptsWhatTheRequirementAllows(t *testing.T) {
	cases := []struct {
		what string
		src  string
	}{
		{"a scale step", `setPadding(Kit.dimenPx(context, R.dimen.swarm_space_12), 0, 0, 0)`},
		{"a layout constant", `LinearLayout.LayoutParams(MATCH, WRAP)`},
		{"a weight", `LinearLayout.LayoutParams(0, WRAP, 1f)`},
		{"a window inset", `view.setPadding(bars.left, bars.top, bars.right, bars.bottom)`},
		{"a resource text appearance", `setTextAppearance(R.style.TextAppearance_Swarm_Title_Row)`},
		{"a documented literal in a comment", "// This was `setPadding(24, 24, 24, 24)` in raw pixels."},
	}
	for _, c := range cases {
		if got := s24VisualConstantFaults("allowed.kt", c.src, true); len(got) > 0 {
			t.Errorf("the scan rejects %s, which the requirement allows: `%s`\n%s",
				c.what, c.src, strings.Join(got, "\n"))
		}
	}
}

// ---------------------------------------------------------------------------
// PB-DS-6: the kit is the only way a screen is built.
// ---------------------------------------------------------------------------

// s24KitImport is an import of the component kit from outside it.
var s24KitImport = regexp.MustCompile(`import\s+dev\.swarm\.phone\.ui\.kit\.([A-Za-z][A-Za-z0-9_]*)`)

// s24InboxComponents is the claim: the components inventory C1 composes the triage inbox from.
//
// It is a CLAIM AND NOT A SURVEY. Each entry names a factory the kit already ships and the part
// of the recorded composition it renders, so a screen that quietly stops rendering one -- the
// section labels, say, which is the failure PB-DS-9 describes -- fails here rather than looking
// like a tidier screen.
var s24InboxComponents = map[string]string{
	"navHeader":    "C1.1 `.pnav` -- the display title and the live counter",
	"filterChip":   "C1.2 `.chips` -- the scope bar",
	"chipRow":      "C1.2 `.chips` -- the scope bar's container",
	"sectionLabel": "C1.3 `.plabel` -- one heading per status.Group, INCLUDING the empty ones",
	"sessionList":  "C1.3 `.prows` -- the rows' container, which is where the side padding lives",
	"sessionRow":   "C1.3 `.prow` -- one session, with its dot, its need line and its workbar",
	"tabBar":       "C1.4 `.ptabs` -- the four tabs and the NeedsInput badge",
}

// s24ScreenSources returns the recomposed screens, repo-relative path -> source.
func s24ScreenSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, src := range s24ProductionKotlin(t) {
		if strings.HasPrefix(name, s24ScreenPackage+"/") {
			out[name] = src
		}
	}
	return out
}

// TestPBDS6_TheKitHasProductionCallSites is the requirement's own recorded defect, inverted.
//
// PB-DS-6 was marked NOT MET on exactly this: "the kit has ZERO production call sites ... across
// ~11.6k inserted lines the only user-visible change is one padding moving from 24px to 24dp".
// A component library nothing renders is not a design system, and no amount of testing the
// components changes that -- so this asks the one question the audit asked.
func TestPBDS6_TheKitHasProductionCallSites(t *testing.T) {
	reached := map[string][]string{}
	for name, src := range s24ProductionKotlin(t) {
		code := kotlinCodeOnly(src)
		for _, m := range s24KitImport.FindAllStringSubmatch(code, -1) {
			reached[m[1]] = append(reached[m[1]], name)
		}
	}
	if len(reached) == 0 {
		t.Fatalf("PB-DS-6: no production Kotlin outside ui/kit/ imports a single kit symbol, so " +
			"the kit has ZERO production call sites and nothing a user sees is built from it. " +
			"This is the state the requirement was recorded NOT MET in.")
	}

	var missing []string
	for factory, role := range s24InboxComponents {
		if len(reached[factory]) == 0 {
			missing = append(missing, "  "+factory+" -- "+role)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("PB-DS-6 / PB-DS-9: the triage inbox does not reach %d of the components its "+
			"recorded composition is made of:\n%s\n\nA screen that composes some of the kit and "+
			"hand-builds the rest is the copy-paste drift the requirement exists to prevent.",
			len(missing), strings.Join(missing, "\n"))
	}
}

// s24ScreenForbidden is what a screen may NOT name. PB-DS-6's evidence: "surface files contain no
// colour, dimension, radius or typeface reference -- a gate fences them to component calls plus
// layout."
//
// IT IS STRICTER THAN PB-DS-11 ON PURPOSE, and the difference is the whole claim. PB-DS-11 stops
// a screen INVENTING a value; this stops it CHOOSING one. `setTextAppearance(R.style.…)` is
// perfectly good style discipline and it is still a screen deciding what type a thing is, which
// is the kit's decision to make -- a screen composes components and passes data.
var s24ScreenForbidden = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`\bR\.color\.`), "a colour resource"},
	{regexp.MustCompile(`\bR\.dimen\.`), "a dimension resource"},
	{regexp.MustCompile(`\bR\.style\.`), "a style resource"},
	{regexp.MustCompile(`\bsetTextAppearance\s*\(`), "a text appearance"},
	{regexp.MustCompile(`\bsetTextColor\s*\(`), "an ink"},
	{regexp.MustCompile(`\bsetTextSize\s*\(`), "a text size"},
	{regexp.MustCompile(`\bsetPadding(?:Relative)?\s*\(`), "a padding"},
	{regexp.MustCompile(`\bsetBackground(?:Color|Resource|Drawable)?\s*\(`), "a background"},
	{regexp.MustCompile(`\bbackground\s*=`), "a background"},
	{regexp.MustCompile(`\bGradientDrawable\b`), "a shape"},
	{regexp.MustCompile(`\bPaint\b`), "a paint"},
}

// s24ScreenFenceFaults reports every visual decision a screen source makes for itself.
func s24ScreenFenceFaults(name, src string) []string {
	var faults []string
	code := kotlinCodeOnly(src)
	for _, forbidden := range s24ScreenForbidden {
		for _, m := range forbidden.re.FindAllString(code, -1) {
			faults = append(faults, name+": names "+forbidden.what+" (`"+strings.TrimSpace(m)+
				"`). A screen composes components and passes data; the look is the kit's.")
		}
	}
	sort.Strings(faults)
	return faults
}

// TestPBDS6_TheScreenPackageIsFencedToComponentCallsPlusLayout is PB-DS-6's own evidence line.
func TestPBDS6_TheScreenPackageIsFencedToComponentCallsPlusLayout(t *testing.T) {
	screens := s24ScreenSources(t)
	if len(screens) == 0 {
		t.Fatalf("PB-DS-6 / PB-DS-9: there is no screen under %s. The requirement's first "+
			"clause is that the screens are RECOMPOSED on the kit, triage inbox first; with no "+
			"screen package the fence below has no subject and would pass vacuously.",
			s24ScreenPackage)
	}
	var faults []string
	for name, src := range screens {
		faults = append(faults, s24ScreenFenceFaults(name, src)...)
	}
	sort.Strings(faults)
	if len(faults) > 0 {
		t.Errorf("PB-DS-6: %d visual decisions are made in the screen package rather than in "+
			"the kit:\n%s", len(faults), strings.Join(faults, "\n"))
	}
}

// TestPBDS6_TheScreenFenceSeesEachClassOfViolation is that fence's negative control.
func TestPBDS6_TheScreenFenceSeesEachClassOfViolation(t *testing.T) {
	cases := []struct {
		what string
		src  string
	}{
		{"an ink chosen by the screen", `label.setTextColor(Kit.colour(context, R.color.swarm_hero))`},
		{"a padding chosen by the screen", `row.setPaddingRelative(pad, 0, pad, 0)`},
		{"a type style chosen by the screen", `label.setTextAppearance(R.style.TextAppearance_Swarm_Title_Row)`},
		{"a dimension read by the screen", `val pad = resources.getDimensionPixelSize(R.dimen.swarm_space_24)`},
		{"a background built by the screen", `view.background = GradientDrawable()`},
	}
	for _, c := range cases {
		if got := s24ScreenFenceFaults("perturbed.kt", c.src); len(got) == 0 {
			t.Errorf("the screen fence is blind to %s: `%s` produced no fault", c.what, c.src)
		}
	}
	// And the shapes a screen MUST be able to write, or it cannot compose anything.
	allowed := []string{
		`root.addView(sectionLabel(context, label))`,
		`val list = sessionList(context)`,
		`row.setOnClickListener { onSelect(id) }`,
		`LinearLayout(context).apply { orientation = LinearLayout.VERTICAL }`,
	}
	for _, src := range allowed {
		if got := s24ScreenFenceFaults("allowed.kt", src); len(got) > 0 {
			t.Errorf("the screen fence rejects composition itself: `%s`\n%s",
				src, strings.Join(got, "\n"))
		}
	}
}

// ---------------------------------------------------------------------------
// PB-DS-9: every Group is a section, and an empty section is still a section.
// ---------------------------------------------------------------------------

// s24TriageOrderRe reads the model's own declared order, so this gate never carries a second copy
// of it. TriageInbox.TRIAGE_ORDER is the authority: its KDoc argues the order, and a gate that
// transcribed the four strings would keep passing after the model changed its mind.
var s24TriageOrderRe = regexp.MustCompile(`(?s)TRIAGE_ORDER:\s*List<String>\s*=\s*\n?\s*listOf\(([^)]*)\)`)

// s24QuotedString pulls each "..." out of a Kotlin expression.
var s24QuotedString = regexp.MustCompile(`"([^"]*)"`)

// s24GroupLabelRow is one row of the screen's Group -> heading table.
var s24GroupLabelRow = regexp.MustCompile(`"([a-z_]+)"\s*to\s*"([^"]+)"`)

func s24TriageOrder(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(s24KotlinRoot(t), "dev/swarm/phone/ui/TriageInbox.kt")
	src := readFileOrFail(t, path, "PB-DS-9")
	m := s24TriageOrderRe.FindStringSubmatch(kotlinCodeOnly(src))
	if m == nil {
		t.Fatalf("PB-DS-9: cannot read TRIAGE_ORDER out of %s, so the section order this gate "+
			"checks would be a second copy written here", mustRel(t, path))
	}
	var order []string
	for _, q := range s24QuotedString.FindAllStringSubmatch(m[1], -1) {
		order = append(order, q[1])
	}
	if len(order) == 0 {
		t.Fatalf("PB-DS-9: TRIAGE_ORDER parsed as empty")
	}
	return order
}

// TestPBDS9_EveryTriageGroupHasASectionHeadingAndEmptyCopy is the "an empty section is still a
// section" criterion, at the source.
//
// TWO TABLES ARE REQUIRED AND BOTH ARE JOINED TO TRIAGE_ORDER: the heading a Group renders, and
// the copy an EMPTY section says. Dropping an empty section is the obvious implementation and it
// is wrong for a triage surface -- the model's own KDoc says why: the sections then move under
// the user as sessions change group, and "nothing is waiting on me", the most useful fact this
// screen can report, becomes indistinguishable from "that section scrolled away".
func TestPBDS9_EveryTriageGroupHasASectionHeadingAndEmptyCopy(t *testing.T) {
	order := s24TriageOrder(t)
	screens := s24ScreenSources(t)
	if len(screens) == 0 {
		t.Fatalf("PB-DS-9: there is no screen under %s to carry the section headings",
			s24ScreenPackage)
	}

	headings := map[string]string{}
	empties := map[string]string{}
	for _, src := range screens {
		code := kotlinCodeOnly(src)
		heads, empty, ok := s24SplitLabelTables(code)
		if !ok {
			continue
		}
		for k, v := range heads {
			headings[k] = v
		}
		for k, v := range empty {
			empties[k] = v
		}
	}

	var faults []string
	for _, group := range order {
		if headings[group] == "" {
			faults = append(faults, "  "+group+" has no section heading, so a Group the roster "+
				"can carry would render as an unlabelled block of rows")
		}
		if empties[group] == "" {
			faults = append(faults, "  "+group+" has no empty copy, so the section vanishes when "+
				"it is empty -- which is the failure PB-DS-9 names by name")
		}
	}
	for group := range headings {
		if !contains(order, group) {
			faults = append(faults, "  "+group+" has a heading and is not in TRIAGE_ORDER, so the "+
				"screen renders a section for a status.Group the model does not place")
		}
	}
	sort.Strings(faults)
	if len(faults) > 0 {
		t.Errorf("PB-DS-9: the triage inbox does not cover TRIAGE_ORDER %v:\n%s",
			order, strings.Join(faults, "\n"))
	}
}

// s24TableDeclaration finds a named table's DECLARATION -- `NAME: Map<..> = mapOf(` -- and not
// merely the first place the name is mentioned.
//
// THE OBVIOUS SPELLING IS WRONG HERE, which is why this is a regexp rather than a `strings.Index`
// on the name. A lookup (`SECTION_HEADINGS[group]`) mentions the identifier too, and if one of
// those came first the reader would take the `(` after it and parse an unrelated argument list --
// reporting a table with no rows, which the coverage check below would read as "no heading for any
// Group" or, worse, as a partial table if the mentions happened to line up. Matching the
// declaration form makes the subject unambiguous.
var s24TableDeclaration = regexp.MustCompile(
	`\b(SECTION_HEADINGS|EMPTY_SECTION_COPY)\b\s*(?::\s*Map<[^>]*>)?\s*=\s*mapOf\s*\(`)

// s24SplitLabelTables reads the screen's two Group-keyed tables. They are found by the names the
// screen gives them rather than by position, so reordering the file changes nothing here.
func s24SplitLabelTables(code string) (headings, empties map[string]string, ok bool) {
	headings = map[string]string{}
	empties = map[string]string{}
	for _, loc := range s24TableDeclaration.FindAllStringSubmatchIndex(code, -1) {
		into := headings
		if code[loc[2]:loc[3]] == "EMPTY_SECTION_COPY" {
			into = empties
		}
		// loc[1] is one past the `(` the pattern ends on.
		for _, arg := range s23CallArguments(code, loc[1]-1) {
			if m := s24GroupLabelRow.FindStringSubmatch(arg); m != nil {
				into[m[1]] = m[2]
				ok = true
			}
		}
	}
	return headings, empties, ok
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// TestPBDS9_TheSectionTableReaderSeesAMissingGroup is that check's negative control: the reader
// must actually be reading, and it must actually notice an absence.
func TestPBDS9_TheSectionTableReaderSeesAMissingGroup(t *testing.T) {
	full := `private val SECTION_HEADINGS = mapOf(
		"needs_input" to "Needs you",
		"ready_for_review" to "Ready for review",
		"completed" to "Done",
		"working" to "Working",
	)
	private val EMPTY_SECTION_COPY = mapOf(
		"needs_input" to "Nothing is waiting on you.",
		"ready_for_review" to "Nothing is waiting to be reviewed.",
		"completed" to "Nothing has finished yet.",
		"working" to "Nothing is running.",
	)`
	headings, empties, ok := s24SplitLabelTables(full)
	if !ok || len(headings) != 4 || len(empties) != 4 {
		t.Fatalf("the table reader cannot read a well-formed pair of tables: headings=%v empties=%v",
			headings, empties)
	}

	// The perturbation: the Working section dropped, which is precisely what "dropping the empty
	// section is the obvious implementation" produces.
	perturbed := strings.Replace(full, "\t\t\"working\" to \"Working\",\n", "", 1)
	headings, _, _ = s24SplitLabelTables(perturbed)
	if headings["working"] != "" {
		t.Errorf("the table reader still reports a heading for `working` after the row was "+
			"deleted, so the coverage check above would pass over a dropped section: %v", headings)
	}

	// And the reader must find the DECLARATION rather than the first mention. A lookup placed
	// above it is the arrangement that would make a `strings.Index` on the name parse an unrelated
	// argument list and report an empty table -- which reads as "the screen covers no Group".
	mentionFirst := `fun headingFor(g: String) = SECTION_HEADINGS[g]
	` + full
	headings, empties, ok = s24SplitLabelTables(mentionFirst)
	if !ok || len(headings) != 4 || len(empties) != 4 {
		t.Errorf("a lookup written above the declaration defeats the table reader: "+
			"headings=%v empties=%v", headings, empties)
	}
}
