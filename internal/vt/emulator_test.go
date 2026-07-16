package vt

// Epic 2 Emulator-wrapper test suite (implementation-goals.md E2.2–E2.6).
//
// These are FAILING-FIRST tests: they exercise the frozen production API
// (NewEmulator/Feed/Snapshot/Resize + DecodeSnapshot/Snap/Line/Run) that a
// separate implementer will build. Until that code exists, this package does
// not compile — the only errors must be "undefined" for the new API symbols.
// The E2.1 validation fixture (spike_test.go, package vt_test) is intentionally
// left untouched; it keeps guarding the pinned x/vt version.
//
// CONCURRENCY CONTRACT (system-spec Implicit for Epic 2; build-plan Epic 2):
// Feed is single-goroutine — the caller serializes calls — and Snapshot is
// atomic with respect to Feed. The suite therefore only exercises interleaved
// Feed/Snapshot on one goroutine (TestConcurrencyContract_SnapshotBetweenFeeds);
// there is deliberately no goroutine race test, since concurrent Feed is
// outside the contract.
//
// GOLDEN-GRID ORACLE (renderGrid): the plain-text grid is, per row, the
// concatenation of every Run.Text with trailing spaces trimmed (matching the
// spike's row() helper). This pins two obligations on the implementer:
//   - blank cells must serialize as space characters inside Run.Text, so that
//     interior spaces survive (e.g. "ALPHA line one");
//   - a double-width grapheme contributes its single grapheme to Run.Text and
//     its spacer cell contributes nothing (the run's Width, not its Text,
//     accounts for the second cell).
// Line-cell accounting ("each line exactly Cols cells wide") is verified
// separately in TestSnapshot_LineWidthInvariant so the goldens stay robust to
// trailing padding.

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// updateGolden regenerates testdata/golden/*.txt from the current
// implementation output (E2.5). Run: go test ./internal/vt/ -update
var updateGolden = flag.Bool("update", false, "regenerate golden grid files")

const (
	gridCols = 80
	gridRows = 24
)

var altExit = []byte("\x1b[?1049l") // leave alt screen, restore primary

// altFixtureBytes mirrors the spike fixture: clear+write primary, enter alt,
// draw bold-red "ALT-RED" at row3/col5 (1-based), park cursor at row7/col12.
func altFixtureBytes() []byte {
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H")
	b.WriteString("PRIMARY-HOME")
	b.WriteString("\x1b[?1049h")
	b.WriteString("\x1b[2J\x1b[H")
	b.WriteString("\x1b[3;5H")
	b.WriteString("\x1b[1;31mALT-RED\x1b[0m")
	b.WriteString("\x1b[7;12H")
	return []byte(b.String())
}

// snapshotDecode snapshots e and decodes the bytes, failing on any error.
func snapshotDecode(t *testing.T, e *Emulator) *Snap {
	t.Helper()
	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	s, err := DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	return s
}

