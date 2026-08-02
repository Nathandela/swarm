package gate

// FAILING-FIRST (TDD RED, GG-5) for the two things the tab bar does not do: it draws no glyphs,
// and it spends the design's home-indicator inset ON TOP of the real one.
//
// THE STATE OF THE WORLD, verified before these assertions were written:
//
//	TabItem.icon                      nullable, and every call site passes null
//	res/drawable/                     ic_launcher_foreground.xml, ic_swarm_wake.xml -- no tab glyph
//	TabBar.kt                         setPaddingRelative(0, 0, 0, R.dimen.swarm_space_14)
//	PhoneActivity.insetTheSystemBars  view.setPadding(bars.left, bars.top, bars.right, bars.bottom)
//
// so the bar renders four bare labels, and on a handset the 14 dp it reserves for the home
// indicator lands on top of the navigation inset the scaffold has already applied.
//
// BOTH HALVES JOIN TO THE ARTIFACT. The four glyphs are in the Substrate artifact's `.ptabs`
// block, as `<svg viewBox>` + `<path d>` next to the label they belong to, and `.ptabs svg`
// declares the box they are drawn in. Nothing below transcribes a path or a size: the expected
// value for every assertion is read out of docs/research/remote-control-design-directions.html at
// test time, which is the arrangement s22b_designsource_test.go's header argues for and the reason
// PB-TOK-1's palette drifted before it was adopted.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The artifact's four tabs.
// ---------------------------------------------------------------------------

// tabbarGlyph is one tab as the artifact draws it: a label, and the glyph beside it.
type tabbarGlyph struct {
	Label    string
	ViewBox  [4]float64
	PathData string
}

// tabbarBlockStart is the artifact's own tab bar. `.ptabs` is a Substrate rule, so the block is
// inside the shared phone structure the whole slice reads.
const tabbarBlockStart = `<div class="ptabs">`

var tabbarItemRe = regexp.MustCompile(
	`<div[^>]*>\s*<svg viewBox="([^"]+)"\s*>\s*<path d="([^"]+)"\s*/>\s*</svg>\s*([A-Za-z][A-Za-z ]*)</div>`)

// tabbarGlyphs reads the four tabs out of the artifact, in the order it draws them.
func tabbarGlyphs(t *testing.T) []tabbarGlyph {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(s22bDesignSourceRelPath))
	raw := readFileOrFail(t, path, "PB-DS-6")

	start := strings.Index(raw, tabbarBlockStart)
	if start < 0 {
		t.Fatalf("PB-DS-6: %s no longer contains %s. Every glyph asserted below is read out of "+
			"that block; without it this file would compare four drawables against nothing and be "+
			"green.", mustRel(t, path), tabbarBlockStart)
	}
	block := tabbarNestedBlock(raw[start:])

	// The tabs are counted before they are read. A pattern that stopped matching -- an artifact
	// that put a class on the `<svg>`, a re-export that reformatted the markup -- would otherwise
	// yield an empty set, and "no tab has the wrong glyph" is what an empty set says.
	if divs := strings.Count(block, "<div"); divs != 1+len(tabbarItemRe.FindAllString(block, -1)) {
		t.Fatalf("PB-DS-6: the artifact's `.ptabs` block holds %d nested elements and %d of them "+
			"read as `<svg><path/></svg>Label`:\n%s", divs-1,
			len(tabbarItemRe.FindAllString(block, -1)), block)
	}

	var out []tabbarGlyph
	for _, m := range tabbarItemRe.FindAllStringSubmatch(block, -1) {
		box, err := tabbarViewBox(m[1])
		if err != nil {
			t.Errorf("PB-DS-6: the `%s` tab's viewBox %q: %v", m[3], m[1], err)
			continue
		}
		out = append(out, tabbarGlyph{
			Label:    strings.TrimSpace(m[3]),
			ViewBox:  box,
			PathData: m[2],
		})
	}
	if len(out) == 0 {
		t.Fatalf("PB-DS-6: no tab parsed from the artifact's `.ptabs` block")
	}
	return out
}

// tabbarNestedBlock returns src up to the `</div>` that closes the element src opens.
func tabbarNestedBlock(src string) string {
	depth := 0
	for i := 0; i < len(src); i++ {
		switch {
		case strings.HasPrefix(src[i:], "<div"):
			depth++
		case strings.HasPrefix(src[i:], "</div>"):
			depth--
			if depth == 0 {
				return src[:i+len("</div>")]
			}
		}
	}
	return src
}

