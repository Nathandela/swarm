package vt

// render.go turns a decoded snapshot back into ANSI bytes that faithfully
// repaint a terminal. It is the consumer half of the Snap projection: the
// snapshot is a structured, escape-free description of the visible screen
// (emulator.go), and RenderSnapshotClipped replays exactly that description as
// escape sequences — optional alt-screen entry, clear + home, each row's runs
// with their SGR styling, a trailing reset, cursor visibility, then the
// recorded cursor position. It invents nothing beyond what the Snap records.
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
//     stripControls also DROPS Unicode bidi formatting/override/isolate and
//     zero-width characters (F7, Trojan-Source): those runes have zero display
//     width, so dropping them is the parity-preserving move, where a space would
//     add a column the emulator never counted.

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// RenderSnapshotClipped converts a decoded snapshot into ANSI bytes that repaint
// the screen, clipped to a live terminal of cols x rows cells. A snapshot
// captured on a terminal larger than the attaching client would otherwise pile
// the excess rows onto the bottom line, and a wider row would wrap — a wrap on
// the bottom row scrolls the screen. Clipping to the client bounds keeps the
// repaint inside the visible grid:
//   - rows beyond the client height are skipped;
//   - a row is truncated at the client width. A run that fits whole is emitted
//     whole; a run that would cross the edge is clipped INTRA-run to the longest
//     grapheme-cluster prefix that still fits (runs are style-merged spans since
//     item 4.3, so a straddling run may be many cells wide). A wide (2-cell)
//     grapheme straddling the edge is dropped whole, never split into a lone
//     spacer;
//   - the final cursor is clamped into the clipped bounds.
//
// cols<=0 or rows<=0 disables clipping on that axis; (0, 0) renders unclipped. A
// nil snapshot renders to nothing. It writes: optional alt-screen entry, reset
// SGR, clear+home, each surviving row absolutely positioned with per-run SGR, a
// trailing reset, cursor visibility, then the clamped cursor position.
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
				// This run straddles the client edge. Emit the fitting prefix
				// (grapheme-aware) instead of dropping the whole run, then stop:
				// nothing after a straddling run can fit. A prefix that fits zero
				// cells (e.g. a wide grapheme with one column of room) emits
				// nothing, matching the old whole-run-drop at that boundary.
				if prefix, w := clipRunPrefix(r.Text, acc, cols); prefix != "" {
					sgr := runSGR(r)
					if sgr != last {
						b.WriteString(sgr)
						last = sgr
					}
					b.WriteString(stripControls(prefix))
					acc += w
				}
				break
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

// SnapText flattens a snapshot grid into plain-text lines, one string per grid
// row, safe to display on a phone: no terminal control sequence can escape, and no
// Unicode bidi/zero-width rune can visually spoof what is displayed. Unlike
// RenderSnapshot it emits NO ANSI — just each row's run text concatenated with every
// control byte, bidi-formatting/override/isolate rune, and zero-width rune removed
// (stripControls: C0 incl. LF/CR, DEL, C1, bidi, zero-width). It is a sanitization
// choke point in its own right, stripping directly rather than trusting the
// producer-side N-6 filter, so hostile bytes that reach a Snap by any path still
// cannot smuggle an escape sequence or a Trojan-Source visual spoof to the viewer
// (F7). A nil snapshot yields nil.
func SnapText(s *Snap) []string {
	if s == nil {
		return nil
	}
	lines := make([]string, len(s.Lines))
	for y, line := range s.Lines {
		var b strings.Builder
		for _, r := range line.Runs {
			// stripControls drops every C0 (incl. LF/CR/ESC), DEL, and C1 byte, so
			// the concatenation can never contain a control byte or an embedded
			// newline — the row is a single flat, escape-free line.
			b.WriteString(stripControls(r.Text))
		}
		lines[y] = b.String()
	}
	return lines
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

// clipRunPrefix returns the longest grapheme-cluster prefix of a straddling
// merged run whose display width, added to acc (the row width already emitted),
// stays within cols, together with that prefix's width. A cluster that would
// cross cols stops the walk, so a wide grapheme straddling the edge is dropped
// whole — matching the per-cell clip behavior of the pre-merge renderer.
//
// Width authority: it walks with ansi.FirstGraphemeCluster under
// ansi.GraphemeWidth, the SAME segmentation + width the in-shim emulator used to
// assign each cell's Width (charm x/vt utf8.go flushGrapheme). Because the merged
// run's Width is those per-cell widths summed, re-walking the concatenated text
// reproduces the identical cell boundaries and widths, so clipping a merged run
// yields byte-identical output to clipping the equivalent one-run-per-cell row.
//
// Strip interaction: the walk runs on the RAW run text (as the snapshot carries
// it), and stripControls is applied by the caller to the RESULTING prefix, not
// before the walk. stripControls replaces a control rune with a space, and a
// space is a grapheme-cluster boundary — stripping first could re-segment the
// text and desync the walk's widths from the declared Run.Width. On a well-
// behaved snapshot (producer-sanitized, no control runes in run text) the two
// orders are identical; walking raw keeps column parity in the hostile/skewed
// case too, which is exactly the pre-existing edge stripControls documents.
func clipRunPrefix(text string, acc, cols int) (string, int) {
	var w int
	rest := text
	for len(rest) > 0 {
		cluster, cw := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if acc+w+cw > cols {
			break
		}
		w += cw
		rest = rest[len(cluster):]
	}
	return text[:len(text)-len(rest)], w
}

// stripControls sanitizes run text before it is written to a real terminal, as
// the render-time N-6 backstop to the producer-side filter in emulator.go: a
// skewed or compromised peer cannot smuggle ESC/OSC (e.g. an OSC 52 clipboard
// write) through a validly-versioned snapshot. Two rune classes, two treatments,
// and the difference is COLUMN PARITY:
//
//   - C0 controls (0x00-0x1f, including ESC), DEL (0x7f) and the C1 range
//     (U+0080-U+009F, whose UTF-8 CSI/OSC forms xterm-family terminals honor)
//     are REPLACED WITH A SPACE, not deleted, so a run's written character count
//     keeps pace with its declared Width -- the merged-run renderer
//     (clipRunPrefix) depends on that pacing.
//   - Unicode bidi formatting/override/isolate runes (U+061C, U+200E/U+200F,
//     U+202A-U+202E, U+2066-U+2069) and zero-width runes (U+200B-U+200D,
//     U+FEFF) are DROPPED (F7): without this a hostile PTY could emit a
//     Trojan-Source visual spoof (U+202E reordering displayed text, zero-width
//     runes hiding or splicing content) that no control-byte filter catches.
//     These runes have zero display width, so dropping them PRESERVES parity,
//     where a space would add a column the emulator never counted.
//
// Clean single-grapheme run text (the overwhelming common case) passes through
// unchanged. Union of the two lines' filters, reconciled in the 2026-08-02 merge.
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return ' ' // C0 / DEL / C1: replaced, never deleted (column parity)
		case r == 0x061c, r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e,
			r >= 0x2066 && r <= 0x2069, r == 0xfeff:
			return -1 // bidi + zero-width (F7): zero display width, drop keeps parity
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