// lineText concatenates a line's Run.Text in order (no trimming).
func lineText(l Line) string {
	var b strings.Builder
	for _, r := range l.Runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

// renderGrid is the golden-grid oracle: each row's concatenated Run.Text with
// trailing spaces trimmed, rows joined by '\n' (trailing '\n' after each row).
func renderGrid(s *Snap) string {
	var g strings.Builder
	for _, l := range s.Lines {
		g.WriteString(strings.TrimRight(lineText(l), " "))
		g.WriteByte('\n')
	}
	return g.String()
}

// runAt returns the run covering 0-based cell column col on line l.
func runAt(l Line, col int) (Run, bool) {
	x := 0
	for _, r := range l.Runs {
		w := r.Width
		if w <= 0 {
			w = utf8.RuneCountInString(r.Text)
		}
		if col >= x && col < x+w {
			return r, true
		}
		x += w
	}
	return Run{}, false
}

func mustRunAt(t *testing.T, s *Snap, row, col int) Run {
	t.Helper()
	if row < 0 || row >= len(s.Lines) {
		t.Fatalf("row %d out of range (rows=%d)", row, len(s.Lines))
	}
	r, ok := runAt(s.Lines[row], col)
	if !ok {
		t.Fatalf("no run at row %d col %d; line text=%q", row, col, lineText(s.Lines[row]))
	}
	return r
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// --- E2.2: construct, plain text, cursor --------------------------------

func TestEmulator_PlainTextAndCursorAdvance(t *testing.T) {
	// E2.2
	e := NewEmulator(20, 5)
	e.Feed([]byte("Hello"))
	s := snapshotDecode(t, e)

	if s.Version != SnapshotVersion {
		t.Errorf("Version = %d, want %d", s.Version, SnapshotVersion)
	}
	if s.Cols != 20 || s.Rows != 5 {
		t.Errorf("dims = %dx%d, want 20x5", s.Cols, s.Rows)
	}
	if len(s.Lines) != 5 {
		t.Fatalf("len(Lines) = %d, want 5", len(s.Lines))
	}
	if s.AltScreen {
		t.Errorf("AltScreen = true on primary buffer")
	}
	if got := strings.TrimRight(lineText(s.Lines[0]), " "); got != "Hello" {
		t.Errorf("row0 = %q, want %q", got, "Hello")
	}
	if s.CursorX != 5 || s.CursorY != 0 {
		t.Errorf("cursor = (%d,%d), want (5,0)", s.CursorX, s.CursorY)
	}
	if !s.CursorVisible {
		t.Errorf("CursorVisible = false, want true by default")
	}
}

// --- E2.2: SGR colors and attributes ------------------------------------

func TestEmulator_SGRColorsAndAttrs(t *testing.T) {
	// E2.2
	e := NewEmulator(20, 3)
	// R red, B blue, O bold, F faint, X italic+underline+reverse, Z red-bg.
	e.Feed([]byte("\x1b[31mR\x1b[0m" +
		"\x1b[34mB\x1b[0m" +
		"\x1b[1mO\x1b[0m" +
		"\x1b[2mF\x1b[0m" +
		"\x1b[3;4;7mX\x1b[0m" +
		"\x1b[41mZ\x1b[0m"))
	s := snapshotDecode(t, e)

	red := mustRunAt(t, s, 0, 0)
	if red.Fg == "" {
		t.Errorf("col0 R has empty Fg, want a color spec")
	}
	blue := mustRunAt(t, s, 0, 1)
	if blue.Fg == "" {
		t.Errorf("col1 B has empty Fg, want a color spec")
	}
	if red.Fg == blue.Fg {
		t.Errorf("red Fg %q not distinguishable from blue Fg %q", red.Fg, blue.Fg)
	}
	if o := mustRunAt(t, s, 0, 2); !o.Bold {
		t.Errorf("col2 O not bold")
	}
	if f := mustRunAt(t, s, 0, 3); !f.Faint {
		t.Errorf("col3 F not faint")
	}
	x := mustRunAt(t, s, 0, 4)
	if !x.Italic || !x.Underline || !x.Reverse {
		t.Errorf("col4 X attrs italic=%v underline=%v reverse=%v, want all true",
			x.Italic, x.Underline, x.Reverse)
	}
	if z := mustRunAt(t, s, 0, 5); z.Bg == "" {
		t.Errorf("col5 Z has empty Bg, want a color spec")
	}
	// A never-written cell carries no attributes or colors.
	blank := mustRunAt(t, s, 0, 15)
	if blank.Fg != "" || blank.Bg != "" || blank.Bold || blank.Italic || blank.Underline || blank.Reverse || blank.Faint {
		t.Errorf("blank cell carries styling: %+v", blank)
	}
}

// --- E2.2: cursor moves and visibility ----------------------------------

func TestEmulator_CursorMovesAndVisibility(t *testing.T) {
	// E2.2
	e := NewEmulator(20, 5)
	e.Feed([]byte("\x1b[3;7HZ")) // row3 col7 (1-based) => (x=6,y=2); Z, cursor->x=7
	s := snapshotDecode(t, e)
	if s.CursorX != 7 || s.CursorY != 2 {
		t.Errorf("cursor = (%d,%d), want (7,2)", s.CursorX, s.CursorY)
	}
	if r := mustRunAt(t, s, 2, 6); r.Text != "Z" {
		t.Errorf("cell (6,2) = %q, want %q", r.Text, "Z")
	}

	e.Feed([]byte("\x1b[?25l"))
	if s := snapshotDecode(t, e); s.CursorVisible {
		t.Errorf("CursorVisible = true after DECTCEM hide")
	}
	e.Feed([]byte("\x1b[?25h"))
	if s := snapshotDecode(t, e); !s.CursorVisible {
		t.Errorf("CursorVisible = false after DECTCEM show")
	}
}

// --- E2.2: alternate screen enter / draw / exit -------------------------

func TestEmulator_AltScreenEnterDrawExit(t *testing.T) {
	// E2.2
	e := NewEmulator(gridCols, gridRows)
	e.Feed(altFixtureBytes())
	s := snapshotDecode(t, e)

	if !s.AltScreen {
		t.Errorf("AltScreen = false after CSI ?1049h")
	}
	if s.CursorX != 11 || s.CursorY != 6 {
		t.Errorf("cursor = (%d,%d), want (11,6)", s.CursorX, s.CursorY)
	}
	if got := lineText(s.Lines[2]); !strings.Contains(got, "ALT-RED") {
		t.Errorf("alt text missing on row2: %q", got)
	}
	if got := lineText(s.Lines[0]); strings.Contains(got, "PRIMARY") {
		t.Errorf("primary content leaked onto active alt screen row0: %q", got)
	}
	a := mustRunAt(t, s, 2, 4) // 'A' of ALT-RED at col5 (1-based)
	if a.Text != "A" {
		t.Errorf("alt cell (4,2) = %q, want %q", a.Text, "A")
	}
	if !a.Bold {
		t.Errorf("alt 'A' not bold")
	}
	if a.Fg == "" {
		t.Errorf("alt 'A' has no foreground color (want red)")
	}

	e.Feed(altExit)
	s2 := snapshotDecode(t, e)
	if s2.AltScreen {
		t.Errorf("still AltScreen after CSI ?1049l")
	}
	if got := lineText(s2.Lines[0]); !strings.Contains(got, "PRIMARY-HOME") {
		t.Errorf("primary not restored after alt exit: %q", got)
	}
}

// --- E2.2: resize changes grid dimensions -------------------------------

func TestEmulator_ResizeChangesDims(t *testing.T) {
	// E2.2
	e := NewEmulator(80, 24)
	e.Feed([]byte("hi"))
	if s := snapshotDecode(t, e); s.Cols != 80 || s.Rows != 24 || len(s.Lines) != 24 {
		t.Fatalf("initial dims = %dx%d lines=%d, want 80x24 lines=24", s.Cols, s.Rows, len(s.Lines))
	}
	e.Resize(100, 40)
	if s := snapshotDecode(t, e); s.Cols != 100 || s.Rows != 40 || len(s.Lines) != 40 {
		t.Errorf("after grow dims = %dx%d lines=%d, want 100x40 lines=40", s.Cols, s.Rows, len(s.Lines))
	}
	e.Resize(40, 10)
	if s := snapshotDecode(t, e); s.Cols != 40 || s.Rows != 10 || len(s.Lines) != 10 {
		t.Errorf("after shrink dims = %dx%d lines=%d, want 40x10 lines=10", s.Cols, s.Rows, len(s.Lines))
	}
}

// --- E2.2/E2.3: structural width invariant ------------------------------

func TestSnapshot_LineWidthInvariant(t *testing.T) {
	// E2.2/E2.3: exactly Rows lines, each exactly Cols cells wide (wide chars
	// occupy content cell + spacer, so their run Width accounts for 2 cells).
	cases := []struct {
		name       string
		cols, rows int
		feed       []byte
	}{
		{"scene", 10, 3, sceneFixtureBytes()},
		{"vim", gridCols, gridRows, vimAltBefore(t)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEmulator(tc.cols, tc.rows)
			e.Feed(tc.feed)
			s := snapshotDecode(t, e)
			if len(s.Lines) != tc.rows {
				t.Fatalf("len(Lines) = %d, want %d", len(s.Lines), tc.rows)
			}
			for y, l := range s.Lines {
				sum := 0
				for _, r := range l.Runs {
					sum += r.Width
				}
				if sum != tc.cols {
					t.Errorf("row %d covers %d cells, want %d", y, sum, tc.cols)
				}
			}
		})
	}
}

// --- E2.3: snapshot fidelity (S10 content half) -------------------------

func TestSnapshotFidelity_Vim(t *testing.T) {
	// E2.3 / invariant S10 content half. Feed the real vim alt-screen capture,
	// snapshot, decode, and assert the decoded state equals the emulator's
	// known ground-truth state (content, cursor, alt flag, cursor visibility).
	before := vimAltBefore(t)
	e := NewEmulator(gridCols, gridRows)
	e.Feed(before)
	s := snapshotDecode(t, e)

	if !s.AltScreen {
		t.Errorf("AltScreen = false while vim owns the alt screen")
	}
	if s.Cols != gridCols || s.Rows != gridRows {
		t.Errorf("dims = %dx%d, want %dx%d", s.Cols, s.Rows, gridCols, gridRows)
	}
	// Ground-truth cursor/visibility for this fixture (derived from the capture).
	if s.CursorX != 0 || s.CursorY != 23 {
		t.Errorf("cursor = (%d,%d), want (0,23)", s.CursorX, s.CursorY)
	}
	if !s.CursorVisible {
		t.Errorf("CursorVisible = false, want true at end of vim paint")
	}
	grid := renderGrid(s)
	for _, w := range []string{"ALPHA line one", "BETA line two", "GAMMA line three", "~"} {
		if !strings.Contains(grid, w) {
			t.Errorf("decoded grid missing %q\n---grid---\n%s", w, grid)
		}
	}

	// Round-trip determinism: decode -> re-encode (canonical JSON of the frozen
	// Snap struct) -> decode again yields an equal structure.
	reB, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("re-encode Snap: %v", err)
	}
	s2, err := DecodeSnapshot(reB)
	if err != nil {
		t.Fatalf("DecodeSnapshot(re-encoded): %v", err)
	}
	if !reflect.DeepEqual(s, s2) {
		t.Errorf("round-trip not stable:\n first=%+v\nsecond=%+v", s, s2)
	}
}

