package gate

import (
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// THE LAUNCHER ICON IS A TRANSCRIPTION, NOT A DRAWING.
//
// The mark that ships in res/ is not authored there. docs/design/icon-candidates/solid-wedge.svg
// is the artifact the owner chose out of six, and ic_launcher_foreground.xml is that file restated
// in Android's vector dialect. Two things follow, and neither is safe to trust:
//
//  1. EVERY NUMBER IN THE DRAWABLE MUST BE A NUMBER IN THE SVG. A transcription that is merely
//     close is a second design nobody approved, and it is invisible in review because each file
//     looks right on its own -- nothing on the page says what the other one holds. So this parses
//     BOTH and compares the geometry it evaluates from each. It deliberately does not restate the
//     coordinates here: a gate that carries its own copy of the answer is a third place for the
//     design to rot, and it goes stale in exactly the way the two files it is watching would.
//
//  2. NO COLOUR MAY BE WRITTEN IN THE DRAWABLE AT ALL. PB-TOK-1 makes internal/design/tokens.json
//     the single origin and android/design-tokens.tsv the reviewable join to colors.xml. A hex
//     literal in a drawable bypasses both -- it is not wrong today, it is unreachable by the fence
//     that keeps it from becoming wrong. So the assertion here is not "the icon is green": it is
//     that the icon names a resource, that the resource has a row in the join, and that the token
//     the row names carries the value the SVG paints. SVG literal -> token -> resource, checked
//     end to end, with the icon holding none of the three.
//
// WHAT THIS GATE DOES NOT ASSERT, said plainly so its absence is not read as a pass. It does not
// check the safe zone. That figure can only be had by rasterising -- the SVG's own comment records
// a hand-derived number that was wrong because it ignored what a mitred join does past the end of
// its path -- and this package must run with no renderer, no JDK and no Android SDK on the machine.
// The measured figure lives with the artwork, in the SVG's comment, and is re-measured when the
// artwork moves. The join above is what keeps the drawable from moving on its own.
const iconCandidateRelPath = "docs/design/icon-candidates/solid-wedge.svg"

// ---------------------------------------------------------------------------
// Reading the two files.
// ---------------------------------------------------------------------------

// svgRectShape and svgPathShape are the two primitives the candidate is drawn with.
type svgRectShape struct {
	X      string `xml:"x,attr"`
	Y      string `xml:"y,attr"`
	Width  string `xml:"width,attr"`
	Height string `xml:"height,attr"`
	Fill   string `xml:"fill,attr"`
}

type svgPathShape struct {
	D           string `xml:"d,attr"`
	Fill        string `xml:"fill,attr"`
	Stroke      string `xml:"stroke,attr"`
	StrokeWidth string `xml:"stroke-width,attr"`
	LineCap     string `xml:"stroke-linecap,attr"`
	LineJoin    string `xml:"stroke-linejoin,attr"`
}

// iconCandidate is the chosen SVG's structure, and the field paths are themselves an assertion:
// the ground is a rect at the top level, the mark is a path inside the group, and the cursor is a
// rect inside the group. A candidate reorganised into some other shape does not unmarshal into
// this and is reported rather than half-read.
type iconCandidate struct {
	ViewBox string         `xml:"viewBox,attr"`
	Ground  []svgRectShape `xml:"rect"`
	Marks   []svgPathShape `xml:"g>path"`
	Cursors []svgRectShape `xml:"g>rect"`
}

func loadIconCandidate(t *testing.T) iconCandidate {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(iconCandidateRelPath))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the launcher icon is a transcription of %s and that file is unreadable: %v",
			iconCandidateRelPath, err)
	}
	var out iconCandidate
	if err := xml.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s is not well-formed XML: %v", iconCandidateRelPath, err)
	}
	if len(out.Ground) != 1 || len(out.Marks) != 1 || len(out.Cursors) != 1 {
		t.Fatalf("%s holds %d ground rect(s), %d mark path(s) and %d cursor rect(s); this gate "+
			"transcribes exactly one of each, and a candidate with more shapes needs the "+
			"comparison widened rather than the extra shapes silently dropped",
			iconCandidateRelPath, len(out.Ground), len(out.Marks), len(out.Cursors))
	}
	return out
}