func tabbarViewBox(raw string) ([4]float64, error) {
	var out [4]float64
	fields := strings.Fields(strings.ReplaceAll(raw, ",", " "))
	if len(fields) != 4 {
		return out, fmt.Errorf("has %d values, want 4", len(fields))
	}
	for i, f := range fields {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return out, fmt.Errorf("value %q is not a number", f)
		}
		out[i] = v
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Path data, as a token stream rather than as a string.
// ---------------------------------------------------------------------------

// tabbarPathArity is how many parameters each path command takes per group.
var tabbarPathArity = map[byte]int{
	'm': 2, 'l': 2, 'h': 1, 'v': 1, 'c': 6, 's': 4, 'q': 4, 't': 2, 'a': 7, 'z': 0,
}

// tabbarPathTokens canonicalises SVG path data so the drawable can join to the artifact's `d`
// WITHOUT being byte-identical to it.
//
// THE SETTINGS GLYPH IS WHY THIS FUNCTION EXISTS. Its path is `M12 8a4 4 0 110 8 4 4 0 010-8z`,
// and `110` there is not the number one hundred and ten: SVG lets an elliptical arc's two flags
// run together with the parameter after them, so it is `1`, `1`, `0`. Android's PathParser reads
// arc parameters as seven plain numbers and does not implement that rule, so the artifact's own
// spelling parses to a different arc on the device -- silently, as a shape rather than an error.
// The drawable therefore separates the flags, and this function makes the two spellings compare
// equal by tokenising both under the SVG rule. Numbers are normalised through a float on the way
// out, so `1.70` and `1.7` are the same token and `.5` is `0.5`.
func tabbarPathTokens(d string) ([]string, error) {
	var out []string
	var cmd byte
	group := 0

	i := 0
	skip := func() {
		for i < len(d) && (d[i] == ' ' || d[i] == ',' || d[i] == '\n' || d[i] == '\t') {
			i++
		}
	}
	number := func() error {
		start := i
		if i < len(d) && (d[i] == '+' || d[i] == '-') {
			i++
		}
		for i < len(d) && (d[i] >= '0' && d[i] <= '9' || d[i] == '.') {
			i++
		}
		if i < len(d) && (d[i] == 'e' || d[i] == 'E') {
			i++
			if i < len(d) && (d[i] == '+' || d[i] == '-') {
				i++
			}
			for i < len(d) && d[i] >= '0' && d[i] <= '9' {
				i++
			}
		}
		if i == start {
			return fmt.Errorf("expected a number at offset %d of %q", start, d)
		}
		v, err := strconv.ParseFloat(d[start:i], 64)
		if err != nil {
			return fmt.Errorf("%q at offset %d is not a number: %w", d[start:i], start, err)
		}
		out = append(out, strconv.FormatFloat(v, 'g', -1, 64))
		return nil
	}

	for {
		skip()
		if i >= len(d) {
			return out, nil
		}
		c := d[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
			lower := c | 0x20
			if _, ok := tabbarPathArity[lower]; !ok {
				return nil, fmt.Errorf("%q is not a path command (offset %d of %q)", c, i, d)
			}
			cmd, group = c, 0
			out = append(out, string(c))
			i++
			continue
		}
		if cmd == 0 {
			return nil, fmt.Errorf("path data starts with %q rather than a command: %q", c, d)
		}
		arity := tabbarPathArity[cmd|0x20]
		if arity == 0 {
			return nil, fmt.Errorf("%q takes no parameters and is followed by %q in %q", cmd, c, d)
		}
		// The two arc flags are single digits, and only there does the position matter.
		if cmd|0x20 == 'a' && (group == 3 || group == 4) {
			if c != '0' && c != '1' {
				return nil, fmt.Errorf("arc flag %q at offset %d of %q is not 0 or 1", c, i, d)
			}
			out = append(out, string(c))
			i++
			group = (group + 1) % arity
			continue
		}
		if err := number(); err != nil {
			return nil, err
		}
		group = (group + 1) % arity
	}
}

// ---------------------------------------------------------------------------
// The drawables.
// ---------------------------------------------------------------------------

// tabbarDrawableName is the resource a tab's glyph must be: derived from the label the artifact
// draws it next to, so the join needs no table of its own.
func tabbarDrawableName(label string) string {
	return "swarm_tab_" + strings.ToLower(label)
}

var (
	tabbarAndroidAttrRe = regexp.MustCompile(`android:([A-Za-z]+)\s*=\s*"([^"]*)"`)
	tabbarPathElemRe    = regexp.MustCompile(`(?s)<path\b(.*?)/>`)
	tabbarVectorElemRe  = regexp.MustCompile(`(?s)<vector\b(.*?)>`)
)

func tabbarAttrs(src string) map[string]string {
	out := map[string]string{}
	for _, m := range tabbarAndroidAttrRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = m[2]
	}
	return out
}

func tabbarDrawableDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(appModule(t), "src", "main", "res", "drawable")
}

// TestPBDS6_EveryTabGlyphIsTheArtifactsPath is the join, in both directions.
func TestPBDS6_EveryTabGlyphIsTheArtifactsPath(t *testing.T) {
	glyphs := tabbarGlyphs(t)
	css := s22bSharedCSS(t)

	// The box the artifact draws the glyph in, read from `.ptabs svg`. It is 22 px, not the 24 the
	// viewBox declares: the viewBox is the coordinate space the path is written in and the CSS is
	// the size it renders at, and conflating them scales every glyph by 24/22.
	svg, ok := css[".ptabs svg"]
	if !ok {
		t.Fatalf("PB-DS-6: the artifact declares no `.ptabs svg` rule, so the size and stroke of "+
			"every glyph below would be a number chosen here. Rules parsed: %d", len(css))
	}
	wantPx := map[string]float64{}
	for _, prop := range []string{"width", "height"} {
		px, isPx := s22bPx(svg.Decls[prop])
		if !isPx {
			t.Fatalf("PB-DS-6: `.ptabs svg { %s: %s }` is not a px length", prop, svg.Decls[prop])
		}
		wantPx[prop] = px
	}
	wantStroke, err := strconv.ParseFloat(strings.TrimSpace(svg.Decls["stroke-width"]), 64)
	if err != nil {
		t.Fatalf("PB-DS-6: `.ptabs svg { stroke-width: %s }` is not a number: %v",
			svg.Decls["stroke-width"], err)
	}
	if fill := strings.TrimSpace(svg.Decls["fill"]); fill != "none" {
		t.Errorf("PB-DS-6: `.ptabs svg` declares `fill: %s`, and the drawables below are asserted "+
			"to carry no fill at all. A filled glyph is a different drawing.", fill)
	}
	if stroke := strings.TrimSpace(svg.Decls["stroke"]); stroke != "currentColor" {
		t.Errorf("PB-DS-6: `.ptabs svg` declares `stroke: %s`. The kit tints these drawables with "+
			"the tab's own ink, which is what `currentColor` means; any other value is a colour "+
			"the glyph carries itself.", stroke)
	}

	claimed := map[string]string{}
	for _, glyph := range glyphs {
		name := tabbarDrawableName(glyph.Label)
		file := filepath.Join(tabbarDrawableDir(t), name+".xml")
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("PB-DS-6: the artifact draws a `%s` tab with a glyph and %s does not exist. "+
				"`TabItem.icon` is null at every call site, so the bar renders four bare labels "+
				"where the design draws a glyph over each one.\n\tpath d=%q\n\t%v",
				glyph.Label, mustRel(t, file), glyph.PathData, err)
			continue
		}
		claimed[name] = glyph.Label
		tabbarAssertDrawable(t, file, string(raw), glyph, wantPx, wantStroke)
	}

	// UNDRAWN TAB FAILS, above. STRAY DRAWABLE FAILS, here: a glyph nobody's tab claims is a
	// drawing that ships in the APK and answers to no design fact.
	entries, err := os.ReadDir(tabbarDrawableDir(t))
	if err != nil {
		t.Fatalf("PB-DS-6: %s is unreadable: %v", mustRel(t, tabbarDrawableDir(t)), err)
	}
	var stray []string
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".xml")
		if !strings.HasPrefix(name, "swarm_tab_") {
			continue
		}
		if _, ok := claimed[name]; !ok {
			stray = append(stray, e.Name())
		}
	}
	sort.Strings(stray)
	if len(stray) > 0 {
		t.Errorf("PB-DS-6: %s holds tab glyphs the artifact draws no tab for: %s",
			mustRel(t, tabbarDrawableDir(t)), strings.Join(stray, ", "))
	}
}

