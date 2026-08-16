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

// s24AssignedName is the same set of assignments with a NAME on the right instead of a number --
// `textSize = SAS_TEXT_SP`, which is how the one this slice deleted was actually written. The
// capture is the identifier, resolved through [s24FileConstants].
var s24AssignedName = []*regexp.Regexp{
	regexp.MustCompile(`\btextSize\s*=\s*([A-Za-z_][A-Za-z0-9_]*)\b`),
	regexp.MustCompile(`\b(?:topMargin|bottomMargin|marginStart|marginEnd|leftMargin|rightMargin)\s*=\s*([A-Za-z_][A-Za-z0-9_]*)\b`),
	regexp.MustCompile(`\b(?:cornerRadius|strokeWidth|letterSpacing)\s*=\s*([A-Za-z_][A-Za-z0-9_]*)\b`),
}

// s24LiteralNumber is a whole argument that is nothing but a number -- `24`, `28f`, `-3`.
// Applied to one already-split, already-trimmed top-level argument.
var s24LiteralNumber = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?[fF]?$`)

// s24NamedNumber is a file-local constant whose value is a bare number.
//
// EVERY VIOLATION THIS SLICE FIXED WAS ONE HOP BEHIND A NAME, and a fence that only reads literals
// at the call site would have found none of them. `PADDING = 24`, `SCANNER_HEIGHT = 720` and
// `SAS_TEXT_SP = 28f` were all `const val`s in a companion object, spent as
// `setPadding(pad, ...)`, `LayoutParams(MATCH, SCANNER_HEIGHT)` and `textSize = SAS_TEXT_SP` --
// which is to say the obvious way to write a raw dimension is also the way that hides it. So an
// identifier argument is resolved against this table before it is passed over.
//
// ONE HOP AND NO MORE. A constant defined in another file, or computed, is not followed: that
// needs a type checker, and a heuristic that guessed would fail in both directions. What is
// covered is the shape all three defects had.
var s24NamedNumber = regexp.MustCompile(
	`(?m)^\s*(?:private\s+|internal\s+)?(?:const\s+)?val\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?::\s*[A-Za-z]+\s*)?=\s*(-?[0-9]+(?:\.[0-9]+)?[fF]?)\s*$`)

// s24FileConstants maps a source's file-local numeric constants to their values.
func s24FileConstants(code string) map[string]string {
	out := map[string]string{}
	for _, m := range s24NamedNumber.FindAllStringSubmatch(code, -1) {
		out[m[1]] = m[2]
	}
	return out
}

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

	constants := s24FileConstants(code)
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
			value, named := arg, false
			if !s24LiteralNumber.MatchString(arg) {
				// One hop: an identifier standing for a number declared in this file.
				resolved, ok := constants[arg]
				if !ok {
					continue
				}
				value, named = resolved, true
			}
			if s24IsZero(value) {
				continue
			}
			spelling := "raw dimension `" + value + "`"
			if named {
				spelling = "raw dimension `" + arg + " = " + value + "`"
			}
			faults = append(faults, name+": "+spelling+" in argument "+
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
	for _, re := range s24AssignedName {
		for _, m := range re.FindAllStringSubmatch(code, -1) {
			value, ok := constants[m[1]]
			if !ok || s24IsZero(value) {
				continue
			}
			faults = append(faults, name+": raw dimension `"+strings.TrimSpace(m[0])+
				"`, where "+m[1]+" = "+value+" -- a length may only come from R.dimen or the "+
				"design scale")
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
		// The three this slice actually deleted, each one hop behind a name. A fence that read
		// only call-site literals would have reported all three clean.
		{
			"a padding behind a constant",
			"private const val PADDING = 24\nfun f() { setPadding(PADDING, PADDING, PADDING, PADDING) }",
		},
		{
			"a layout height behind a constant",
			"private const val SCANNER_HEIGHT = 720\nval p = LinearLayout.LayoutParams(MATCH, SCANNER_HEIGHT)",
		},
		{
			"a text size behind a constant",
			"private const val SAS_TEXT_SP = 28f\nval v = label().apply { textSize = SAS_TEXT_SP }",
		},
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
		// A name that stands for something other than a number must pass: the constant table only
		// resolves file-local `val NAME = <number>`, so a layout constant is not a dimension.
		{
			"a layout constant behind a name",
			"const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT\nval p = LinearLayout.LayoutParams(MATCH, MATCH)",
		},
		{"a duration behind a constant", "const val POLL_MILLIS = 400L\npoller.postDelayed({}, POLL_MILLIS)"},
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