// resElement is any Android resource element, attributes captured by local name.
//
// The attributes are read generically rather than through namespace-tagged struct fields because
// every attribute in a drawable is in the android: namespace and encoding/xml's handling of
// namespaced ATTRIBUTES is not the same as its handling of namespaced elements. Reading by local
// name also means an attribute this gate does not know about is still visible to it, which is what
// makes the "no literal anywhere in the file" check possible.
type resElement struct {
	XMLName  xml.Name
	Attrs    []xml.Attr   `xml:",any,attr"`
	Children []resElement `xml:",any"`
}

func (e resElement) attr(name string) (string, bool) {
	for _, a := range e.Attrs {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

func (e resElement) childrenNamed(name string) []resElement {
	var out []resElement
	for _, c := range e.Children {
		if c.XMLName.Local == name {
			out = append(out, c)
		}
	}
	return out
}

func loadResourceElement(t *testing.T, path, requirement string) resElement {
	t.Helper()
	raw := readFileOrFail(t, path, requirement)
	var out resElement
	if err := xml.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("%s: %s is not well-formed XML: %v", requirement, mustRel(t, path), err)
	}
	return out
}

func launcherForegroundPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(appModule(t), "src", "main", "res", "drawable", "ic_launcher_foreground.xml")
}

func mipmapDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(appModule(t), "src", "main", "res", "mipmap-anydpi-v26")
}

// ---------------------------------------------------------------------------
// Path data, evaluated rather than compared as text.
// ---------------------------------------------------------------------------

// iconPathPoints turns path data into the absolute points the pen visits.
//
// It evaluates rather than string-matches on purpose. `M62,67.75 L80,67.75` and `M62,67.75h18` are
// the same drawing, and a gate that only compares spelling would report the second as a design
// change; worse, it would let a real change through whenever the spelling happened to survive. The
// evaluated subset is M, L, H, V and Z in either case, which is the whole of both files -- a curve
// or an arc here would be a different mark, and this returns an error saying so rather than
// skipping the command it cannot read.
func iconPathPoints(d string) (pts [][2]float64, closed bool, err error) {
	toks := iconPathTokens(d)
	var cx, cy float64
	var cmd byte
	seenMove := false
	i := 0
	for i < len(toks) {
		tok := toks[i]
		if len(tok) == 1 && isIconPathCommand(tok[0]) {
			cmd = tok[0]
			i++
			if cmd == 'Z' || cmd == 'z' {
				closed = true
			}
			continue
		}
		if cmd == 0 {
			return nil, false, fmt.Errorf("path data %q begins with a number rather than a command", d)
		}
		relative := cmd >= 'a'
		var operands int
		switch cmd &^ 0x20 {
		case 'M', 'L':
			operands = 2
		case 'H', 'V':
			operands = 1
		case 'Z':
			return nil, false, fmt.Errorf("path data %q gives operands to Z", d)
		default:
			return nil, false, fmt.Errorf(
				"path data %q uses command %q; this gate evaluates only straight-line commands "+
					"(M, L, H, V, Z), and a curve in the launcher mark is a different drawing "+
					"rather than a transcription of one", d, string(cmd))
		}
		if i+operands > len(toks) {
			return nil, false, fmt.Errorf("path data %q: command %q wants %d number(s) and the data ends",
				d, string(cmd), operands)
		}
		nums := make([]float64, operands)
		for k := 0; k < operands; k++ {
			v, convErr := strconv.ParseFloat(toks[i+k], 64)
			if convErr != nil {
				return nil, false, fmt.Errorf("path data %q: %q is not a number", d, toks[i+k])
			}
			nums[k] = v
		}
		i += operands
		switch cmd &^ 0x20 {
		case 'M', 'L':
			if relative {
				cx, cy = cx+nums[0], cy+nums[1]
			} else {
				cx, cy = nums[0], nums[1]
			}
		case 'H':
			if relative {
				cx += nums[0]
			} else {
				cx = nums[0]
			}
		case 'V':
			if relative {
				cy += nums[0]
			} else {
				cy = nums[0]
			}
		}
		if cmd == 'M' || cmd == 'm' {
			if seenMove {
				return nil, false, fmt.Errorf(
					"path data %q starts a second subpath; both shapes in the launcher icon are "+
						"single subpaths and this comparator would flatten the break into the "+
						"point list, where it would stop being visible", d)
			}
			seenMove = true
			// SVG's implicit repeat: the pairs after a moveto are linetos, and treating them as
			// further movetos would silently break the stroke into pieces.
			cmd = 'L' | (cmd & 0x20)
		}
		pts = append(pts, [2]float64{cx, cy})
	}
	if len(pts) == 0 {
		return nil, false, fmt.Errorf("path data %q visits no points", d)
	}
	return pts, closed, nil
}