// tabbarAssertDrawable compares one vector drawable against the artifact's own glyph.
func tabbarAssertDrawable(
	t *testing.T,
	file, raw string,
	glyph tabbarGlyph,
	wantPx map[string]float64,
	wantStroke float64,
) {
	t.Helper()
	where := mustRel(t, file)

	vector := tabbarVectorElemRe.FindStringSubmatch(raw)
	if vector == nil {
		t.Errorf("PB-DS-6: %s declares no <vector> element", where)
		return
	}
	paths := tabbarPathElemRe.FindAllStringSubmatch(raw, -1)
	if len(paths) != 1 {
		t.Errorf("PB-DS-6: %s declares %d <path> elements and the artifact draws the `%s` glyph "+
			"as one", where, len(paths), glyph.Label)
		return
	}
	vectorAttrs := tabbarAttrs(vector[1])
	pathAttrs := tabbarAttrs(paths[0][1])

	// SIZE, and the coordinate space it is drawn in. The two are different numbers on purpose.
	for prop, want := range wantPx {
		got, ok := vectorAttrs[prop]
		if !ok {
			t.Errorf("PB-DS-6: %s declares no android:%s; `.ptabs svg` says %gpx", where, prop, want)
			continue
		}
		if got != fmt.Sprintf("%gdp", want) {
			t.Errorf("PB-DS-6: %s has android:%s=%q; `.ptabs svg` says %gpx, and CSS px in the "+
				"386x812 frame is Android dp at 1:1", where, prop, got, want)
		}
	}
	for i, prop := range []string{"viewportWidth", "viewportHeight"} {
		want := glyph.ViewBox[2+i]
		got, ok := vectorAttrs[prop]
		if !ok {
			t.Errorf("PB-DS-6: %s declares no android:%s; the glyph's viewBox is %g wide/tall "+
				"and the path is written in those coordinates", where, prop, want)
			continue
		}
		if v, err := strconv.ParseFloat(got, 64); err != nil || v != want {
			t.Errorf("PB-DS-6: %s has android:%s=%q; the artifact's viewBox says %g. A viewport "+
				"that disagrees with the coordinate space scales the whole glyph.",
				where, prop, got, want)
		}
	}

	// THE PATH. Compared as tokens, so the drawable may separate the arc flags Android cannot
	// read run together and still be provably the artifact's own drawing.
	want, err := tabbarPathTokens(glyph.PathData)
	if err != nil {
		t.Errorf("PB-DS-6: the artifact's `%s` path does not tokenise: %v", glyph.Label, err)
		return
	}
	got, err := tabbarPathTokens(pathAttrs["pathData"])
	if err != nil {
		t.Errorf("PB-DS-6: %s: android:pathData does not tokenise: %v", where, err)
		return
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("PB-DS-6: %s draws a different shape from the one the artifact draws for `%s`.\n"+
			"\tartifact d=%q\n\t     -> %s\n\tdrawable  =%q\n\t     -> %s",
			where, glyph.Label, glyph.PathData, strings.Join(want, " "),
			pathAttrs["pathData"], strings.Join(got, " "))
	}

	// STROKE. Width from the CSS; colour present but unasserted as a value, because
	// `stroke: currentColor` says the colour is the CALLER's -- the kit tints it with the tab's
	// ink. What is asserted is that the drawable carries no colour of its own to be tinted away
	// from, which is PB-DS-11's rule and the reason `ic_swarm_wake.xml` takes the platform's white.
	if got, ok := pathAttrs["strokeWidth"]; !ok {
		t.Errorf("PB-DS-6: %s declares no android:strokeWidth; `.ptabs svg` says %g",
			where, wantStroke)
	} else if v, err := strconv.ParseFloat(got, 64); err != nil || v != wantStroke {
		t.Errorf("PB-DS-6: %s has android:strokeWidth=%q; `.ptabs svg` says %g",
			where, got, wantStroke)
	}
	if _, ok := pathAttrs["strokeColor"]; !ok {
		t.Errorf("PB-DS-6: %s declares no android:strokeColor. A VectorDrawable with neither a "+
			"fill nor a stroke colour draws nothing at all, and the tab would render an empty box "+
			"that looks exactly like the missing asset it replaced.", where)
	}
	if _, ok := pathAttrs["fillColor"]; ok {
		t.Errorf("PB-DS-6: %s declares android:fillColor and `.ptabs svg` declares `fill: none`. "+
			"A filled outline glyph is a solid shape.", where)
	}
	if strings.Contains(raw, "#") {
		t.Errorf("PB-DS-6 / PB-DS-11: %s carries an ARGB literal. The stroke is tinted by the kit "+
			"with the tab's own ink token, so a colour written here is either dead or a second "+
			"palette.", where)
	}

	// NOTHING ELSE. Every attribute in a vector drawable is a drawing decision, and one that came
	// from neither the artifact nor a derivation row is one somebody chose while typing.
	tabbarAssertNoExtraAttrs(t, where, "<vector>", vectorAttrs,
		"width", "height", "viewportWidth", "viewportHeight")
	tabbarAssertNoExtraAttrs(t, where, "<path>", pathAttrs,
		"pathData", "strokeColor", "strokeWidth")
}

