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
	"unicode"
	"unicode/utf8"

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
	// ADR-017 T4/T5-a: the DECLARED geometry is what the phone allocates against, and a
	// Snap's content is not otherwise bound to it. A producer that declares Rows=24 and
	// ships 50 000 lines, or declares 80 columns and ships one 4 MB run, is an unbounded
	// allocation on the handset spent from the shared 8-appends/s budget. Neither is a
	// spoof; both are a denial of service, and the choke point is here because SnapText is
	// where the phone's copy is made.
	rows := snapRowCountCap(s.Rows, len(s.Lines))
	maxRow := snapRowByteCap(s.Cols)
	lines := make([]string, rows)
	for y := 0; y < rows; y++ {
		var b strings.Builder
		for _, r := range s.Lines[y].Runs {
			if b.Len() >= maxRow {
				break
			}
			// stripControls drops every C0 (incl. LF/CR/ESC), DEL, and C1 byte, so
			// the concatenation can never contain a control byte or an embedded
			// newline — the row is a single flat, escape-free line.
			b.WriteString(stripControls(r.Text))
		}
		lines[y] = clampRuneBytes(clampCombiningDepth(b.String()), maxRow)
	}
	return lines
}

// snapRowCountCap is the row-count ceiling, and it applies to an UNDECLARED geometry too
// (round-2 minor 12). The clause used to read `if s.Rows > 0 && have > s.Rows`, so a Snap
// declaring Rows=0 with 50 000 lines emitted all 50 000 -- which contradicts the rule
// snapRowByteCap states nineteen lines below for the identical undeclared case, that
// "undeclared" must not read as "unlimited". It is reachable from renderInitialView, which
// decodes ANOTHER PROCESS's initial snapshot.
func snapRowCountCap(declared, have int) int {
	ceiling := snapDefaultRowCeiling
	if declared > 0 && declared < ceiling {
		ceiling = declared
	}
	if have > ceiling {
		return ceiling
	}
	return have
}

// snapDefaultRowCeiling bounds a grid whose Snap declares no (or an absurd) row count. 1000
// rows is far above any real terminal, and far below an allocation denial of service on a
// handset.
const snapDefaultRowCeiling = 1000

// snapRowByteCap is the byte ceiling one phone-displayed row may occupy under the Snap's
// DECLARED column count: SnapshotTextMax is the producer's own per-cell cap, so cols cells
// is the row's ceiling. A Snap that declares no geometry at all still gets a ceiling --
// "undeclared" must not read as "unlimited", for the same reason a zero profile bound does
// not (ADR-017 T5-a).
func snapRowByteCap(cols int) int {
	if cols <= 0 {
		cols = snapDefaultColCeiling
	}
	if cols > snapDefaultColCeiling {
		cols = snapDefaultColCeiling
	}
	return SnapshotTextMax * cols
}

// snapDefaultColCeiling bounds a row whose Snap declares no (or an absurd) column count.
// 1000 columns is far above any real terminal and far below a layout denial of service.
const snapDefaultColCeiling = 1000

// snapMaxCombiningDepth is the per-cell combining-mark clamp (ADR-017 T4-c). The "Zalgo"
// vector is a single base character carrying hundreds of combining marks: on a phone's
// text stack that renders as a vertical smear overdrawing the rows above and below it,
// and no per-rune filter sees it because every rune involved is individually legitimate.
//
// Eight is chosen well under UAX #15's stream-safe bound of 30 for a defective combining
// sequence; a real terminal cell carries a handful.
const snapMaxCombiningDepth = 8

// clampCombiningDepth truncates each run of combining marks to snapMaxCombiningDepth,
// keeping a PREFIX of the marks supplied — nothing reordered, nothing invented — and
// leaving the base character and everything after the cluster intact.
func clampCombiningDepth(s string) string {
	// Fast path: the overwhelming common case carries no combining mark at all.
	var deep bool
	var run int
	for _, r := range s {
		if isCombiningMark(r) {
			run++
			if run > snapMaxCombiningDepth {
				deep = true
				break
			}
			continue
		}
		run = 0
	}
	if !deep {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	run = 0
	for _, r := range s {
		if isCombiningMark(r) {
			run++
			if run > snapMaxCombiningDepth {
				continue
			}
			b.WriteRune(r)
			continue
		}
		run = 0
		b.WriteRune(r)
	}
	return b.String()
}

// isCombiningMark reports whether r is a Unicode mark (Mn/Mc/Me) — the runes that attach
// to the preceding base character rather than occupying a cell of their own.
func isCombiningMark(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r)
}