func isIconPathCommand(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// iconPathTokens splits path data into command letters and numbers. A leading minus starts a new
// number without a separator before it, which is the one piece of the grammar that is not
// whitespace-delimited.
func iconPathTokens(d string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range d {
		switch {
		case r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		case r < 128 && isIconPathCommand(byte(r)):
			flush()
			out = append(out, string(r))
		case r == '-':
			flush()
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// ---------------------------------------------------------------------------
// The geometry, normalised so both files reduce to the same thing.
// ---------------------------------------------------------------------------

type iconGeometry struct {
	Viewport    [2]float64
	MarkPts     [][2]float64
	MarkClosed  bool
	StrokeWidth float64
	LineCap     string
	LineJoin    string
	// CursorPts is sorted, because a rectangle written clockwise from the top-left and one written
	// anticlockwise from the bottom-right are the same rectangle and neither is more correct.
	CursorPts [][2]float64
}

// diff is the whole comparison, in one place so the negative control below can drive it with a
// perturbed input and watch it fail. A comparator only reachable through the file it reads is a
// comparator nobody can prove is able to say no.
func (want iconGeometry) diff(got iconGeometry) []string {
	var out []string
	const eps = 1e-9
	sameNum := func(a, b float64) bool { return math.Abs(a-b) < eps }
	samePts := func(a, b [][2]float64) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if !sameNum(a[i][0], b[i][0]) || !sameNum(a[i][1], b[i][1]) {
				return false
			}
		}
		return true
	}
	if !sameNum(want.Viewport[0], got.Viewport[0]) || !sameNum(want.Viewport[1], got.Viewport[1]) {
		out = append(out, fmt.Sprintf("viewport %gx%g, want %gx%g",
			got.Viewport[0], got.Viewport[1], want.Viewport[0], want.Viewport[1]))
	}
	if !samePts(want.MarkPts, got.MarkPts) {
		out = append(out, fmt.Sprintf("the mark visits %s, want %s",
			formatIconPts(got.MarkPts), formatIconPts(want.MarkPts)))
	}
	if want.MarkClosed != got.MarkClosed {
		out = append(out, fmt.Sprintf("the mark is closed=%v, want closed=%v; the zigzag is an open "+
			"polyline and closing it draws a fourth leg back to the start", got.MarkClosed, want.MarkClosed))
	}
	if !sameNum(want.StrokeWidth, got.StrokeWidth) {
		out = append(out, fmt.Sprintf("stroke width %g, want %g", got.StrokeWidth, want.StrokeWidth))
	}
	if want.LineCap != got.LineCap {
		out = append(out, fmt.Sprintf("stroke line cap %q, want %q; the candidate cuts both "+
			"terminals flat at the diagonal's own angle", got.LineCap, want.LineCap))
	}
	if want.LineJoin != got.LineJoin {
		out = append(out, fmt.Sprintf("stroke line join %q, want %q; the reversals are mitred and "+
			"a round join takes the points off them", got.LineJoin, want.LineJoin))
	}
	if !samePts(want.CursorPts, got.CursorPts) {
		out = append(out, fmt.Sprintf("the cursor covers %s, want %s",
			formatIconPts(got.CursorPts), formatIconPts(want.CursorPts)))
	}
	return out
}

func formatIconPts(pts [][2]float64) string {
	parts := make([]string, 0, len(pts))
	for _, p := range pts {
		parts = append(parts, fmt.Sprintf("(%g,%g)", p[0], p[1]))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func sortIconPts(pts [][2]float64) [][2]float64 {
	out := append([][2]float64(nil), pts...)
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

func iconFloat(t *testing.T, raw, what string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		t.Fatalf("%s is %q, which is not a number: %v", what, raw, err)
	}
	return v
}

// candidateGeometry is the chosen SVG reduced to the normalised form.
func candidateGeometry(t *testing.T, svg iconCandidate) iconGeometry {
	t.Helper()
	box := strings.Fields(svg.ViewBox)
	if len(box) != 4 {
		t.Fatalf("%s has viewBox %q, which is not four numbers", iconCandidateRelPath, svg.ViewBox)
	}
	pts, closed, err := iconPathPoints(svg.Marks[0].D)
	if err != nil {
		t.Fatalf("%s: the mark's own path data does not evaluate: %v", iconCandidateRelPath, err)
	}
	r := svg.Cursors[0]
	x := iconFloat(t, r.X, "the candidate cursor's x")
	y := iconFloat(t, r.Y, "the candidate cursor's y")
	w := iconFloat(t, r.Width, "the candidate cursor's width")
	h := iconFloat(t, r.Height, "the candidate cursor's height")
	return iconGeometry{
		Viewport:    [2]float64{iconFloat(t, box[2], "viewBox width"), iconFloat(t, box[3], "viewBox height")},
		MarkPts:     pts,
		MarkClosed:  closed,
		StrokeWidth: iconFloat(t, svg.Marks[0].StrokeWidth, "the candidate mark's stroke-width"),
		LineCap:     strings.TrimSpace(svg.Marks[0].LineCap),
		LineJoin:    strings.TrimSpace(svg.Marks[0].LineJoin),
		CursorPts:   sortIconPts([][2]float64{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}),
	}
}

// foregroundGeometry is the shipped drawable reduced to the same form.
//
// Which path is which is decided by how it is painted rather than by its position in the file: the
// mark is the stroke-only path and the cursor is the fill-only one. Asking for them by index would
// make the element order part of the specification, which it is not.
func foregroundGeometry(t *testing.T, vec resElement) iconGeometry {
	t.Helper()
	if vec.XMLName.Local != "vector" {
		t.Fatalf("ic_launcher_foreground.xml has root <%s>, want <vector>", vec.XMLName.Local)
	}
	paths := vec.childrenNamed("path")
	if len(paths) != 2 {
		t.Fatalf("ic_launcher_foreground.xml declares %d <path> element(s); the candidate is one "+
			"stroked zigzag and one filled cursor, so it transcribes to exactly two", len(paths))
	}
	var stroked, filled *resElement
	for i := range paths {
		_, hasStroke := paths[i].attr("strokeColor")
		_, hasFill := paths[i].attr("fillColor")
		switch {
		case hasStroke && !hasFill:
			stroked = &paths[i]
		case hasFill && !hasStroke:
			filled = &paths[i]
		}
	}
	if stroked == nil || filled == nil {
		t.Fatalf("ic_launcher_foreground.xml does not hold exactly one stroke-only path (the "+
			"zigzag: the candidate paints it fill=none) and one fill-only path (the cursor: it is "+
			"the one filled shape in the candidate, which is what keeps it reading as a cursor "+
			"beside a stroked letter). Found stroked=%v filled=%v", stroked != nil, filled != nil)
	}

	need := func(e resElement, name string) string {
		v, ok := e.attr(name)
		if !ok {
			t.Fatalf("ic_launcher_foreground.xml: a <path> has no android:%s, so the transcription "+
				"leaves it to the platform default rather than saying what the candidate says", name)
		}
		return v
	}
	markPts, markClosed, err := iconPathPoints(need(*stroked, "pathData"))
	if err != nil {
		t.Fatalf("ic_launcher_foreground.xml: the mark's pathData does not evaluate: %v", err)
	}
	cursorPts, cursorClosed, err := iconPathPoints(need(*filled, "pathData"))
	if err != nil {
		t.Fatalf("ic_launcher_foreground.xml: the cursor's pathData does not evaluate: %v", err)
	}
	if !cursorClosed {
		t.Errorf("ic_launcher_foreground.xml: the cursor's pathData is not closed. The candidate " +
			"draws a <rect>, and an unclosed four-point fill relies on the renderer implying the " +
			"last edge.")
	}
	if len(cursorPts) != 4 {
		t.Fatalf("ic_launcher_foreground.xml: the cursor visits %d points; the candidate's <rect> "+
			"transcribes to four corners", len(cursorPts))
	}

	return iconGeometry{
		Viewport: [2]float64{
			iconFloat(t, need(vec, "viewportWidth"), "android:viewportWidth"),
			iconFloat(t, need(vec, "viewportHeight"), "android:viewportHeight"),
		},
		MarkPts:     markPts,
		MarkClosed:  markClosed,
		StrokeWidth: iconFloat(t, need(*stroked, "strokeWidth"), "android:strokeWidth"),
		LineCap:     need(*stroked, "strokeLineCap"),
		LineJoin:    need(*stroked, "strokeLineJoin"),
		CursorPts:   sortIconPts(cursorPts),
	}
}

// ---------------------------------------------------------------------------
// The colour chain: SVG literal -> token -> resource, with the icon holding none of it.
// ---------------------------------------------------------------------------

type iconPalette struct {
	colours map[string]string
	tokenOf map[string]string
	tokens  designTokens
}

func loadIconPalette(t *testing.T) iconPalette {
	t.Helper()
	p := iconPalette{
		colours: androidColors(t),
		tokenOf: map[string]string{},
		tokens:  loadDesignTokens(t),
	}
	for _, row := range loadTokenMap(t) {
		if row.Kind == "color" {
			p.tokenOf[row.Resource] = row.Token
		}
	}
	return p
}

// resolve checks one colour reference all the way back to the design origin and reports the token
// it lands on. `where` names the thing being painted, so a failure says which half of the mark is
// wrong without the reader opening the file.
func (p iconPalette) resolve(t *testing.T, ref, wantLiteral, where string) {
	t.Helper()
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "@color/") {
		t.Errorf("PB-TOK-1: %s is painted %q. A colour written in a drawable is a colour that "+
			"entered the product without passing through internal/design/tokens.json and "+
			"android/design-tokens.tsv, which is the single origin the token gate exists to "+
			"defend. Reference a @color/ resource.", where, ref)
		return
	}
	name := strings.TrimPrefix(ref, "@color/")
	argb, ok := p.colours[name]
	if !ok {
		t.Errorf("PB-TOK-1: %s references @color/%s, which values/colors.xml does not declare", where, name)
		return
	}
	token, ok := p.tokenOf[name]
	if !ok {
		t.Errorf("PB-TOK-1: %s references @color/%s, which has no row in android/design-tokens.tsv, "+
			"so nothing says which design token it is", where, name)
		return
	}
	value, ok := p.tokens.Tokens[token]
	if !ok {
		t.Errorf("PB-TOK-1: %s resolves to token %s, which internal/design/tokens.json does not declare",
			where, token)
		return
	}
	fromOrigin, err := argbFromToken(value)
	if err != nil {
		t.Errorf("PB-TOK-1: %s resolves to token %s, whose value %q is not a colour: %v",
			where, token, value, err)
		return
	}
	if fromOrigin != argb {
		t.Errorf("PB-TOK-1: %s references @color/%s = %s, but token %s carries %s",
			where, name, argb, token, fromOrigin)
		return
	}
	wanted, err := argbFromToken(wantLiteral)
	if err != nil {
		t.Fatalf("%s paints %s as %q, which is not a colour this gate can read: %v",
			iconCandidateRelPath, where, wantLiteral, err)
	}
	if wanted != argb {
		t.Errorf("%s is @color/%s (token %s) = %s, but %s paints it %s. The drawable is a "+
			"transcription of the candidate and the two must be the same colour.",
			where, name, token, argb, iconCandidateRelPath, wanted)
	}
}

// ---------------------------------------------------------------------------
// The requirements.
// ---------------------------------------------------------------------------

// TestLauncherForegroundIsTheChosenIconCandidate is the join: the shipped mark is Solid Wedge,
// at Solid Wedge's own coordinates, in Solid Wedge's colours, none of which it holds itself.
func TestLauncherForegroundIsTheChosenIconCandidate(t *testing.T) {
	svg := loadIconCandidate(t)
	vec := loadResourceElement(t, launcherForegroundPath(t), "the launcher icon")

	want := candidateGeometry(t, svg)
	got := foregroundGeometry(t, vec)

	for _, d := range want.diff(got) {
		t.Errorf("the launcher foreground is not the chosen candidate: %s\n"+
			"\tThe owner chose %s out of six. The drawable is a transcription of it and every "+
			"number in it comes from that file, not from judgement at the keyboard.",
			d, iconCandidateRelPath)
	}

	// The width and height are the viewport in dp: the adaptive canvas is 108dp and a drawable
	// whose intrinsic size disagrees with its viewport scales the mark out of the safe zone.
	for _, row := range []struct{ attr, want string }{
		{"width", fmt.Sprintf("%gdp", want.Viewport[0])},
		{"height", fmt.Sprintf("%gdp", want.Viewport[1])},
	} {
		if v, _ := vec.attr(row.attr); v != row.want {
			t.Errorf("ic_launcher_foreground.xml: android:%s is %q, want %q (the candidate's own "+
				"viewBox, so the intrinsic size and the viewport agree at 1:1)", row.attr, v, row.want)
		}
	}
}

// TestLauncherForegroundSpendsOnlyTokenColours is the other half, and it is deliberately blind to
// which shape is which.
//
// It walks EVERY attribute of every element in the drawable and judges anything that names a
// colour. Asking only about the two attributes this gate expects would leave a literal in a third
// one -- a tint, a gradient stop, a future <group> -- unread, and "no hardcoded colour in the icon"
// has to mean the file rather than the parts of it someone thought to look at.
func TestLauncherForegroundSpendsOnlyTokenColours(t *testing.T) {
	svg := loadIconCandidate(t)
	vec := loadResourceElement(t, launcherForegroundPath(t), "the launcher icon")
	palette := loadIconPalette(t)

	// The candidate paints the zigzag and the cursor the same colour, so there is one colour in
	// the foreground and every reference in the file has to be it.
	if svg.Marks[0].Stroke != svg.Cursors[0].Fill {
		t.Fatalf("%s paints the mark %q and the cursor %q; this gate assumes one foreground colour "+
			"and needs widening before a two-colour candidate can ship",
			iconCandidateRelPath, svg.Marks[0].Stroke, svg.Cursors[0].Fill)
	}

	found := 0
	var walk func(resElement, string)
	walk = func(e resElement, path string) {
		for _, a := range e.Attrs {
			name := a.Name.Local
			if !strings.Contains(strings.ToLower(name), "color") && !strings.Contains(strings.ToLower(name), "tint") {
				continue
			}
			found++
			palette.resolve(t, a.Value, svg.Marks[0].Stroke, fmt.Sprintf("%s's android:%s", path, name))
		}
		for i, c := range e.Children {
			walk(c, fmt.Sprintf("%s/%s[%d]", path, c.XMLName.Local, i))
		}
	}
	walk(vec, "<vector>")

	if found == 0 {
		t.Error("ic_launcher_foreground.xml names no colour at all, so this check asserted nothing " +
			"and the icon would draw in whatever the platform defaults to")
	}
}

// TestLauncherAdaptiveIconDeclaresAllThreeLayers covers the layer Android 13 added.
//
// A themed icon reuses the FOREGROUND, tinted flat by the system. An adaptive icon with no
// <monochrome> is not broken -- the launcher falls back to the full-colour icon -- which is exactly
// why its absence survives review: nothing looks wrong until someone turns themed icons on.
func TestLauncherAdaptiveIconDeclaresAllThreeLayers(t *testing.T) {
	svg := loadIconCandidate(t)
	palette := loadIconPalette(t)

	for _, file := range []string{"ic_launcher.xml", "ic_launcher_round.xml"} {
		root := loadResourceElement(t, filepath.Join(mipmapDir(t), file), "the launcher icon")
		if root.XMLName.Local != "adaptive-icon" {
			t.Errorf("%s has root <%s>, want <adaptive-icon>", file, root.XMLName.Local)
			continue
		}

		layers := map[string]string{}
		for _, name := range []string{"background", "foreground", "monochrome"} {
			children := root.childrenNamed(name)
			if len(children) != 1 {
				t.Errorf("%s declares %d <%s> layer(s), want exactly 1. Android 13 tints the "+
					"foreground for themed icons; without <monochrome> the launcher quietly falls "+
					"back to the full-colour icon and nothing looks wrong until themed icons are on.",
					file, len(children), name)
				continue
			}
			ref, ok := children[0].attr("drawable")
			if !ok {
				t.Errorf("%s: <%s> has no android:drawable", file, name)
				continue
			}
			layers[name] = strings.TrimSpace(ref)
		}

		if bg, ok := layers["background"]; ok {
			palette.resolve(t, bg, svg.Ground[0].Fill, file+"'s background layer")
		}
		const wantFg = "@drawable/ic_launcher_foreground"
		for _, name := range []string{"foreground", "monochrome"} {
			if ref, ok := layers[name]; ok && ref != wantFg {
				t.Errorf("%s: the %s layer is %q, want %q. The themed icon must be the SAME "+
					"artwork as the foreground; a second drawable is a second mark that can drift.",
					file, name, ref, wantFg)
			}
		}
	}
}

// TestLauncherIconComparisonCanActuallyFail is the negative control.
//
// Every assertion above is "these two files agree", and that shape of assertion passes both when
// the files agree and when the comparator cannot tell them apart. This drives the same comparator
// with perturbed inputs, IN MEMORY -- the working tree is shared with other agents and a gate that
// proves itself by editing a checked-in file is a gate that can lose someone else's work.
func TestLauncherIconComparisonCanActuallyFail(t *testing.T) {
	svg := loadIconCandidate(t)
	base := candidateGeometry(t, svg)

	if d := base.diff(base); len(d) != 0 {
		t.Fatalf("the candidate does not equal itself, so the comparator reports differences that "+
			"are not there: %v", d)
	}

	for _, tc := range []struct {
		name    string
		perturb func(iconGeometry) iconGeometry
	}{
		{"one coordinate of the zigzag moves by a tenth of a dp", func(g iconGeometry) iconGeometry {
			g.MarkPts = append([][2]float64(nil), g.MarkPts...)
			g.MarkPts[1] = [2]float64{g.MarkPts[1][0] + 0.1, g.MarkPts[1][1]}
			return g
		}},
		{"the stroke weight goes back up to the 10 the candidate came down from", func(g iconGeometry) iconGeometry {
			g.StrokeWidth = 10
			return g
		}},
		{"the terminals are rounded", func(g iconGeometry) iconGeometry {
			g.LineCap = "round"
			return g
		}},
		{"the reversals are rounded", func(g iconGeometry) iconGeometry {
			g.LineJoin = "round"
			return g
		}},
		{"the cursor bar grows a dp to the right", func(g iconGeometry) iconGeometry {
			g.CursorPts = append([][2]float64(nil), g.CursorPts...)
			g.CursorPts[3] = [2]float64{g.CursorPts[3][0] + 1, g.CursorPts[3][1]}
			return g
		}},
		{"the zigzag closes into a triangle", func(g iconGeometry) iconGeometry {
			g.MarkClosed = !g.MarkClosed
			return g
		}},
		{"the canvas is not the adaptive 108", func(g iconGeometry) iconGeometry {
			g.Viewport = [2]float64{96, 96}
			return g
		}},
	} {
		if d := base.diff(tc.perturb(base)); len(d) == 0 {
			t.Errorf("the comparator accepts a drawable where %s, so its agreement with the "+
				"candidate means nothing", tc.name)
		}
	}

	// The path evaluator is the other half: if it read the two dialects differently, two identical
	// drawings would compare as different and the gate would be noise rather than a fence.
	for _, tc := range []struct{ a, b string }{
		{"M 62 67.75 L 80 67.75 L 80 76.25 L 62 76.25 Z", "M62,67.75h18v8.5h-18z"},
		{"M 57 36 L 34 48", "M57,36L34,48"},
	} {
		pa, ca, err := iconPathPoints(tc.a)
		if err != nil {
			t.Fatalf("path evaluator rejects %q: %v", tc.a, err)
		}
		pb, cb, err := iconPathPoints(tc.b)
		if err != nil {
			t.Fatalf("path evaluator rejects %q: %v", tc.b, err)
		}
		if ca != cb || len((iconGeometry{MarkPts: pa}).diff(iconGeometry{MarkPts: pb})) != 0 {
			t.Errorf("the path evaluator reads %q and %q as different drawings; they are the same "+
				"one written in the two dialects this gate has to compare", tc.a, tc.b)
		}
	}
}