func tabbarAssertNoExtraAttrs(t *testing.T, where, elem string, got map[string]string, allowed ...string) {
	t.Helper()
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	var extra []string
	for name, value := range got {
		if !ok[name] {
			extra = append(extra, fmt.Sprintf("android:%s=%q", name, value))
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("PB-DS-6: %s's %s carries %s, which the artifact does not declare. The glyph's "+
			"line caps, joins, alpha and tint are the SVG's and Android's defaults on both sides; "+
			"stating one here is a drawing decision with no origin.",
			where, elem, strings.Join(extra, ", "))
	}
}

// TestPBDS6_ThePathTokeniserRefusesPerturbedInput is the negative control for the join above.
//
// The tokeniser is the whole comparison: if it returned the same tokens for two different
// drawings, every glyph in the app would be green against every path in the artifact.
func TestPBDS6_ThePathTokeniserRefusesPerturbedInput(t *testing.T) {
	// The SVG flag rule, which is the reason this function exists at all. The artifact's own
	// spelling and the spelling Android can parse must be the same drawing.
	compact, err := tabbarPathTokens("M12 8a4 4 0 110 8 4 4 0 010-8z")
	if err != nil {
		t.Fatalf("the artifact's Settings path does not tokenise: %v", err)
	}
	separated, err := tabbarPathTokens("M12,8 a4,4 0 1 1 0,8 4,4 0 0 1 0,-8 z")
	if err != nil {
		t.Fatalf("the separated spelling of the Settings path does not tokenise: %v", err)
	}
	if strings.Join(compact, " ") != strings.Join(separated, " ") {
		t.Errorf("the tokeniser reads the artifact's arc flags as one number:\n\tcompact:   %s\n"+
			"\tseparated: %s\nA drawable that separates them would then be reported as a "+
			"different shape and the join would be unsatisfiable.",
			strings.Join(compact, " "), strings.Join(separated, " "))
	}
	if got := strings.Join(compact, " "); got != "M 12 8 a 4 4 0 1 1 0 8 4 4 0 0 1 0 -8 z" {
		t.Errorf("the Settings glyph tokenises to %q, which is not two arcs of a circle centred "+
			"on (12,12). The flag rule is what separates `110` into `1 1 0`.", got)
	}

	// A different drawing must not compare equal. Each of these is one edit away from a real
	// glyph, which is the population this comparison exists to tell apart.
	for _, perturbed := range []string{
		"M12 8a4 4 0 110 8 4 4 0 010-9z", // one coordinate
		"M12 8a4 4 0 100 8 4 4 0 010-8z", // one flag: the arc sweeps the other way
		"M12 8a5 4 0 110 8 4 4 0 010-8z", // one radius
		"M12 8a4 4 0 110 8 4 4 0 010-8",  // the path never closes
		"M12 8A4 4 0 110 8 4 4 0 010-8z", // absolute where the artifact is relative
	} {
		other, err := tabbarPathTokens(perturbed)
		if err != nil {
			t.Errorf("%q does not tokenise: %v", perturbed, err)
			continue
		}
		if strings.Join(other, " ") == strings.Join(compact, " ") {
			t.Errorf("the tokeniser cannot tell the artifact's Settings glyph from %q. A "+
				"comparison that collapses two drawings passes on whichever one shipped.",
				perturbed)
		}
	}

	// And it must refuse what is not path data at all, rather than returning a short token list
	// that happens to compare equal to another short one.
	for _, notAPath := range []string{"", "12 8", "M12 8Z12", "M12 8a4 4 0 210 8z"} {
		if got, err := tabbarPathTokens(notAPath); err == nil && len(got) > 0 {
			t.Errorf("the tokeniser reads %q as path data: %v", notAPath, got)
		}
	}
}

// ---------------------------------------------------------------------------
// The bar binds them, and spends the inset once.
// ---------------------------------------------------------------------------

func tabbarKitSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone",
		"ui", "kit", "TabBar.kt")
	return kotlinCodeOnly(readFileOrFail(t, path, "PB-DS-6"))
}

