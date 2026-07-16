package vt_test

// E2.1 VALIDATION FIXTURE (ADR-005).
//
// This is the risk-gate test for Epic 2: it proves the chosen VT-emulation
// library, github.com/charmbracelet/x/vt, correctly emulates an alternate-
// screen TUI grid. It is the seed of the Epic 2 test suite and must keep
// passing on any bump of the pinned x/vt version. There is deliberately no
// production Emulator wrapper here yet; that API is a later Epic 2 task.
//
// Two fixtures, both asserting the frozen Epic 2 needs (primary+alternate
// buffers, cell content, SGR attrs, cursor, modes, resize-capable API):
//
//  1. A scripted byte stream that enters the alternate screen (CSI ?1049h),
//     draws bold-red text at a known position, parks the cursor, then leaves
//     the alt screen (CSI ?1049l). Run whole AND one byte at a time to prove
//     escape sequences are buffered correctly when split across Feed calls.
//  2. A real capture of `vim -u NONE` painting a 3-line file in an 80x24 PTY
//     (testdata/vim_altscreen.raw): feeding up to vim's rmcup must render the
//     file on the alt screen; feeding the remainder must restore the primary.

import (
	"bytes"
	"os"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	vt "github.com/charmbracelet/x/vt"
)

const (
	rows = 24
	cols = 80
)

// buildFixture: clear+write primary, enter alt, draw bold-red "ALT-RED" at
// row3/col5 (1-based), park cursor at row7/col12 (1-based).
func buildFixture() []byte {
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H")            // clear primary, cursor home
	b.WriteString("PRIMARY-HOME")             // primary content at row0 col0
	b.WriteString("\x1b[?1049h")              // enter alternate screen
	b.WriteString("\x1b[2J\x1b[H")            // clear alt, cursor home
	b.WriteString("\x1b[3;5H")                // move to row3 col5 (1-based)
	b.WriteString("\x1b[1;31mALT-RED\x1b[0m") // bold + red foreground
	b.WriteString("\x1b[7;12H")               // final cursor: row7 col12
	return []byte(b.String())
}

var exitAlt = []byte("\x1b[?1049l") // leave alt screen, restore primary

// Expected 0-based coordinates derived from the fixture.
const (
	altTextRow = 2  // row3, 1-based
	altTextCol = 4  // col5, 1-based
	curRow     = 6  // row7, 1-based
	curCol     = 11 // col12, 1-based
	altText    = "ALT-RED"
)

func feedWhole(e *vt.Emulator, data []byte) {
	_, _ = e.Write(data)
}

func feedByteByByte(e *vt.Emulator, data []byte) {
	for i := 0; i < len(data); i++ {
		_, _ = e.Write(data[i : i+1])
	}
}

// row reads a full grid row as text, trailing blanks trimmed.
func row(e *vt.Emulator, r, width int) string {
	var b strings.Builder
	for x := 0; x < width; x++ {
		c := e.CellAt(x, r)
		if c == nil || c.Content == "" {
			b.WriteString(" ")
			continue
		}
		b.WriteString(c.Content)
	}
	return strings.TrimRight(b.String(), " ")
}

func runScripted(t *testing.T, split bool) {
	t.Helper()
	e := vt.NewEmulator(cols, rows)
	fix := buildFixture()
	if split {
		feedByteByByte(e, fix)
	} else {
		feedWhole(e, fix)
	}

	// While the alternate screen is active.
	if !e.IsAltScreen() {
		t.Errorf("alt screen not active after CSI ?1049h")
	}
	if got := row(e, altTextRow, cols); !strings.Contains(got, altText) {
		t.Errorf("alt text missing: row %d = %q, want contains %q", altTextRow, got, altText)
	}
	if got := row(e, 0, cols); strings.Contains(got, "PRIMARY") {
		t.Errorf("primary content leaked onto active alt screen: row0 = %q", got)
	}
	if pos := e.CursorPosition(); pos.X != curCol || pos.Y != curRow {
		t.Errorf("cursor = (%d,%d), want (%d,%d)", pos.X, pos.Y, curCol, curRow)
	}
	cell := e.CellAt(altTextCol, altTextRow)
	if cell == nil {
		t.Fatalf("alt 'A' cell is nil")
	}
	if cell.Content != "A" {
		t.Errorf("alt cell content = %q, want %q", cell.Content, "A")
	}
	if cell.Style.Fg == nil {
		t.Errorf("alt 'A' cell has no foreground color (want red)")
	}
	if cell.Style.Attrs&uv.AttrBold == 0 {
		t.Errorf("alt 'A' cell not bold (attrs=%d)", cell.Style.Attrs)
	}

	// After leaving the alternate screen: primary must be restored.
	feedWhole(e, exitAlt)
	if e.IsAltScreen() {
		t.Errorf("still on alt screen after CSI ?1049l")
	}
	if got := row(e, 0, cols); !strings.Contains(got, "PRIMARY-HOME") {
		t.Errorf("primary not restored after alt exit: row0 = %q", got)
	}
}

func TestAltScreenScripted_Whole(t *testing.T) { runScripted(t, false) }
func TestAltScreenScripted_Split(t *testing.T) { runScripted(t, true) }

// TestAltScreenVimCapture drives a real vim alt-screen paint through the
// emulator (the E2.5 "real alt-screen TUI capture" seed).
func TestAltScreenVimCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/vim_altscreen.raw")
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	i := bytes.LastIndex(data, exitAlt)
	if i < 0 {
		t.Fatalf("capture has no alt-screen exit sequence")
	}
	before, after := data[:i], data[i:]

	e := vt.NewEmulator(cols, rows)
	feedWhole(e, before)

	if !e.IsAltScreen() {
		t.Errorf("not on alt screen after vim paint")
	}
	var grid strings.Builder
	for y := 0; y < rows; y++ {
		grid.WriteString(row(e, y, cols))
		grid.WriteByte('\n')
	}
	screen := grid.String()
	for _, w := range []string{"ALPHA line one", "BETA line two", "GAMMA line three"} {
		if !strings.Contains(screen, w) {
			t.Errorf("alt screen missing %q\n---grid---\n%s", w, screen)
		}
	}
	if !strings.Contains(screen, "~") {
		t.Errorf("vim empty-line ~ markers missing from alt screen")
	}

	feedWhole(e, after)
	if e.IsAltScreen() {
		t.Errorf("still on alt screen after vim rmcup")
	}
}
