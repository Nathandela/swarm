package vt

// render.go turns a decoded snapshot back into ANSI bytes that faithfully
// repaint a terminal. It is the consumer half of the Snap projection: the
// snapshot is a structured, escape-free description of the visible screen
// (emulator.go), and RenderSnapshot replays exactly that description as escape
// sequences — clear + home, each row's runs with their SGR styling, a trailing
// reset, cursor visibility, then the recorded cursor position. It invents
// nothing beyond what the Snap records.
//
// Scope (deliberate): the Snap also carries AltScreen and Title, which the
// renderer does NOT act on. It never enters/leaves the alternate screen and
// never sets the window title — the live PTY stream that follows the paint owns
// that terminal state, so switching modes here would fight it. The renderer only
// paints the visible grid content and places the cursor.

import (
	"strconv"
	"strings"
)

// RenderSnapshot converts a decoded snapshot into ANSI bytes that repaint the
// screen: reset SGR, clear the screen and home the cursor, write each line's
// runs (absolutely positioning every row so a repaint never depends on wrap or
// scroll) with per-run SGR, reset SGR after the grid, apply cursor visibility,
// then place the cursor at the snapshot's recorded position. A nil snapshot
// renders to nothing.
func RenderSnapshot(s *Snap) []byte {
	if s == nil {
		return nil
	}
	var b strings.Builder
	// Reset any inherited SGR BEFORE clearing so the cleared cells take the
	// default background (terminals with background-color-erase fill the screen
	// with the current SGR background otherwise), then home the cursor.
	b.WriteString("\x1b[0m\x1b[2J\x1b[H")

	// last is the most recently emitted SGR; identical consecutive runs (e.g. a
	// row of default-styled blanks) then reuse it instead of re-emitting.
	last := ""
	for y, line := range s.Lines {
		// Absolute 1-based CUP for each row. Positioning the next row also
		// resolves any pending-wrap from the previous row's final cell, so
		// writing the bottom-right cell never scrolls the screen.
		b.WriteString("\x1b[")
		b.WriteString(strconv.Itoa(y + 1))
		b.WriteString(";1H")
		for _, r := range line.Runs {
			sgr := runSGR(r)
			if sgr != last {
				b.WriteString(sgr)
				last = sgr
			}
			b.WriteString(r.Text)
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
	// Final cursor position. CUP is 1-based; snapshot coordinates are 0-based.
	b.WriteString("\x1b[")
	b.WriteString(strconv.Itoa(s.CursorY + 1))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(s.CursorX + 1))
	b.WriteByte('H')

	return []byte(b.String())
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