// clampRuneBytes truncates s to at most max bytes WITHOUT splitting a rune, so a clamped
// row is still valid UTF-8. A row that is already inside the ceiling is returned unchanged.
func clampRuneBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := 0
	for i := range s {
		if i > max {
			break
		}
		cut = i
	}
	return s[:cut]
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
//   - Unicode runes of ZERO DISPLAY WIDTH are DROPPED (F7, widened by ADR-017
//     T4-c): bidi formatting/override/isolate (U+061C, U+200E/U+200F,
//     U+202A-U+202E, U+2066-U+2069), zero-width (U+200B-U+200D, U+FEFF), and the
//     classes T4-c adds — see zeroWidthFormatRune below. Without this a hostile
//     PTY could emit a Trojan-Source visual spoof (U+202E reordering displayed
//     text, zero-width runes hiding or splicing content) that no control-byte
//     filter catches. These runes have zero display width, so dropping them
//     PRESERVES parity, where a space would add a column the emulator never
//     counted.
//
// INVALID UTF-8 BECOMES AN EXPLICIT U+FFFD (playbook:457-458). This is written as
// a behavior rather than left as a side effect of rune iteration: a byte that is
// dropped silently shortens a row, and a byte that survives hands the phone's own
// decoder the malformed input the machine was supposed to absorb.
//
// Clean single-grapheme run text (the overwhelming common case) passes through
// unchanged. Union of the two lines' filters, reconciled in the 2026-08-02 merge.
func stripControls(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			// An invalid byte, decoded one byte at a time. EXPLICITLY replaced, so a
			// four-byte truncated sequence yields the same number of glyphs a decoder
			// downstream would have produced, rather than vanishing.
			b.WriteRune(utf8.RuneError)
			continue
		}
		switch {
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			b.WriteByte(' ') // C0 / DEL / C1: replaced, never deleted (column parity)
		case invisibleCellRune(r):
			// REPLACED: invisible on the phone, but the TERMINAL gave it a cell, so
			// dropping would shift every column after it left against a grid that did not.
			b.WriteByte(' ')
		case zeroWidthFormatRune(r):
			// dropped: zero display width, so dropping keeps column parity
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// zeroWidthFormatRune is the DROP class: runes the emulator assigns no cell to, so a
// space in their place would add a column nothing counted. Each range below is a named
// spoof with a payoff on a PHONE, which is a different renderer from a terminal:
//
//   - U+00AD (SOFT HYPHEN) and U+180E render as nothing and split a word:
//     `pay<SHY>pal.com` reads as paypal.com while comparing unequal to it.
//   - U+061C, U+200E/U+200F, U+202A-U+202E, U+2066-U+2069 are the bidi
//     formatting/override/isolate runes of Trojan Source (F7).
//   - U+200B-U+200D and U+FEFF are the classic zero-width splice runes (F7).
//   - U+2028/U+2029 are laid out as LINE BREAKS by Android's text stack. One grid row
//     becomes two, every row after it shifts, and the phone's chrome then reads a row of
//     the terminal as a row of something else — with no control byte in the payload.
//     They are in this class and not the replace class because the emulator assigns them
//     no cell either: `above<U+2028>below` renders ten columns, not eleven.
//   - U+2060-U+2064 (WORD JOINER and the invisible operators) splice or hide content
//     exactly as U+200B does, and are outside the U+200B-U+200F range F7 covered.
//   - U+FFF9-U+FFFB (interlinear annotation) let one visible string carry a second,
//     hidden one.
//   - U+E0000-U+E007F (TAG) is a complete invisible ASCII alphabet: an entire second
//     sentence can ride inside a line that displays as innocuous.
//
// IT IS A PROPERTY AND NOT A LIST, and that is a round-3 repair rather than a style
// preference. The enumeration this replaced was reviewed twice and still leaked, measurably:
// U+206A-U+206F (INHIBIT SYMMETRIC SWAPPING and the deprecated format block, ONE CODE POINT
// past the hand-written U+2060-U+2064 arm), U+1D173-U+1D17A (the musical format controls) and
// the four Hangul fillers all survived it, each splicing an invisible payload into a rendered
// row with no control byte present. A hand list makes the DEFAULT ALLOW, so the next block
// Unicode adds -- or the next one a reviewer does not think of -- is through; this wave's rule
// is refuse rather than render.
//
// The classes are split by MEASUREMENT against the real emulator, not by intuition
// (r8r3_sanitizeproperty_test.go re-runs the measurement, so a future Unicode release that
// contradicts it fails rather than silently eating a column):
//
//	unicode.Cf, Zl, Zp        the emulator gives them NO CELL -- every one, checked over the
//	                          whole plane -- so dropping preserves parity. Zl/Zp are U+2028
//	                          and U+2029, which Android lays out as LINE BREAKS.
//	Default_Ignorable \ Cf    zero-width too (the variation selectors, U+034F, U+17B4/5,
//	                          U+2065, the reserved ranges, the non-format E0000 members),
//	                          EXCEPT the ones with a Letter category -- see invisibleCellRune.
func zeroWidthFormatRune(r rune) bool {
	if invisibleCellRune(r) {
		return false // it occupies a cell; the REPLACE class owns it
	}
	return unicode.Is(unicode.Cf, r) ||
		unicode.Is(unicode.Zl, r) ||
		unicode.Is(unicode.Zp, r) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r)
}

// invisibleCellRune is the REPLACE class: a rune that renders as NOTHING on a phone and that
// the terminal nonetheless gave a cell to. Today that is exactly the four Hangul fillers
// (U+115F, U+1160, U+3164, U+FFA0), and the property that picks them out is
// "default-ignorable, not a format character, and carrying a Letter's General_Category" --
// which is what makes a text stack lay them out at all.
//
// THE TWO WRONG ANSWERS ARE SYMMETRICAL, and both were shipped by someone at some point.
// Keeping them leaves an invisible splice inside a line the user reads (`pay<U+3164>pal.com`).
// Dropping them removes one or two columns the terminal spent, so every character to the
// right of the payload lands under the wrong column on the phone -- which is the same class
// of harm as the U+2028 row split, one axis over. Replacing with a blank is the only answer
// that is both invisible-free and parity-preserving.
func invisibleCellRune(r rune) bool {
	return unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) &&
		!unicode.Is(unicode.Cf, r) &&
		unicode.IsLetter(r)
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