// s24Spends reports whether [symbol] is CALLED or CONSTRUCTED somewhere in [code], as opposed to
// merely imported. The import line itself is excluded, which is the whole point.
func s24Spends(code, symbol string) bool {
	call := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\s*\(`)
	for _, line := range strings.Split(code, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "import ") {
			continue
		}
		if call.MatchString(line) {
			return true
		}
	}
	return false
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
			// AN IMPORT IS NOT A CALL SITE, and the difference is this requirement's whole
			// subject. `import dev.swarm.phone.ui.kit.sessionRow` with no `sessionRow(` under it
			// is a file that MENTIONS the kit, which is exactly as much use to a user as the zero
			// call sites PB-DS-6 was marked NOT MET over. The symbol has to be spent.
			if !s24Spends(code, m[1]) {
				continue
			}
			reached[m[1]] = append(reached[m[1]], name)
		}
	}
	if len(reached) == 0 {
		t.Fatalf("PB-DS-6: no production Kotlin outside ui/kit/ calls a single kit factory, so " +
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

// TestPBDS6_AnImportIsNotACallSite is the control on the distinction the requirement turns on.
//
// PB-DS-6 was recorded NOT MET over a kit with ZERO CALL SITES, and the cheapest way to make that
// finding go away without changing anything a user sees is to import the symbols. So the check
// must be able to tell the two apart, and this is where that is demonstrated rather than assumed.
func TestPBDS6_AnImportIsNotACallSite(t *testing.T) {
	const mentioned = `import dev.swarm.phone.ui.kit.sessionRow

class Screen {
    fun draw() {
        // sessionRow(context, ...) would go here
    }
}`
	if s24Spends(kotlinCodeOnly(mentioned), "sessionRow") {
		t.Error("a file that imports sessionRow and never calls it reads as a call site, so the " +
			"kit's zero-call-site defect could be closed by adding import lines")
	}

	const spent = `import dev.swarm.phone.ui.kit.sessionRow

class Screen {
    fun draw() {
        root.addView(sessionRow(context, project, agent, need, group))
    }
}`
	if !s24Spends(kotlinCodeOnly(spent), "sessionRow") {
		t.Error("a real call is not recognised, so the check would report a screen built entirely " +
			"out of the kit as reaching none of it")
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
	// The three ways to reach a length without naming a resource. Without them a screen could
	// convert its own dp to px and every check above would stay green.
	{regexp.MustCompile(`\bresources\.getDimension`), "a dimension"},
	{regexp.MustCompile(`\bTypedValue\b`), "a unit conversion"},
	{regexp.MustCompile(`\bdisplayMetrics\b`), "the screen density"},
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
		{"a density conversion", `val px = 12f * context.resources.displayMetrics.density`},
		{"a unit conversion", `TypedValue.applyDimension(COMPLEX_UNIT_DIP, 12f, metrics)`},
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

// s24EmptyStateFile is the component PB-DS-9's most-argued clause depends on.
const s24EmptyStateFile = "dev/swarm/phone/ui/kit/EmptyState.kt"

// s24ComponentsDoc is the derivation table, which is the ONLY authority for a component Substrate
// never drew.
const s24ComponentsDoc = "docs/design/substrate-components.md"

// s24Row8Padding reads row 8's spacing cell: "padding 48 (2 x `space_24`) vertical, `space_24`
// horizontal".
//
// IT NEEDS ITS OWN READER, and that is the reason this check is here rather than in the S23 gate.
// `s23RowPadding` matches “padding `space_N` x `space_N` “ -- the shape every other derived
// component's cell has -- and row 8 does not have it, because its vertical padding is a MULTIPLE
// of a step rather than a step. A row the reader cannot parse is a row nothing checks, and this is
// the component whose absence left the triage inbox drawing four headings over nothing.
var s24Row8Padding = regexp.MustCompile(
	"padding ([0-9]+) \\(([0-9]+) x `(space_[0-9]+)`\\) vertical, `(space_[0-9]+)` horizontal")

// TestPBDS9_TheEmptyStateSpendsTheStepsRow8States joins the derivation table to the component.
//
// WHAT IT IS FOR. `emptyState` is cited as `derived: <doc> #8 Empty state`, and a citation is a
// gesture until something reads the row. This reads it: the arithmetic the cell states has to be
// true (48 = 2 x 24), the component has to reference the step the cell names, and it must
// reference NO OTHER step -- row 8 states exactly one, so a second would be a value chosen
// somewhere other than the table.
func TestPBDS9_TheEmptyStateSpendsTheStepsRow8States(t *testing.T) {
	docPath := filepath.Join(repoRoot(t), filepath.FromSlash(s24ComponentsDoc))
	doc := readFileOrFail(t, docPath, "PB-DS-9")
	m := s24Row8Padding.FindStringSubmatch(doc)
	if m == nil {
		t.Fatalf("PB-DS-9: %s states no row-8 padding of the form this reader knows, so the empty "+
			"state's spacing is joined to nothing", s24ComponentsDoc)
	}
	total, steps, vertical, horizontal := m[1], m[2], m[3], m[4]

	// The cell's own arithmetic, checked rather than trusted: 48 = 2 x 24.
	stepValue, err := strconv.Atoi(strings.TrimPrefix(vertical, "space_"))
	if err != nil {
		t.Fatalf("PB-DS-9: row 8's vertical step %q carries no number", vertical)
	}
	count, err := strconv.Atoi(steps)
	if err != nil {
		t.Fatalf("PB-DS-9: row 8's multiplier %q is not a number", steps)
	}
	want, err := strconv.Atoi(total)
	if err != nil {
		t.Fatalf("PB-DS-9: row 8's total %q is not a number", total)
	}
	if count*stepValue != want {
		t.Errorf("PB-DS-9: row 8 states %s = %s x `%s`, and %d x %d is %d. The table disagrees "+
			"with itself, so neither number can be spent from it.",
			total, steps, vertical, count, stepValue, count*stepValue)
	}
	if vertical != horizontal {
		t.Errorf("PB-DS-9: row 8 states `%s` vertically and `%s` horizontally; this check assumes "+
			"one step and needs rewriting rather than relaxing", vertical, horizontal)
	}

	path := filepath.Join(s24KotlinRoot(t), filepath.FromSlash(s24EmptyStateFile))
	if !exists(path) {
		t.Fatalf("PB-DS-9: there is no %s. An empty section then renders as a heading over "+
			"nothing, which is the failure the requirement names by name.", s24EmptyStateFile)
	}
	code := kotlinCodeOnly(readFileOrFail(t, path, "PB-DS-9"))

	if !strings.Contains(code, "R.dimen.swarm_"+vertical) {
		t.Errorf("PB-DS-9: %s never references R.dimen.swarm_%s, which is the only step row 8 "+
			"names. A dimension that is not read from the scale is one typed at the call site.",
			s24EmptyStateFile, vertical)
	}
	for _, other := range s24SpaceStepRef.FindAllStringSubmatch(code, -1) {
		if other[1] == vertical {
			continue
		}
		t.Errorf("PB-DS-9: %s spends R.dimen.swarm_%s, and row 8 states only `%s`. A second step "+
			"is a value chosen somewhere other than the table that is this component's whole "+
			"authority.", s24EmptyStateFile, other[1], vertical)
	}
}

// s24SpaceStepRef is any spacing step a source reads.
var s24SpaceStepRef = regexp.MustCompile(`R\.dimen\.swarm_(space_[0-9]+)`)

// TestPBDS9_TheRow8ReaderSeesADisagreeingTable is that join's negative control.
//
// The reader has one job -- turn a sentence in a document into two numbers -- and the way it fails
// silently is by matching nothing and reporting a clean run over a component nobody checked. Both
// directions are exercised: a well-formed cell must parse, and a cell whose arithmetic is wrong
// must be caught rather than transcribed.
func TestPBDS9_TheRow8ReaderSeesADisagreeingTable(t *testing.T) {
	good := "padding 48 (2 x `space_24`) vertical, `space_24` horizontal; compact variant"
	m := s24Row8Padding.FindStringSubmatch(good)
	if m == nil {
		t.Fatalf("the row-8 reader cannot parse the cell as the document writes it: %q", good)
	}
	if m[1] != "48" || m[2] != "2" || m[3] != "space_24" || m[4] != "space_24" {
		t.Errorf("the row-8 reader mis-parsed a well-formed cell: %v", m[1:])
	}

	// The perturbation: a table whose own arithmetic is wrong. 3 x 24 is 72, not 48 -- and a
	// check that only read the total and the step name would transcribe it happily.
	bad := "padding 48 (3 x `space_24`) vertical, `space_24` horizontal"
	m = s24Row8Padding.FindStringSubmatch(bad)
	if m == nil {
		t.Fatal("the row-8 reader cannot parse the perturbed cell, so the control proves nothing")
	}
	count, _ := strconv.Atoi(m[2])
	step, _ := strconv.Atoi(strings.TrimPrefix(m[3], "space_"))
	total, _ := strconv.Atoi(m[1])
	if count*step == total {
		t.Errorf("3 x space_24 is being read as %d, which equals the stated %d -- the arithmetic "+
			"check cannot fail", count*step, total)
	}
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

// ---------------------------------------------------------------------------
// PB-DS-6 / PB-DS-9 over the screens S24 recomposed AFTER the triage inbox.
//
// WHY THE TWO CHECKS ABOVE ARE NOT ENOUGH. `TestPBDS6_TheKitHasProductionCallSites` asks whether
// the kit is reached AT ALL and names the inbox's seven components; a second screen added beside
// it that composed nothing would leave that test green, because the inbox still reaches all seven.
// `TestPBDS6_TheScreenPackageIsFencedToComponentCallsPlusLayout` asks whether a screen names a
// colour, which a screen that hand-builds unstyled views does not. So a hand-built screen dropped
// into this package passes both -- and "the kit is the only way a screen is built" is exactly the
// claim that would then be false.
// ---------------------------------------------------------------------------

// s24BuildsViews recognises a screen source that puts things ON SCREEN, as opposed to one that is
// a pure model.
//
// THE DISTINCTION IS LOAD-BEARING AND IT IS WHY THIS IS NOT "every file in the package".
// `TriageInboxScreen.kt`, `SettingsPanel.kt` and `PairingPanel.kt` are data and copy with no
// Android import at all -- that is what makes them checkable on a plain JVM -- and requiring them
// to call a component factory would require them to stop being models.
var s24BuildsViews = regexp.MustCompile(`import\s+android\.(?:view|widget)\.`)

// s24ScreenComponents is the claim, per recomposed screen: which kit factories its recorded
// composition is made of.
//
// IT IS A CLAIM AND NOT A SURVEY, the same way s24InboxComponents is. Each entry names a factory
// the kit already ships and the element of the screen inventory it renders, so a screen that
// quietly stops rendering one fails here rather than looking like a tidier screen.
//
// THE LISTS ARE SHORT BECAUSE THE KIT IS SHORT, and what is missing is recorded rather than
// hidden. Derivation rows 15 (settings row), 9 (text field) and 18 (the mono well) landed while
// this slice was running and are spent -- the settings row here, the field and the well from the
// surfaces, which is where the views they replace are built. Row 4 (toggle) and row 10 (the three
// CTA variants) still have no factory, so the toggle and the seven pairing controls arrive at
// these screens as parameters. Both belong in this table the day they exist.
var s24ScreenComponents = map[string]map[string]string{
	"dev/swarm/phone/ui/screens/TriageInboxView.kt": {
		"navHeader":    "C1.1 `.pnav`",
		"chipRow":      "C1.2 `.chips`",
		"filterChip":   "C1.2 `.chips` -- one scope",
		"sectionLabel": "C1.3 `.plabel`",
		"sessionList":  "C1.3 `.prows`",
		"sessionRow":   "C1.3 `.prow`",
	},
	// C1.4 IS THE SCAFFOLD'S AND NOT THE INBOX'S, and the row moved here rather than being
	// dropped. `tabBar` was claimed above while `triageInboxView` was the only screen that
	// composed one -- which is precisely why the other three destinations could not be reached: a
	// bar drawn inside one of four screens is a bar the other three do not have, so a tab that
	// swapped the content would land the user on Machines with no way back. Derivation row 20 puts
	// the bar under the scaffold's scrolling content ("bottom `screen_bottom` (or inset +
	// `tabbar_height`)" -- the inset UNDER a bar that is `tabbar_height` tall), which is the
	// composition this file now names. The claim is unchanged in strength: `tabBar` is still
	// required of a named screen source, and it is still the only place in the app that composes
	// one.
	"dev/swarm/phone/ui/screens/PhoneScaffoldView.kt": {
		"tabBar": "C1.4 `.ptabs` -- the four destinations, under whichever one is on screen",
	},
	// THE CONNECTION SECTION IS THIS SCREEN'S NOW (agents-tracker-nx44.3), which is why
	// `machineRow` is claimed here. Inventory C4 -- the machines screen -- is deleted: its four
	// per-channel gap cards and its "this phone cannot read your machine's details" sentence were
	// the whole of a primary destination, and field test 3 recorded an owner reading it and asking
	// what the page was for. Derivation row 11's machine row is the part that answered a question
	// somebody asked, so it moves under the PAIRING section rather than being deleted with the
	// screen -- and this file is its ONLY production call site now that `MachinesPanelView.kt` is
	// gone. `notice` is claimed twice over: the disclosure and the per-toggle notices, and the
	// section's two fault lines (the channel-health summary and PB-TIME-1's clock verdict), which
	// draw only when there is a fault.
	"dev/swarm/phone/ui/screens/SettingsPanelView.kt": {
		"navHeader":    "C6.1 -- the settings screen's own title",
		"sectionLabel": "C6.2 `.seclabel` -- one per section",
		"settingsRow":  "C6.2 `.setrow` -- one per preference, derivation row 15",
		"machineRow":   "derivation row 11 -- the CONNECTION section's machine, with its presence mark",
		"notice":       "§4 Notice line -- the disclosure, the per-toggle notices, the link's faults",
		// agents-tracker-2pnu F5: derivation row 12, which lost its only caller when nx44.3
		// deleted MachinesPanelView -- so a machine with remote access OFF refused every command
		// this phone sent and said nothing about why, anywhere. It is drawn ONLY for the OFF
		// state; an --p-err box reporting that nothing is wrong is the loudest way to say it.
		"killSwitchPanel": "derivation row 12 -- the machine's own switch, where the CONNECTION section can be asked",
	},
	"dev/swarm/phone/ui/screens/PairingPanelView.kt": {
		// Derivation row 18: the pairing step has no nav header, so its title IS the screen
		// title, in `Display.NavTitle` -- which is the style `navHeader` renders.
		"navHeader": "C7 -- the step title, per derivation row 18",
	},
	// C2 -- the session detail, and IT WAS UNFENCED UNTIL THIS ROW EXISTED. The file had no entry,
	// so `s24ScreenComponents[name]` returned an empty requirement and the loop above found nothing
	// to check: the screen passed because nothing was asked of it, which reads identical to
	// passing. It is the last screen the inventory names and the one with the most kit in it.
	//
	// **THIS ROW IS AMENDED BY ADR-009-structured-chat-interaction, AND THE AMENDMENT IS THE POINT
	// OF THE SLICE.** It used to require `monoWell` -- "C2.2 `.term` -- the daemon-rendered grid,
	// reused from C3" -- beside the four components of the session's own journal log. (1) leaves
	// "no terminal emulation and no raw grid anywhere in the app" and (3) deletes the well at slice
	// I1's exit, so the requirement it carried is GONE rather than unmet, and a row that went on
	// demanding it would fail a screen for obeying a decision. The four journal components are not
	// deleted either: they moved WHOLE to `TranscriptView.kt` below, which is the section this
	// screen now places, so §2's reuse rule is satisfied one file over rather than dropped.
	//
	// WHAT IS LEFT HERE IS ONE FACTORY, and that is honest rather than thin. Everything else on
	// this screen is either a slot the surface owns (Take control, Stop, Kill -- they reach facade
	// verbs, carry PB-SEC-12 clause 1's touch filter and must survive a redraw, which is
	// `PairingPanelView`'s arrangement), a bare notice `TextView` (there is no body-copy component
	// in the kit and this file says so in as many words), or the transcript's own composition. The
	// composer is absent for a third reason: derivation row 9's bar has no kit factory at all, and
	// it ships with PB-INPUT-1's undelivered-input ledger or not at all (agents-tracker-hxv).
	"dev/swarm/phone/ui/screens/SessionDetailView.kt": {
		"navHeaderDrill": "C2.1 `.navhead` -- the chevron and the session it names, per §4",
		"notice":         "§4 Notice line -- the stale, not-sent, lease and outcome lines",
	},
	// C2.3 as ADR-009 (1) redraws it: "the phone's only session surface is a structured chat
	// transcript". The four components below are the session log's, reused verbatim -- the heading,
	// the container, the row and row 8's empty state -- which is what makes this a MOVE rather than
	// a second inventory of the same shapes. `monoWell` is here for a tool's output and a file's
	// diff, at `terminal = FALSE`: it is §2's one factory for every mono block, and the variant
	// that printed the VT grid in `terminal_peek.fg` has no caller left in this app.
	"dev/swarm/phone/ui/screens/TranscriptView.kt": {
		"sectionLabel": "the heading over the conversation -- an empty section is still a section (PB-DS-9)",
		"sessionList":  "`.prows` -- the blocks' container, carrying the gap and the side padding",
		"activityRow":  "one interaction item, derivation row 14 reused a fourth time",
		"monoWell":     "a tool's output and a file's diff -- §2's one factory for every mono block",
		"emptyState":   "derivation row 8 -- a heading over no conversation is a section that lies",
	},
	// The approval card (ADR-009 (4)), and the row that keeps `monoWell`'s reuse rule fenced now
	// that the two terminal screens are gone. It is the LITERAL a decision is about -- §7's action
	// line, or IS-APR-3's sanitized prompt region on the prompt-card fallback -- and it is a card
	// either way, never a grid. `ctaButton` is claimed because the buttons ARE the screen: a sheet
	// that drew a question with no way to answer it is the state O6 shipped and recorded PARTIAL.
	"dev/swarm/phone/ui/screens/ApprovalSheetView.kt": {
		"approvalSheet": "D4.4's sheet -- the heaviest material in the app, for the moment of decision",
		"monoWell":      "the literal the decision is about, §5's one mono block spent again",
		"ctaButton":     "one per `decisions[].label`, every one `.a2-more` -- IS-APR-4 keeps polarity machine-side",
	},
	// The launch form HAS NO SCREEN INVENTORY ENTRY. The eight screens the artifacts draw are the
	// inbox, the session detail, the terminal peek, machines, activity, settings, pairing and the
	// approval sheet; starting a session is PB-APP-6's requirement and the mock never drew it. So
	// the role below cites the design rule the component answers to rather than an inventory
	// section, which is the honest citation -- and `LaunchPanel`'s own KDoc says the same thing
	// about its copy.
	//
	// THE LIST IS ONE ROW AND THE REST OF THE FORM IS SLOTS, which is `PairingPanelView`'s
	// arrangement and for the same reason. The three fields hold what the user typed and are read
	// back on submit, so they must survive a redraw; the submit control carries PB-SEC-12 clause
	// 1's touch filter and the facade call. Both are `PhoneSurface`'s, and both are built out of
	// the kit THERE -- `textField` and `ctaButton` -- which is what
	// TestPBDS6_TheKitHasProductionCallSites reads. A screen that constructed them would be a
	// screen owning a listener and a native call, which is not what a screen is.
	"dev/swarm/phone/ui/screens/LaunchPanelView.kt": {
		"sectionLabel": "`.plabel` -- the section the form sits under",
		"notice":       "§4 Notice line -- what the form has to report about the last launch",
	},
	// THE ACTIVITY SCREEN HAS ONE SECTION AND THE MOCK DRAWS TWO. `renderActivity()` splits its
	// rows under `While you were away` and `Informative`, and NOTHING ON THE WIRE SUPPORTS THAT
	// SPLIT: swarmmobile.JournalEntry is (Cursor, SessionID, Type, Group) and carries no
	// seen-ness, no acknowledgement and no salience. Reproducing the two headings would be a
	// grouping invented to match a drawing, so the panel renders one section and says so in its
	// own KDoc. The claim below is therefore `sectionLabel` once, which is what a screen that
	// tells the truth about this journal composes.
	"dev/swarm/phone/ui/screens/ActivityPanelView.kt": {
		"navHeader":    "the activity tab's own `.pnav` title -- the mock's `.navhead`, retitled",
		"sectionLabel": "`.plabel` -- the one section the journal actually supports",
		"activityRow":  "`.arow` -- one per journal record, derivation row 14",
		// `.cards` is `.prows` with different numbers (0 14px/gap 8 against 0 12px/gap 7), and §6
		// is where that difference is already settled: every mock geometry in this app moves onto
		// Substrate's, which is why row 14 puts the activity row itself on `--p-card-r` 9. So the
		// container is a REUSE of sessionList and not a second factory -- §2's rule -- and it is
		// claimed here because a screen that dropped it would type the side padding and the gap
		// itself, which is the PB-DS-6 violation the kit exists to prevent.
		"sessionList": "`.prows` -- the rows' container, carrying the gap and the side padding",
		"emptyState":  "derivation row 8 -- a heading over nothing is a section that lies",
		"notice":      "§4 Notice line -- the stale mark over a journal that has stopped arriving",
	},
	// THE SYNC DETAIL SHEET, WHICH INHERITED THE LINK SECTION'S JOB (agents-tracker-nx44.2, and
	// agents-tracker-nx44.3 for the inheritance). This block used to claim `LinkPanelView.kt` --
	// four unconditional per-channel rows on the Machines destination, a status label on the live
	// ones and `notice` for the clock -- and that destination is deleted (the status label itself
	// is retired by agents-tracker-2pnu F5, for want of any caller at all). The argument the deleted
	// claim carried is preserved where it still holds: the UNCONDITIONAL readout, healthy rows
	// included, is what makes "all of them are fine" distinguishable from "this screen forgot the
	// reply channel", and this sheet is where it lives because a sheet is opened deliberately. The
	// summary that replaced the four cards is one `notice` in the settings CONNECTION section.
	//
	// NEITHER `navHeader` NOR `sectionLabel` IS CLAIMED: the sheet has no title of its own -- it
	// opens from the nav row's pill and the pill is its heading -- and its three labelled rows are
	// row 15's, which is the `settingsRow` below.
	"dev/swarm/phone/ui/screens/SyncStatusView.kt": {
		"sessionList": "`.prows` -- the three labelled facts' container, carrying the gap and the side padding",
		"settingsRow": "derivation row 15 -- one per fact: HEARD, READING, VIEWS",
		"notice":      "§4 Notice line -- one per repair channel with a hole in it",
		"ctaButton":   "the one control: the repair, or the way back to Pairing",
	},
	// WAVE R4'S MACHINE SWITCHER (bead agents-tracker-0ox9). It arrived with no row here, which
	// is how it came to be fenced by the weak clause alone -- `s24ScreenComponents[name]` returns
	// an empty requirement for an unclaimed file, so the loop asks only that SOMETHING in the kit
	// is called and a screen that dropped its header, its disclosure or its rows would still
	// pass. `TestR4D3R3_EveryComposedScreenIsClaimedByTheCompositionTable` is what makes that
	// omission fail rather than read as a pass.
	//
	// `notice` IS CLAIMED FOR TWO SENTENCES AND BOTH ARE HONESTY: ADR-018's cap sentence, drawn
	// only when the roster exceeds the documented connection cap, and round 3's add-form limits,
	// drawn ALWAYS because they always bind (the added computer still needs its own pairing
	// ceremony -- bead agents-tracker-ak2s -- and switching does not re-target the live session).
	// A screen that quietly stopped drawing either would be claiming completeness it does not
	// have, which is the exact defect this slice's round 3 was called over.
	"dev/swarm/phone/ui/screens/MachinesPanelView.kt": {
		"navHeaderDrill": "the drill header -- the switcher is a sub-state of Settings, per §4",
		"ctaButton":      "the aggregate inbox entry and the Add computer submit",
		"sessionList":    "`.prows` -- the switcher's rows, carrying the gap and the side padding",
		"settingsRow":    "derivation row 15 -- one per pairing, the row IS the switch control",
		"denyChip":       "the per-row phone-side Forget (playbook 4.9), destructive polarity",
		"notice":         "§4 Notice line -- ADR-018's cap sentence, the add form's limits, a broken row's own fault",
	},
	// THE AGGREGATE INBOX (inbox.global), the switcher's one destination. `emptyState` is row 8's
	// and it is load-bearing on THIS screen above all: an aggregate list that draws nothing when
	// no pairing holds a session is indistinguishable from an aggregate list that failed to read
	// any pairing at all.
	"dev/swarm/phone/ui/screens/GlobalInboxView.kt": {
		"navHeaderDrill": "the drill header -- one level below the switcher, per §4",
		"sessionList":    "`.prows` -- the (machine_id, session_id) rows' container",
		"settingsRow":    "derivation row 15 -- one per row: the session, and the machine that serves it",
		"emptyState":     "derivation row 8 -- a screen that draws nothing cannot say why it is empty",
	},
	// THE PAIR-ONLY SCREEN, unclaimed since it landed and found by the same exhaustiveness fence.
	// It is the whole app on a phone with no pairing, so what it composes is not a detail: the
	// title, the one offer to pair, and the reason there is nothing else.
	"dev/swarm/phone/ui/screens/PairOnlyView.kt": {
		"navHeader":  "the screen's own title -- there is nothing to drill back to",
		"ctaButton":  "the one offer: pair this phone with a computer",
		"emptyState": "derivation row 8 -- the reason this screen is all there is",
		"notice":     "§4 Notice line -- why a pairing is missing, when the reason is known",
	},
}

// s24ScreenKitFaults reports what one screen source owes the requirement and does not spend.
//
// @return one line per fault, sorted, empty when the source is composed as claimed.
func s24ScreenKitFaults(name, src string, required map[string]string) []string {
	code := kotlinCodeOnly(src)
	if !s24BuildsViews.MatchString(code) {
		// A pure model. It composes nothing because it renders nothing.
		return nil
	}

	var faults []string
	spent := 0
	for _, m := range s24KitImport.FindAllStringSubmatch(code, -1) {
		if s24Spends(code, m[1]) {
			spent++
		}
	}
	if spent == 0 {
		faults = append(faults, name+": builds views and calls no kit factory at all, so this "+
			"screen is hand-built -- which is the state PB-DS-6 was recorded NOT MET in, with a "+
			"second screen beside the one that fixed it")
	}
	for factory, role := range required {
		if !s24Spends(code, factory) {
			faults = append(faults, name+": does not reach `"+factory+"` -- "+role)
		}
	}
	sort.Strings(faults)
	return faults
}

// TestPBDS6_EveryRecomposedScreenIsBuiltOutOfTheKit is the fence.
func TestPBDS6_EveryRecomposedScreenIsBuiltOutOfTheKit(t *testing.T) {
	screens := s24ScreenSources(t)
	if len(screens) == 0 {
		t.Fatalf("PB-DS-6: there is no screen under %s", s24ScreenPackage)
	}

	var faults []string
	views := 0
	for name, src := range screens {
		if s24BuildsViews.MatchString(kotlinCodeOnly(src)) {
			views++
		}
		faults = append(faults, s24ScreenKitFaults(name, src, s24ScreenComponents[name])...)
	}
	// A package of models and no views would report every screen composed, vacuously.
	if views == 0 {
		t.Fatalf("PB-DS-6: no file under %s builds a view, so nothing a user sees is composed "+
			"from the kit and every claim below is about nothing", s24ScreenPackage)
	}
	for claimed := range s24ScreenComponents {
		if _, ok := screens[claimed]; !ok {
			faults = append(faults, claimed+": the composition table claims this screen and the "+
				"file does not exist, so its components are checked against nothing")
		}
	}
	sort.Strings(faults)
	if len(faults) > 0 {
		t.Errorf("PB-DS-6: %d screens are not built out of the kit:\n%s", len(faults),
			strings.Join(faults, "\n"))
	}
}

// TestPBAPP3_TheSessionDetailIsReachedFromTheApp is the composition fence's missing half, for the
// one screen that had no way in.
//
// WHY A SOURCE SCAN IS NEEDED AT ALL. Every other assertion in this file asks how a screen is
// BUILT; none asks whether anything renders it, because the runtime half of that question is
// `PhoneSurfaceNavigationTest` -- it launches the real Activity, taps a real tab and reads what
// lands under the bar. That test reaches the machines and activity screens and CANNOT reach this
// one: the session detail is a drill-down opened by tapping a session ROW, and a row exists only
// on the branch where the phone core built. The core is a gomobile AAR of .so files cross-compiled
// for Android ABIs, so `PhoneRuntime.phone()` answers Unavailable on every JVM run and the whole
// drill-down is out of reach there (the argument in full is
// android/gate/pbapp6_pbinput2_surface_test.go's). A source scan is what is left, and one fence is
// better than the none this screen had.
//
// WHAT IT ASSERTS IS THE DEFECT PB-DS-6 WAS RECORDED NOT MET OVER, one level up: a component
// library nothing renders is not a design system, and a SCREEN nothing navigates to is worth
// exactly as much. `sessionDetailView` landed composed from the kit, covered by eight tests, and
// reachable by nothing -- which is also the state `navHeaderDrill`'s chevron shipped in
// (agents-tracker-2yb: "the chevron therefore looks like a control and does not act").
//
// What the comparison can and cannot see is on [assertScreenIsReachedFromTheApp], which both
// fences of this shape call.
func TestPBAPP3_TheSessionDetailIsReachedFromTheApp(t *testing.T) {
	assertScreenIsReachedFromTheApp(t, "PB-APP-3", "sessionDetailView",
		"inventory C2 is a screen the app cannot navigate to, and a user tapping a session row "+
			"does not arrive at it")
}

// TestPBDS9_TheSyncDetailIsReachedFromTheApp is the same fence over the screen that carries
// PB-APP-8's per-channel verdicts today, which is the one place it would be embarrassing to leave
// unfenced.
//
// **IT NAMED `linkPanelView` UNTIL agents-tracker-nx44.3, AND THE SUBJECT MOVED RATHER THAN THE
// FENCE BEING DROPPED.** That section existed because `ClockBanner` and `StreamView` were
// modelled, unit-tested, reached by `FacadeBridge` and drawn by nothing (agents-tracker-ah2); it
// lived on the Machines destination, and the tab fold deletes the destination and the section with
// it. The four verdicts did not go with them: `syncStatusView` draws every repair channel with a
// hole in it, from the same `FacadeBridge.streamViews()`, on a sheet reachable from every
// destination -- and the clock verdict is the settings CONNECTION section's, which
// `settingsPanelView` composes and `SettingsSurface` renders. So this is the same assertion about
// the file that inherited the job.
//
// A section written to fix "reachable by nothing" and then left reachable by nothing is the same
// bug with more files in it, and every check that would notice is on the wrong side of a seam:
// `SyncStatusViewTest` builds the view itself, and the runtime half is out of reach because
// `PhoneRuntime.phone()` answers Unavailable on every JVM run. A source scan is what is left.
//
// STILL SCOPED TO NAMED SCREENS rather than blanket. A blanket fence would need an exemption
// table, and a table of screens allowed to be unreachable is a place for screens to go and stay.
func TestPBDS9_TheSyncDetailIsReachedFromTheApp(t *testing.T) {
	assertScreenIsReachedFromTheApp(t, "PB-DS-9", "syncStatusView",
		"PB-APP-8's per-channel verdicts are drawn by nothing again, which is the exact defect "+
			"the section this one replaced was written to close")
}

// assertScreenIsReachedFromTheApp is the comparison both fences make.
//
// WHAT IT CANNOT SEE, stated rather than left to be assumed away: a call site is not
// REACHABILITY. A `screen(...)` inside a function nothing invokes satisfies it, the same limit
// android/gate/boundverbledger_test.go records about its own name matching.
//
// The comparison itself is `s24Spends`, whose negative control is TestPBDS6_AnImportIsNotACallSite
// -- an import of the screen factory with no call under it must not read as a call site, which is
// the cheapest possible way to make either finding go away without changing what a user can reach.
func assertScreenIsReachedFromTheApp(t *testing.T, requirement, factory, cost string) {
	t.Helper()

	var reachedBy []string
	for name, src := range s24ProductionKotlin(t) {
		// The screen package is EXCLUDED, and that is the whole assertion. A screen composing
		// itself, or a sibling screen composing it, says nothing about whether the app the user
		// opens can get there.
		if strings.HasPrefix(name, s24ScreenPackage+"/") {
			continue
		}
		if s24Spends(kotlinCodeOnly(src), factory) {
			reachedBy = append(reachedBy, name)
		}
	}
	if len(reachedBy) == 0 {
		t.Errorf("%s: no production Kotlin outside %s calls `%s`, so %s. It is composed from the "+
			"kit and covered by its own suite -- which is the defect PB-DS-6 was recorded NOT MET "+
			"over ('the kit has ZERO production call sites'), one level up.",
			requirement, s24ScreenPackage, factory, cost)
	}
}

// TestPBDS6_TheRecomposedScreenCheckSeesEachClassOfViolation is that fence's negative control.
//
// It feeds perturbed sources to the SAME function the assertion calls, which is this package's
// standing rule: a control that rebuilds the comparison inline proves something about the copy.
func TestPBDS6_TheRecomposedScreenCheckSeesEachClassOfViolation(t *testing.T) {
	required := map[string]string{"navHeader": "the screen title"}

	handBuilt := `import android.widget.LinearLayout

fun screenView(context: Context): View = LinearLayout(context).apply {
    addView(TextView(context).apply { text = "Settings" })
}`
	if got := s24ScreenKitFaults("perturbed.kt", handBuilt, required); len(got) == 0 {
		t.Error("the check is blind to a screen that builds views and reaches no kit factory, " +
			"which is a second hand-built screen beside the one that closed PB-DS-6")
	}

	// The subtler one: it composes SOMETHING, so the "calls nothing" clause is satisfied, and it
	// has quietly stopped drawing the element its recorded composition names.
	partial := `import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.sectionLabel

fun screenView(context: Context): View = LinearLayout(context).apply {
    addView(sectionLabel(context, "NOTIFICATIONS"))
}`
	got := s24ScreenKitFaults("perturbed.kt", partial, required)
	if len(got) != 1 || !strings.Contains(got[0], "navHeader") {
		t.Errorf("the check does not notice a screen that drops one component of its recorded "+
			"composition while still reaching the kit; it reported %v", got)
	}

	// An import is not a call site here either.
	mentioned := `import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.navHeader

fun screenView(context: Context): View = LinearLayout(context)`
	if got := s24ScreenKitFaults("perturbed.kt", mentioned, required); len(got) == 0 {
		t.Error("a screen that imports navHeader and never calls it reads as composed")
	}

	// And the two shapes that MUST pass, or the package cannot hold a model or a screen.
	model := `package dev.swarm.phone.ui.screens

data class SettingsPanel(val title: String)`
	if got := s24ScreenKitFaults("model.kt", model, nil); len(got) > 0 {
		t.Errorf("the check rejects a pure screen model, which builds no views by design:\n%s",
			strings.Join(got, "\n"))
	}
	composed := `import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.navHeader

fun screenView(context: Context): View = LinearLayout(context).apply {
    addView(navHeader(context, panel.title, null))
}`
	if got := s24ScreenKitFaults("allowed.kt", composed, required); len(got) > 0 {
		t.Errorf("the check rejects a screen composed exactly as claimed:\n%s",
			strings.Join(got, "\n"))
	}
}

// s24VisibilityWrite is a screen deciding what is on screen by HIDING it.
var s24VisibilityWrite = regexp.MustCompile(`\b(?:setVisibility\s*\(|visibility\s*=)`)

// TestPBDS9_AScreenComposesWhatIsOnItRatherThanHidingWhatIsNot fences the pattern this slice
// deleted.
//
// WHAT WAS THERE BEFORE. `PairingSurface` decided its fifteen steps with three functions that
// each called `show(view, condition)` -- eight controls, three re-derivations of the same three
// modes, and two of those controls are the SAS answers, which after ADR-007 B133 are the only
// human-in-the-loop security check left in the product. A transposed condition in that shape is
// invisible in review and invisible on screen until the step it governs is reached.
//
// A COMPOSED SCREEN CANNOT HAVE THAT BUG, because a view that is not on screen is a view the
// composition did not add -- there is no second, contradictable statement of the same fact. This
// keeps the screen package that way. It is NOT a rule about the surfaces: a surface owns controls
// whose visibility is genuinely a fact about the device rather than about the step (the camera
// preview is the standing example), and it is fenced by PB-DS-11 rather than by this.
func TestPBDS9_AScreenComposesWhatIsOnItRatherThanHidingWhatIsNot(t *testing.T) {
	screens := s24ScreenSources(t)
	if len(screens) == 0 {
		t.Fatalf("PB-DS-9: there is no screen under %s", s24ScreenPackage)
	}
	var faults []string
	for name, src := range screens {
		for _, m := range s24VisibilityWrite.FindAllString(kotlinCodeOnly(src), -1) {
			faults = append(faults, name+": writes `"+strings.TrimSpace(m)+"`. A screen states "+
				"what is on it by composing it; a second statement that hides what it just added "+
				"is the shape three `render*Step` functions had, where a transposed condition was "+
				"invisible until the step it governed was reached.")
		}
	}
	sort.Strings(faults)
	if len(faults) > 0 {
		t.Errorf("PB-DS-9: %d visibility writes in the screen package:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}
}

// TestPBDS9_TheVisibilityCheckSeesBothSpellings is that fence's negative control, in both
// directions -- a fence that fails on everything is as useless as one that fails on nothing.
func TestPBDS9_TheVisibilityCheckSeesBothSpellings(t *testing.T) {
	for _, dirty := range []string{
		`view.visibility = View.GONE`,
		`view.setVisibility(View.VISIBLE)`,
		`show(startScan, scanning && state == ScannerState.SCANNING)
		 confirmDestination.visibility = if (confirming) View.VISIBLE else View.GONE`,
	} {
		if !s24VisibilityWrite.MatchString(kotlinCodeOnly(dirty)) {
			t.Errorf("the visibility check is blind to `%s`", dirty)
		}
	}
	for _, clean := range []string{
		`if (section.rows.isEmpty()) content.addView(emptyState(context, section.emptyCopy))`,
		`// It used to be ` + "`view.visibility = View.GONE`" + `, set from three places.`,
		`val visible = panel.controls`,
	} {
		if s24VisibilityWrite.MatchString(kotlinCodeOnly(clean)) {
			t.Errorf("the visibility check rejects composition itself: `%s`", clean)
		}
	}
}
