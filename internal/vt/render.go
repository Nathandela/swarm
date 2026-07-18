package vt

// render.go turns a decoded snapshot back into ANSI bytes that faithfully
// repaint a terminal. It is the consumer half of the Snap projection: the
// snapshot is a structured, escape-free description of the visible screen
// (emulator.go), and RenderSnapshot replays exactly that description as escape
// sequences — optional alt-screen entry, clear + home, each row's runs with their
// SGR styling, a trailing reset, cursor visibility, then the recorded cursor
// position. It invents nothing beyond what the Snap records.
//
// Scope (deliberate):
//   - AltScreen IS acted on: when the Snap records the emulator in the alternate
//     screen, the preamble enters it (CSI ?1049h) so a later ?1049l from the live
//     PTY stream restores the correct (primary) buffer instead of leaving a stale
//     alt buffer on screen. A non-alt snapshot never touches the mode.
//   - Title is NOT acted on: the live PTY stream that follows the paint owns the
//     window title, so setting it here would fight that stream.
//   - SGR pen state is NOT restored: the Snap records per-cell style but not the
//     terminal's active pen, so the renderer cannot re-assert it — apps re-assert
//     their SGR when they next draw, and the trailing reset leaves a clean pen.
//   - Run text is sanitized at render time: even a validly-versioned but skewed or
//     compromised peer cannot inject ESC/OSC (e.g. an OSC 52 clipboard write)
//     because every C0/C1 control byte and DEL is replaced with a space in run
//     text before it is written (see stripControls) — replaced, not deleted, so a
//     run's written character count keeps pace with its declared Width. This is
//     the render-time backstop to the producer-side N-6 filter in emulator.go.

import (
	"strconv"
	"strings"
)

// RenderSnapshot converts a decoded snapshot into ANSI bytes that repaint the
// screen without clipping to any client size. It is RenderSnapshotClipped with
// clipping disabled (0, 0); see there for the full contract. A nil snapshot
// renders to nothing.
func RenderSnapshot(s *Snap) []byte {
	return RenderSnapshotClipped(s, 0, 0)
}

// RenderSnapshotClipped is RenderSnapshot clipped to a live terminal of cols x rows
// cells. A snapshot captured on a terminal larger than the attaching client would
// otherwise pile the excess rows onto the bottom line, and a wider row would wrap —
// a wrap on the bottom row scrolls the screen. Clipping to the client bounds keeps
// the repaint inside the visible grid:
//   - rows beyond the client height are skipped;
//   - each row is truncated once its accumulated Run.Width would cross the client
//     width — a wide (2-cell) grapheme straddling the edge is dropped whole, never
//     split into a lone spacer;
//   - the final cursor is clamped into the clipped bounds.
//
// cols<=0 or rows<=0 disables clipping on that axis; (0, 0) is exactly the unclipped
// behavior RenderSnapshot exposes (byte-identical). It writes: optional alt-screen
// entry, reset SGR, clear+home, each surviving row absolutely positioned with per-run
// SGR, a trailing reset, cursor visibility, then the clamped cursor position.
func RenderSnapshotClipped(s *Snap, cols, rows int) []byte {
	if s == nil {
		return nil
	}
	var b strings.Builder
	// Enter the alternate screen first when the snapshot recorded it, so a later
	// ?1049l from the live stream restores the correct buffer (item 4).
	if s.AltScreen {
		b.WriteString("\x1b[?1049h")
	}
	// Reset any inherited SGR BEFORE clearing so the cleared cells take the
	// default background (terminals with background-color-erase fill the screen
	// with the current SGR background otherwise), then home the cursor.
	b.WriteString("\x1b[0m\x1b[2J\x1b[H")

	// last is the most recently emitted SGR; identical consecutive runs (e.g. a
	// row of default-styled blanks) then reuse it instead of re-emitting.
	last := ""
	for y, line := range s.Lines {
		if rows > 0 && y >= rows {
			break // clip: rows beyond the client height are skipped
		}
		// Absolute 1-based CUP for each row. Positioning the next row also
		// resolves any pending-wrap from the previous row's final cell, so
		// writing the bottom-right cell never scrolls the screen.
		b.WriteString("\x1b[")
		b.WriteString(strconv.Itoa(y + 1))
		b.WriteString(";1H")
		acc := 0
		for _, r := range line.Runs {
			if cols > 0 && acc+r.Width > cols {
				break // clip: this run (and the rest) would cross the client edge
			}
			sgr := runSGR(r)
			if sgr != last {
				b.WriteString(sgr)
				last = sgr
			}
			b.WriteString(stripControls(r.Text))
			acc += r.Width
		}
	}
	// Reset styling after the grid so the cursor and any later output are clean.
	b.WriteString("\x1b[0m")

	// Cursor visibility (DECTCEM) exactly as the snapshot recorded it.
	if s.CursorVisible {
		b.WriteString("\x1b[?25h")
	} else {
		b.WriteString("\x1b[?25l")
	}
	// Final cursor position, clamped into the clipped bounds. CUP is 1-based;
	// snapshot coordinates are 0-based.
	b.WriteString("\x1b[")
	b.WriteString(strconv.Itoa(clampCursor(s.CursorY, rows) + 1))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(clampCursor(s.CursorX, cols) + 1))
	b.WriteByte('H')

	return []byte(b.String())
}