func TestSnapshot_Deterministic(t *testing.T) {
	// E2.3: two snapshots of an unchanged emulator are byte-identical.
	e := NewEmulator(40, 6)
	e.Feed([]byte("\x1b[32mstable\x1b[0m output"))
	b1, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot 1: %v", err)
	}
	b2, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot 2: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Errorf("snapshots differ across calls with no intervening Feed")
	}
}

// --- E2.4: hostile-escape filtering (N-6) -------------------------------

// hostileSentinel is the identical payload embedded in every hostile control
// string in testdata/hostile.raw; it must never surface in the snapshot.
const hostileSentinel = "H0STILE_S3NT1NEL"

func TestHostileEscapesFiltered(t *testing.T) {
	// E2.4 / N-6. testdata/hostile.raw is the normative filter contract:
	// OSC 52 clipboard writes (BEL and ST), an OSC 0 title with an embedded
	// control byte, an OSC 2 title, DCS/APC/PM strings, an 8-bit C1 CSI, and an
	// 8-bit C1 OSC. The fixture also carries a row of Trojan-source runes as grid
	// content — NBSP (U+00A0), RLO (U+202E), ZWSP (U+200B), U+2028 (F2 amendment)
	// — which the sanitizer must fold to spaces. All printable grid content is
	// otherwise ASCII and every non-printable rune is replaced by a space before
	// serialization, so any byte in 0x80-0x9f in the snapshot is a leaked control,
	// never legitimate UTF-8.
	e := NewEmulator(gridCols, gridRows)
	e.Feed(readFixture(t, "testdata/hostile.raw"))
	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if bytes.IndexByte(b, 0x1b) >= 0 {
		t.Errorf("snapshot bytes contain ESC (0x1b)")
	}
	if bytes.IndexByte(b, 0x07) >= 0 {
		t.Errorf("snapshot bytes contain BEL (0x07)")
	}
	if bytes.IndexByte(b, 0x7f) >= 0 {
		t.Errorf("snapshot bytes contain DEL (0x7f)")
	}
	for _, c := range b {
		if c >= 0x80 && c <= 0x9f {
			t.Errorf("snapshot bytes contain raw C1 control 0x%02x", c)
			break
		}
	}
	if bytes.Contains(b, []byte(hostileSentinel)) {
		t.Errorf("hostile payload %q leaked into snapshot bytes", hostileSentinel)
	}

	s, err := DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	// Every run's text is printable-only (no control runes anywhere).
	for y, l := range s.Lines {
		for _, r := range l.Runs {
			for _, ru := range r.Text {
				if ru != ' ' && !unicode.IsPrint(ru) {
					t.Errorf("row %d run text %q contains non-printable rune %U", y, r.Text, ru)
					break
				}
			}
			if strings.Contains(r.Text, hostileSentinel) {
				t.Errorf("row %d run text leaks sentinel: %q", y, r.Text)
			}
		}
	}
	// Title is sanitized: no control runes, sentinel stripped.
	for _, ru := range s.Title {
		if ru < 0x20 || ru == 0x7f || (ru >= 0x80 && ru <= 0x9f) {
			t.Errorf("Title %q contains control rune %U", s.Title, ru)
			break
		}
	}
	if strings.Contains(s.Title, hostileSentinel) {
		t.Errorf("Title leaks sentinel: %q", s.Title)
	}
	// Printable content still survives the filter.
	grid := renderGrid(s)
	if !strings.Contains(grid, "SAFE-START") || !strings.Contains(grid, "SAFE-END") {
		t.Errorf("printable content lost through filter; grid=%q", strings.TrimSpace(grid))
	}
}