// TestPBDS6_TheBarBindsEveryGlyphToItsTab closes the last link in the chain.
//
// A drawable that joins to the artifact and that nothing draws is an asset in the APK. The
// artifact puts the glyph INSIDE the tab's own div, next to the label, so the pairing is the
// artifact's and the kit either reproduces it or invents one.
func TestPBDS6_TheBarBindsEveryGlyphToItsTab(t *testing.T) {
	src := tabbarKitSource(t)
	for _, glyph := range tabbarGlyphs(t) {
		want := fmt.Sprintf("%q to R.drawable.%s", glyph.Label, tabbarDrawableName(glyph.Label))
		if !strings.Contains(src, want) {
			t.Errorf("PB-DS-6: TabBar.kt does not bind `%s`. The artifact draws that glyph inside "+
				"the `%s` tab's own element, so `%s` is the pairing and it has to appear "+
				"somewhere.\n\twant: %s", glyph.Label, glyph.Label, want, want)
		}
	}
}

// TestPBDS6_TheBarSpendsTheNavigationInsetOnceIsTheWholePoint is the double-padding fix.
//
// THE MOCK'S 14 IS A MEASUREMENT OF SOMEBODY ELSE'S PHONE. `.ptabs { padding-bottom: 14px }` in a
// 386x812 frame is the iPhone home indicator, reserved inside the bar's own 74 px box. Android
// reports the equivalent region at runtime and it is not 14 dp: a gesture-nav handset is ~24, a
// three-button one ~48. Derivation row 19 has already ruled on exactly this class of constant --
// "`screen_top` 54 is an iPhone notch constant -- on Android it must come from
// `WindowInsets.statusBars`, with 54 as the design-time preview value only" -- and row 20 states
// where the bottom one lands: the screen scaffold's padding is "bottom `screen_bottom` (or inset +
// `tabbar_height`)", which puts the inset UNDER a bar that is `tabbar_height` tall, not inside it.
//
// So the bar spends `tabbar_height` and nothing else, and `PhoneActivity.insetTheSystemBars` --
// the scaffold, and the only place in this app that reads WindowInsets -- puts the real inset
// below it. Both assertions are here rather than in one file each, because the defect is the
// PAIR: either alone is correct, and together they were 14 dp of design constant stacked on top of
// a platform measurement of the same thing.
func TestPBDS6_TheBarSpendsTheNavigationInsetOnce(t *testing.T) {
	src := tabbarKitSource(t)
	if strings.Contains(src, "setPadding") {
		line := 0
		for i, l := range strings.Split(src, "\n") {
			if strings.Contains(l, "setPadding") {
				line = i + 1
				break
			}
		}
		t.Errorf("PB-DS-6: TabBar.kt:%d pads the bar itself. Its `padding-bottom: 14px` is the "+
			"mock's home indicator, and `PhoneActivity.insetTheSystemBars` already applies the "+
			"real navigation inset under the bar (derivation row 20) -- so the bar rides 14 dp "+
			"higher than the design on every handset, and higher still on a three-button one. The "+
			"bar's box is `tabbar_height`; the inset below it is the platform's.", line)
	}

	activity := filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone",
		"PhoneActivity.kt")
	scaffold := kotlinCodeOnly(readFileOrFail(t, activity, "PB-DS-6"))
	if !strings.Contains(scaffold, "setOnApplyWindowInsetsListener") {
		t.Errorf("PB-DS-6: %s reads no window insets. The tab bar spends no bottom padding of its "+
			"own because THIS is where the real one comes from; with the listener gone the bar "+
			"sits under the navigation bar and nothing in the app compensates.",
			mustRel(t, activity))
	}
	if !regexp.MustCompile(`setPadding\([^)]*\bbars\.bottom\b[^)]*\)`).MatchString(scaffold) {
		t.Errorf("PB-DS-6: %s applies no BOTTOM system-bar inset. That inset is the tab bar's "+
			"only bottom air (derivation row 20: the scaffold's bottom padding is the inset plus "+
			"`tabbar_height`), so dropping it here drops it everywhere.", mustRel(t, activity))
	}
}