// clampCursor bounds a 0-based cursor coordinate into the clipped grid. limit is the
// client dimension on that axis; limit<=0 disables clipping and returns v unchanged
// so the unclipped path is byte-identical to the legacy renderer. Otherwise the
// result is in [0, limit-1].
func clampCursor(v, limit int) int {
	if limit <= 0 {
		return v
	}
	if v >= limit {
		v = limit - 1
	}
	if v < 0 {
		v = 0
	}
	return v
}

// stripControls REPLACES C0 control runes (0x00-0x1f, including the ESC that
// introduces any sequence), DEL (0x7f), and the C1 control range (U+0080-U+009F,
// whose UTF-8-encoded CSI/OSC forms xterm-family terminals honor as controls)
// with an ASCII space, keeping every other rune (space included) unchanged. It is
// the render-time N-6 backstop: a skewed or compromised peer cannot smuggle
// ESC/OSC (e.g. an OSC 52 clipboard write) through a validly-versioned snapshot,
// because the control bytes never reach the real terminal. Clean single-grapheme
// run text (the overwhelming common case) passes through unchanged.
//
// A rune is substituted rather than deleted (agents-tracker-rs8) so the run's
// rendered character count keeps pace with its declared Run.Width: the renderer
// positions only the start of each row (one absolute CUP) and relies on the
// terminal's own cursor auto-advance for every run after that, so dropping a
// rune would shift every following run on the row one column left of where
// Run.Width says it belongs. This is not a general width-accounting fix: a
// space is always one column wide, so a control rune that a hostile/producer-
// bypassed snapshot claimed as part of a wider (e.g. combining or wide-grapheme)
// cluster can still leave that run's total column count short of its Run.Width —
// a pre-existing hostile-input edge case, noted here rather than solved.
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return ' '
		}
		return r
	}, s)
}

// runSGR builds the SGR sequence for one run's style. It always starts from a
// reset ("0") so a run never inherits a neighbor's attributes, then appends only
// the attributes the run carries. A style-less run yields a bare "\x1b[0m".
func runSGR(r Run) string {
	var p strings.Builder
	p.WriteString("\x1b[0") // reset baseline
	if r.Bold {
		p.WriteString(";1")
	}
	if r.Faint {
		p.WriteString(";2")
	}
	if r.Italic {
		p.WriteString(";3")
	}
	if r.Underline {
		p.WriteString(";4")
	}
	if r.Reverse {
		p.WriteString(";7")
	}
	writeColor(&p, "38", r.Fg)
	writeColor(&p, "48", r.Bg)
	p.WriteByte('m')
	return p.String()
}

// writeColor appends a truecolor SGR fragment (";38;2;r;g;b" for fg, ";48;..."
// for bg) for a "#rrggbb" spec — the form colorSpec emits. An empty or malformed
// spec (the terminal default, or anything unparseable) appends nothing, so a bad
// color simply yields no color rather than corrupt output.
func writeColor(p *strings.Builder, sel, spec string) {
	if len(spec) != 7 || spec[0] != '#' {
		return
	}
	v, err := strconv.ParseUint(spec[1:], 16, 32)
	if err != nil {
		return
	}
	p.WriteByte(';')
	p.WriteString(sel)
	p.WriteString(";2;")
	p.WriteString(strconv.Itoa(int(v >> 16 & 0xff)))
	p.WriteByte(';')
	p.WriteString(strconv.Itoa(int(v >> 8 & 0xff)))
	p.WriteByte(';')
	p.WriteString(strconv.Itoa(int(v & 0xff)))
}
