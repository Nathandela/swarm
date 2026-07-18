package vt

// Failing-first suite for the v0.2 committee audit fixes to the snapshot renderer:
//   - item 1: render-time control-byte sanitization (defense in depth vs. a skewed
//     or compromised peer that smuggles ESC/OSC through a validly-versioned Snap);
//   - item 2: clipping the snapshot to the live client terminal (a snapshot from a
//     larger terminal must not pile excess rows or wrap the bottom row);
//   - item 4: alt-screen continuity (enter the alternate buffer when the Snap
//     records the emulator there, so a later ?1049l from the live stream restores
//     the correct buffer).

import (
	"bytes"
	"strings"
	"testing"
)

// gridLines builds rows x cols of blank single-width cells, so a clipping test has
// a real grid to clip.
func gridLines(rows, cols int) []Line {
	lines := make([]Line, rows)
	for y := range lines {
		runs := make([]Run, cols)
		for x := range runs {
			runs[x] = Run{Text: " ", Width: 1}
		}
		lines[y] = Line{Runs: runs}
	}
	return lines
}

// item 1 — the renderer strips C0 control bytes (incl. the ESC that introduces any
// sequence) and DEL from run text before writing to the real terminal, so a Snap
// whose run text smuggles an OSC 52 clipboard-write (or a bare BEL) cannot inject it.
// Multi-byte UTF-8 is untouched.
func TestRenderSnapshot_StripsControlBytesFromRunText(t *testing.T) {
	s := &Snap{
		Version: SnapshotVersion, Cols: 4, Rows: 1, CursorVisible: true,
		Lines: []Line{{Runs: []Run{
			{Text: "\x1b]52;c;c2VjcmV0\x07", Width: 1}, // OSC 52 clipboard-write injection
			{Text: "\x07", Width: 1},                   // bare BEL
			{Text: "世", Width: 2},                      // multi-byte UTF-8 must survive
		}}},
	}
	out := render(s)

	// The renderer's own framing uses CSI (ESC-[), never OSC (ESC-]); any ESC-] would
	// be leaked run text.
	if strings.Contains(out, "\x1b]") {
		t.Fatalf("render must strip the ESC that introduces an OSC sequence from run text; got %q", out)
	}
	// The renderer never emits BEL, so any 0x07 came from run text.
	if strings.ContainsRune(out, '\x07') {
		t.Fatalf("render must strip BEL (0x07) from run text; got %q", out)
	}
	if strings.Count(out, "世") != 1 {
		t.Fatalf("multi-byte UTF-8 run text must survive sanitization intact; got %q", out)
	}
}

// item 2 — a snapshot taller than the client terminal skips the excess rows rather
// than piling them onto the bottom line.
func TestRenderSnapshotClipped_SkipsRowsBeyondClientHeight(t *testing.T) {
	s := &Snap{
		Version: SnapshotVersion, Cols: 2, Rows: 4, CursorVisible: true,
		Lines: []Line{
			{Runs: []Run{{Text: "a", Width: 1}, {Text: "a", Width: 1}}},
			{Runs: []Run{{Text: "b", Width: 1}, {Text: "b", Width: 1}}},
			{Runs: []Run{{Text: "c", Width: 1}, {Text: "c", Width: 1}}},
			{Runs: []Run{{Text: "d", Width: 1}, {Text: "d", Width: 1}}},
		},
	}
	out := string(RenderSnapshotClipped(s, 2, 2))

	if !strings.Contains(out, "\x1b[1;1H") || !strings.Contains(out, "\x1b[2;1H") {
		t.Fatalf("rows within the client height must be positioned; got %q", out)
	}
	if strings.Contains(out, "\x1b[3;1H") || strings.Contains(out, "\x1b[4;1H") {
		t.Fatalf("rows beyond the client height must be skipped; got %q", out)
	}
	if strings.Contains(out, "c") || strings.Contains(out, "d") {
		t.Fatalf("clipped rows' text must not render; got %q", out)
	}
}

// item 2 — a snapshot wider than the client terminal truncates each row at the client
// width, dropping the cells that would cross the edge (which would otherwise wrap).
func TestRenderSnapshotClipped_TruncatesRowByWidth(t *testing.T) {
	s := &Snap{
		Version: SnapshotVersion, Cols: 5, Rows: 1, CursorVisible: true,
		Lines: []Line{{Runs: []Run{
			{Text: "a", Width: 1}, {Text: "b", Width: 1}, {Text: "c", Width: 1},
			{Text: "d", Width: 1}, {Text: "e", Width: 1},
		}}},
	}
	out := string(RenderSnapshotClipped(s, 3, 1))

	if !strings.Contains(out, "abc") {
		t.Fatalf("cells within the client width must render; got %q", out)
	}
	if strings.Contains(out, "d") || strings.Contains(out, "e") {
		t.Fatalf("cells beyond the client width must be truncated; got %q", out)
	}
}