// --- E2.5: golden grids -------------------------------------------------

func TestGoldenGrids(t *testing.T) {
	// E2.5. Compare the rendered plain-text grid against committed goldens.
	// Run with -update to regenerate after verifying output by eye.
	cases := []struct {
		name       string
		cols, rows int
		feed       []byte
		golden     string
	}{
		{"scene", 10, 3, sceneFixtureBytes(), "testdata/golden/scene.txt"},
		{"vim", gridCols, gridRows, vimAltBefore(t), "testdata/golden/vim_altscreen.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEmulator(tc.cols, tc.rows)
			e.Feed(tc.feed)
			s := snapshotDecode(t, e)
			got := renderGrid(s)
			if *updateGolden {
				if err := os.WriteFile(tc.golden, []byte(got), 0o644); err != nil {
					t.Fatalf("update golden: %v", err)
				}
				return
			}
			want := readFixture(t, tc.golden)
			if got != string(want) {
				t.Errorf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", tc.name, got, want)
			}
		})
	}
}

// --- E2.6: versioning ---------------------------------------------------

func TestSnapshotVersioning(t *testing.T) {
	// E2.6
	if SnapshotVersion != 1 {
		t.Errorf("SnapshotVersion = %d, want 1", SnapshotVersion)
	}
	e := NewEmulator(10, 2)
	e.Feed([]byte("v"))
	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	s, err := DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	if s.Version != SnapshotVersion {
		t.Errorf("decoded Version = %d, want %d", s.Version, SnapshotVersion)
	}

	// A doctored future version must be rejected.
	s.Version = 999
	bad, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal doctored snap: %v", err)
	}
	if _, err := DecodeSnapshot(bad); err == nil {
		t.Errorf("DecodeSnapshot accepted version 999, want error")
	}
	// Garbage and empty input are rejected too.
	if _, err := DecodeSnapshot([]byte("not a snapshot")); err == nil {
		t.Errorf("DecodeSnapshot accepted non-JSON garbage, want error")
	}
	if _, err := DecodeSnapshot(nil); err == nil {
		t.Errorf("DecodeSnapshot accepted nil, want error")
	}
}

