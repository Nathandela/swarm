package vt

// Failing-first suite for the snapshot-to-ANSI renderer (RenderSnapshot).
//
// The renderer is the P0 fix for agents-tracker-a6f: attaching painted the raw
// snapshot JSON to the terminal because there was no snapshot->ANSI path. These
// tests pin a faithful repaint — reset+clear+home, per-row absolute positioning,
// per-run SGR styling, a trailing reset, cursor visibility, and a final cursor
// placement — with no snapshot JSON leaking into the ANSI stream.

import (
	"strings"
	"testing"
)

func render(s *Snap) string { return string(RenderSnapshot(s)) }

// A snapshot renders to a full repaint: reset+clear+home first, the grid text
// with each row absolutely positioned, a final cursor placement from the
// snapshot's cursor state, the visible-cursor sequence, and never any JSON.
func TestRenderSnapshot_RepaintsGridWithClearHomeAndCursor(t *testing.T) {
	s := &Snap{
		Version:       SnapshotVersion,
		Cols:          3,
		Rows:          2,
		CursorX:       1,
		CursorY:       1,
		CursorVisible: true,
		Lines: []Line{
			{Runs: []Run{{Text: "a", Width: 1}, {Text: "b", Width: 1}, {Text: "c", Width: 1}}},
			{Runs: []Run{{Text: " ", Width: 1}, {Text: " ", Width: 1}, {Text: " ", Width: 1}}},
		},
	}
	out := render(s)

	if !strings.HasPrefix(out, "\x1b[0m\x1b[2J\x1b[H") {
		t.Fatalf("must start with reset+clear+home; got %q", out)
	}
	if !strings.Contains(out, "abc") {
		t.Fatalf("grid text must render contiguously; got %q", out)
	}
	if !strings.Contains(out, "\x1b[1;1H") || !strings.Contains(out, "\x1b[2;1H") {
		t.Fatalf("each row must be positioned with 1-based CUP; got %q", out)
	}
	if !strings.Contains(out, "\x1b[2;2H") {
		t.Fatalf("final cursor must land at snapshot cursor (row 2 col 2); got %q", out)
	}
	if !strings.Contains(out, "\x1b[?25h") {
		t.Fatalf("a visible cursor must be shown; got %q", out)
	}
	if strings.Contains(out, `{"runs":`) || strings.Contains(out, `"version":`) {
		t.Fatalf("rendered ANSI must never contain snapshot JSON; got %q", out)
	}
}

// A styled run emits a self-contained SGR (reset baseline then only its own
// attributes), and styling is reset after the grid.
func TestRenderSnapshot_StyledRunEmitsSGR(t *testing.T) {
	s := &Snap{
		Version:       SnapshotVersion,
		Cols:          2,
		Rows:          1,
		CursorVisible: true,
		Lines: []Line{{Runs: []Run{
			{Text: "X", Width: 1, Bold: true, Fg: "#ff0000"},
			{Text: "Y", Width: 1, Underline: true, Reverse: true, Bg: "#0000ff"},
		}}},
	}
	out := render(s)

	if !strings.Contains(out, "\x1b[0;1;38;2;255;0;0mX") {
		t.Fatalf("bold red run SGR missing; got %q", out)
	}
	if !strings.Contains(out, "\x1b[0;4;7;48;2;0;0;255mY") {
		t.Fatalf("underline+reverse blue-bg run SGR missing; got %q", out)
	}
	if !strings.Contains(out, "\x1b[0m") {
		t.Fatalf("a trailing SGR reset must follow the grid; got %q", out)
	}
}

// A hidden cursor (DECTCEM reset recorded in the snapshot) emits the hide
// sequence and never the show sequence.
func TestRenderSnapshot_HiddenCursorEmitsHideSequence(t *testing.T) {
	s := &Snap{
		Version:       SnapshotVersion,
		Cols:          1,
		Rows:          1,
		CursorVisible: false,
		Lines:         []Line{{Runs: []Run{{Text: " ", Width: 1}}}},
	}
	out := render(s)

	if !strings.Contains(out, "\x1b[?25l") {
		t.Fatalf("a hidden cursor must emit the DECTCEM hide sequence; got %q", out)
	}
	if strings.Contains(out, "\x1b[?25h") {
		t.Fatalf("a hidden cursor must not also show the cursor; got %q", out)
	}
}

// A double-width grapheme is written exactly once (its Width, not its Text,
// accounts for the spacer cell) and is contiguous with the next cell.
func TestRenderSnapshot_WideGraphemeRenderedOnce(t *testing.T) {
	s := &Snap{
		Version:       SnapshotVersion,
		Cols:          3,
		Rows:          1,
		CursorVisible: true,
		Lines:         []Line{{Runs: []Run{{Text: "世", Width: 2}, {Text: "x", Width: 1}}}},
	}
	out := render(s)

	if n := strings.Count(out, "世"); n != 1 {
		t.Fatalf("a wide grapheme must be written exactly once, got %d; %q", n, out)
	}
	if !strings.Contains(out, "世x") {
		t.Fatalf("a wide grapheme and its neighbor must be contiguous; got %q", out)
	}
}

// A nil snapshot renders to nothing (defensive: the caller skips a failed decode,
// but the renderer never panics on a nil).
func TestRenderSnapshot_NilIsEmpty(t *testing.T) {
	if b := RenderSnapshot(nil); len(b) != 0 {
		t.Fatalf("nil snapshot must render to nothing; got %q", b)
	}
}