// item 2 — a wide (2-cell) grapheme that would straddle the client's right edge is
// dropped WHOLE, never split into a lone spacer cell.
func TestRenderSnapshotClipped_DropsWideCellStraddlingEdge(t *testing.T) {
	// client cols=3: a(1) b(1) fill to accumulated width 2; the wide cell needs 2 more
	// (2+2 > 3) so it must be dropped entirely.
	s := &Snap{
		Version: SnapshotVersion, Cols: 4, Rows: 1, CursorVisible: true,
		Lines: []Line{{Runs: []Run{
			{Text: "a", Width: 1}, {Text: "b", Width: 1}, {Text: "世", Width: 2},
		}}},
	}
	out := string(RenderSnapshotClipped(s, 3, 1))

	if !strings.Contains(out, "ab") {
		t.Fatalf("cells that fit within the client width must render; got %q", out)
	}
	if strings.Contains(out, "世") {
		t.Fatalf("a wide cell straddling the client edge must be dropped whole; got %q", out)
	}
}

// item 2 — the final cursor position is clamped into the clipped bounds so it never
// lands off the client's visible grid.
func TestRenderSnapshotClipped_ClampsCursorIntoBounds(t *testing.T) {
	s := &Snap{
		Version: SnapshotVersion, Cols: 80, Rows: 24, CursorX: 40, CursorY: 20, CursorVisible: true,
		Lines: gridLines(24, 80),
	}
	out := string(RenderSnapshotClipped(s, 10, 5))

	// Clamped to the last visible cell: row 5, col 10 (1-based CUP).
	if !strings.Contains(out, "\x1b[5;10H") {
		t.Fatalf("cursor must be clamped into the clipped bounds (row 5, col 10); got %q", out)
	}
}

// item 2 — 0,0 disables clipping: RenderSnapshotClipped(s, 0, 0) is byte-identical to
// the unclipped RenderSnapshot, so existing callers are unaffected.
func TestRenderSnapshotClipped_ZeroZeroMatchesUnclipped(t *testing.T) {
	s := &Snap{
		Version: SnapshotVersion, Cols: 3, Rows: 2, CursorX: 1, CursorY: 1, CursorVisible: true,
		Lines: []Line{
			{Runs: []Run{{Text: "a", Width: 1}, {Text: "b", Width: 1}, {Text: "c", Width: 1}}},
			{Runs: []Run{{Text: " ", Width: 1}, {Text: " ", Width: 1}, {Text: " ", Width: 1}}},
		},
	}
	if !bytes.Equal(RenderSnapshotClipped(s, 0, 0), RenderSnapshot(s)) {
		t.Fatalf("clipping disabled (0,0) must be byte-identical to the unclipped RenderSnapshot")
	}
}

// item 4 — a snapshot recorded in the alternate screen enters the alt buffer
// (CSI ?1049h) BEFORE the first cell write, so a later ?1049l from the live stream
// restores the correct (primary) buffer.
func TestRenderSnapshot_AltScreenEntersAltBuffer(t *testing.T) {
	s := &Snap{
		Version: SnapshotVersion, Cols: 1, Rows: 1, AltScreen: true, CursorVisible: true,
		Lines: []Line{{Runs: []Run{{Text: "x", Width: 1}}}},
	}
	out := render(s)

	i := strings.Index(out, "\x1b[?1049h")
	if i < 0 {
		t.Fatalf("an alt-screen snapshot must enter the alternate buffer (CSI ?1049h); got %q", out)
	}
	if j := strings.Index(out, "x"); j >= 0 && j < i {
		t.Fatalf("?1049h must precede the first cell write; got %q", out)
	}
}

// item 4 — a snapshot NOT in the alternate screen renders as before (no ?1049h).
func TestRenderSnapshot_NonAltScreenDoesNotEnterAltBuffer(t *testing.T) {
	s := &Snap{
		Version: SnapshotVersion, Cols: 1, Rows: 1, AltScreen: false, CursorVisible: true,
		Lines: []Line{{Runs: []Run{{Text: "x", Width: 1}}}},
	}
	if strings.Contains(render(s), "\x1b[?1049h") {
		t.Fatalf("a non-alt-screen snapshot must not enter the alternate buffer")
	}
}

// Fable re-confirm residual (agents-tracker-9p5): C1 control runes
// (U+0080-U+009F) are honored as controls by xterm-family terminals in UTF-8
// mode, so the render-time backstop must strip them like C0+DEL — otherwise a
// UTF-8-encoded CSI (U+009B) or OSC (U+009D) in a hostile snapshot reaches the
// real terminal.
func TestRenderSnapshot_StripsC1ControlRunes(t *testing.T) {
	s := &Snap{
		Version: SnapshotVersion, Cols: 8, Rows: 1, CursorVisible: true,
		Lines: []Line{{Runs: []Run{
			{Text: "a31mb", Width: 1},  // UTF-8 C1 CSI injection
			{Text: "52;cc", Width: 1}, // UTF-8 C1 OSC + ST
		}}},
	}
	out := render(s)

	for _, r := range []rune{0x9b, 0x9d, 0x9c} {
		if strings.ContainsRune(out, r) {
			t.Errorf("render must strip C1 control rune %U from run text (U+0080-U+009F); got %q", r, out)
		}
	}
	for _, want := range []string{"a", "b", "c"} {
		if !strings.Contains(out, want) {
			t.Errorf("printable %q lost by the C1 strip", want)
		}
	}
}