// --- concurrency contract (single-goroutine Feed) -----------------------

func TestConcurrencyContract_SnapshotBetweenFeeds(t *testing.T) {
	// Single-goroutine Feed contract: each Snapshot is a consistent view of all
	// bytes fed so far and none fed afterwards.
	e := NewEmulator(20, 3)
	e.Feed([]byte("AAA"))
	s1 := snapshotDecode(t, e)
	if got := strings.TrimRight(lineText(s1.Lines[0]), " "); got != "AAA" {
		t.Errorf("after first Feed row0 = %q, want %q", got, "AAA")
	}
	e.Feed([]byte("BBB"))
	s2 := snapshotDecode(t, e)
	if got := strings.TrimRight(lineText(s2.Lines[0]), " "); got != "AAABBB" {
		t.Errorf("after second Feed row0 = %q, want %q", got, "AAABBB")
	}
	// The earlier snapshot is an independent value, unaffected by the later Feed.
	if got := strings.TrimRight(lineText(s1.Lines[0]), " "); got != "AAA" {
		t.Errorf("first snapshot mutated by later Feed: row0 = %q, want %q", got, "AAA")
	}
}

// --- shared fixtures ----------------------------------------------------

// sceneFixtureBytes paints a scripted 10x3 scene: red "RED" + bold-green "GO"
// on row0, two CJK wide chars on row1, box-drawing on row2.
func sceneFixtureBytes() []byte {
	var b strings.Builder
	b.WriteString("\x1b[H")
	b.WriteString("\x1b[31mRED\x1b[0m")
	b.WriteString("\x1b[1;32mGO\x1b[0m")
	b.WriteString("\x1b[2;1H")
	b.WriteString("你好")
	b.WriteString("\x1b[3;1H")
	b.WriteString("┌──┐")
	return []byte(b.String())
}

// vimAltBefore returns the vim capture up to (and excluding) its final alt-exit
// so the emulator is left on the alt screen showing the file.
func vimAltBefore(t *testing.T) []byte {
	t.Helper()
	data := readFixture(t, "testdata/vim_altscreen.raw")
	i := bytes.LastIndex(data, altExit)
	if i < 0 {
		t.Fatalf("vim capture has no alt-screen exit sequence")
	}
	return data[:i]
}
