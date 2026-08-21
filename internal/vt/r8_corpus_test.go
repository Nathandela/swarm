package vt

// WAVE R8 / SLICE S5 -- THE ADVERSARIAL SANITIZER CORPUS (FAILING-FIRST, GG-5).
//
// ADR-017 T1 states the property this file is the proof obligation for, and states it
// honestly as a WEAKENING: "The A7 no-injection property returns to resting on the
// sanitizer. Under ADR-009 it was structural-by-absence; under T1 it is structural-by-
// SnapText. That is a weaker kind of proof -- one function instead of one missing screen
// -- and it puts Wave R8's adversarial ANSI/OSC/Unicode fixture work on the critical path
// rather than in the nice-to-have column" (ADR-017:231). Amendment T4-c is the ruling this
// file tests.
//
// EVERY FIXTURE ASSERTS THE EXACT SANITIZED STRING, not "nothing crashed" and not "no
// control byte survived". A corpus that only asserts absence cannot catch the sanitizer
// that answers by deleting the row, and cannot catch the one that answers by deleting a
// column the emulator counted. The two treatments and their reason are the file's own rule:
//
//   - a rune that OCCUPIES A CELL (C0 incl. ESC/LF/CR, DEL, C1) is REPLACED WITH A SPACE,
//     so a row's written character count keeps pace with its declared Width;
//   - a rune of ZERO DISPLAY WIDTH (bidi formatting/override/isolate, zero-width, the Cf
//     format block, the Unicode line/paragraph separators U+2028/U+2029, the interlinear-
//     annotation controls, and the U+E0000-U+E007F TAG block) is DROPPED, because a space
//     there would add a column the emulator never counted.
//
// U+2028/U+2029 are in the DROP class rather than the replace class, and the reason is
// MEASURED rather than assumed: on the real pipeline (r8_hostilepty_test.go) the emulator
// assigns them no cell at all -- `above<U+2028>below` renders as a ten-column row, not an
// eleven-column one -- so replacing them with a space here would add a column the emulator
// never counted, which is precisely the parity break the drop treatment exists to avoid.
//
// WHY THE NEW CLASSES ARE NOT A "WHILE WE ARE HERE". Each one below is a named spoof with a
// named payoff on a PHONE, which is a different renderer from a terminal:
//
//   - U+2028/U+2029 are laid out as LINE BREAKS by Android's text stack. One grid row
//     becomes two, every row after it shifts, and the phone's own chrome then reads a row
//     of the terminal as a row of something else -- with NO control byte anywhere in the
//     payload for a control-byte filter to catch (ADR-017 T4-c).
//   - U+00AD (SOFT HYPHEN) and U+180E render as nothing and split a word: `pay<AD>pal.com`
//     reads as `paypal.com` while comparing unequal to it.
//   - U+2060-U+2064 (WORD JOINER and the invisible operators) splice or hide content
//     exactly as U+200B does, and are not in the U+200B-U+200F range the F7 fix covered.
//   - U+FFF9-U+FFFB (interlinear annotation) let one visible string carry a second hidden
//     one.
//   - U+E0000-U+E007F (TAG) is a full invisible ASCII alphabet: an entire second sentence
//     can ride inside a line that displays as innocuous.
//
// This file uses NO symbol that does not exist today, on purpose: its RED must be a list of
// named assertion failures a reviewer can read one by one, not a single compile error that
// masks the corpus it is supposed to be.

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// r8Snap builds a one-row Snap whose single run carries text verbatim, so every assertion
// below is about SnapText's OWN sanitization and never about the producer-side filter in
// emulator.go. cols is the DECLARED geometry, which is what the bounds assertions read.
func r8Snap(text string, cols int) *Snap {
	return &Snap{
		Version: SnapshotVersion, Cols: cols, Rows: 1, CursorVisible: true,
		Lines: []Line{{Runs: []Run{{Text: text, Width: cols}}}},
	}
}

// r8Fixture is one named hostile payload and the EXACT line SnapText must produce from it.
type r8Fixture struct {
	name    string // the spoof, named by what it buys the attacker
	spoof   string // what a reader must understand the fixture is defending against
	raw     string // the run text, verbatim
	want    string // the exact sanitized line
	newRule bool   // true when ADR-017 T4-c added this class (these are the RED rows)
}

// r8HostileCorpus is the corpus. Rows marked newRule are the ones amendment T4-c adds; the
// unmarked rows are the F7/N-6 classes already ruled on, kept here so a future change that
// widens one class cannot silently narrow another.
var r8HostileCorpus = []r8Fixture{
	// ---- ANSI / OSC / DCS: sequences that make the PHONE'S OWN CHROME lie, or exfiltrate.
	{
		name:  "osc52_clipboard_write",
		spoof: "OSC 52 writes the system clipboard of whatever renders it; on a re-injection path it is exfiltration by paste",
		raw:   "\x1b]52;c;cGF5bG9hZA==\x07",
		want:  " ]52;c;cGF5bG9hZA== ",
	},
	{
		name:  "osc0_window_title_spoof",
		spoof: "OSC 0/2 rewrites the host window title: the chrome around the grid starts asserting something the session did not",
		raw:   "\x1b]0;Swarm - approved by owner\x07real output",
		want:  " ]0;Swarm - approved by owner real output",
	},
	{
		name:  "osc8_hyperlink_masking",
		spoof: "OSC 8 binds arbitrary display text to an arbitrary URL: `Click` that resolves to an attacker host",
		raw:   "\x1b]8;;https://evil.example\x1b\\Click\x1b]8;;\x1b\\",
		want:  " ]8;;https://evil.example \\Click ]8;; \\",
	},
	{
		name:  "dcs_device_control_string",
		spoof: "DCS carries terminfo/sixel payloads; on a naive re-render it is an arbitrary-length binary channel",
		raw:   "\x1bP+q544e\x1b\\",
		want:  " P+q544e \\",
	},
	{
		name:  "csi_dsr_cursor_position_report",
		spoof: "CSI 6n makes the RENDERER answer on its input channel; a rendered-and-replayed report is unsolicited input",
		raw:   "\x1b[6n",
		want:  " [6n",
	},
	{
		name:  "csi_printer_controller_on",
		spoof: "CSI 5i opens the printer-controller channel: everything after it is diverted verbatim",
		raw:   "\x1b[5i",
		want:  " [5i",
	},
	{
		name:  "apc_application_program_command",
		spoof: "APC is an unbounded application-defined string; Kitty-family terminals act on it",
		raw:   "\x1b_Gf=100;payload\x1b\\",
		want:  " _Gf=100;payload \\",
	},
	{
		name:  "pm_privacy_message",
		spoof: "PM is a second unbounded string channel with no standard terminator handling",
		raw:   "\x1b^p\x1b\\",
		want:  " ^p \\",
	},
	{
		name:  "sos_start_of_string",
		spoof: "SOS is the third; a parser that resynchronises differently from the sanitizer is a bypass",
		raw:   "\x1bXpayload\x1b\\",
		want:  " Xpayload \\",
	},
	{
		name:  "c1_eight_bit_csi",
		spoof: "U+009B is CSI in one byte: a filter written against ESC-[ alone never sees it",
		raw:   "\u009b6n",
		want:  " 6n",
	},
	{
		name:  "c1_eight_bit_osc_with_st",
		spoof: "U+009D/U+009C are OSC and ST in one byte each: the whole OSC 52 above, with no ESC in it",
		raw:   "\u009d52;c;cGF5bG9hZA==\u009c",
		want:  " 52;c;cGF5bG9hZA== ",
	},
	{
		name:  "truncated_csi_introducer",
		spoof: "a half-sequence at a chunk boundary is where a stateful stripper resynchronises wrongly",
		raw:   "\x1b[",
		want:  " [",
	},
	{
		name:  "escape_flood",
		spoof: "a run of bare ESC with no final byte: the shape that makes a state machine consume the text after it",
		raw:   "\x1b\x1b\x1b",
		want:  "   ",
	},
	{
		name:  "backspace_overwrite",
		spoof: "BS rewrites what a naive consumer displays without changing the bytes it stores",
		raw:   "admin\x08\x08\x08\x08\x08guest",
		want:  "admin     guest",
	},
	{
		name:  "nul_and_del_padding",
		spoof: "NUL and DEL are the two controls most often dropped rather than replaced, which desynchronises columns",
		raw:   "a\x00b\x7fc",
		want:  "a b c",
	},
	{
		name:  "nel_next_line",
		spoof: "U+0085 is a C1 line break: an embedded newline with no 0x0a in the payload",
		raw:   "row1\u0085row2",
		want:  "row1 row2",
	},

	// ---- UNICODE: no control byte anywhere, and the phone still renders a lie.
	{
		name:    "line_separator_u2028",
		spoof:   "Android lays U+2028 out as a LINE BREAK: one grid row becomes two and every row below it shifts, with no control byte in the payload",
		raw:     "connected\u2028owner approved",
		want:    "connectedowner approved",
		newRule: true,
	},
	{
		name:    "paragraph_separator_u2029",
		spoof:   "U+2029 is the same break one level up, and is the class a U+2028-only fix leaves open",
		raw:     "connected\u2029owner approved",
		want:    "connectedowner approved",
		newRule: true,
	},
	{
		name:    "soft_hyphen_u00ad",
		spoof:   "U+00AD renders as nothing: `pay<SHY>pal.com` displays as paypal.com and compares unequal to it",
		raw:     "pay\u00adpal.com",
		want:    "paypal.com",
		newRule: true,
	},
	{
		name:    "mongolian_vowel_separator_u180e",
		spoof:   "U+180E is the zero-width separator outside the U+200B-U+200F range the F7 fix covered",
		raw:     "ad\u180emin",
		want:    "admin",
		newRule: true,
	},
	{
		name:    "word_joiner_u2060",
		spoof:   "U+2060 is U+200B's non-breaking twin, and is not in U+200B-U+200F",
		raw:     "a\u2060b",
		want:    "ab",
		newRule: true,
	},
	{
		name:    "invisible_operators_u2061_u2064",
		spoof:   "U+2061-U+2064 are four more zero-width runes that splice content invisibly",
		raw:     "rm\u2061 \u2062-rf\u2063 \u2064/",
		want:    "rm -rf /",
		newRule: true,
	},
	{
		name:    "interlinear_annotation_ufff9_ufffb",
		spoof:   "U+FFF9-U+FFFB let one visible string carry a second, hidden annotation string",
		raw:     "ok\ufff9hidden\ufffashown\ufffb",
		want:    "okhiddenshown",
		newRule: true,
	},
	{
		name:    "tag_block_ue0000_ue007f",
		spoof:   "the TAG block is a complete invisible ASCII alphabet: a whole second sentence riding inside an innocuous line",
		raw:     "safe\U000E0001\U000E0074\U000E0065\U000E0078\U000E0074\U000E007Ftext",
		want:    "safetext",
		newRule: true,
	},

	// ---- The F7 classes, kept as a fence so widening one class cannot narrow these.
	{
		name:  "rlo_trojan_source",
		spoof: "U+202E reorders the DISPLAY of everything after it without changing a byte",
		raw:   "safe\u202etxet.exe",
		want:  "safetxet.exe",
	},
	{
		name:  "isolate_pair_u2066_u2069",
		spoof: "the isolate pair is the Trojan-Source variant that survives an override-only filter",
		raw:   "a\u2066b\u2069c",
		want:  "abc",
	},
	{
		name:  "zero_width_space_splice",
		spoof: "U+200B hides a splice point inside what reads as one word",
		raw:   "ad\u200bmin",
		want:  "admin",
	},
	{
		name:  "bom_u_feff",
		spoof: "U+FEFF is the zero-width no-break space wearing a BOM's name",
		raw:   "\ufeffadmin",
		want:  "admin",
	},
}

// TestR8Corpus_SanitizedOutputIsExact is the corpus itself: one subtest per named spoof,
// each asserting the EXACT line SnapText produces.
func TestR8Corpus_SanitizedOutputIsExact(t *testing.T) {
	for _, f := range r8HostileCorpus {
		t.Run(f.name, func(t *testing.T) {
			lines := SnapText(r8Snap(f.raw, len([]rune(f.raw))))
			if len(lines) != 1 {
				t.Fatalf("SnapText returned %d lines, want 1", len(lines))
			}
			if lines[0] != f.want {
				t.Errorf("ADR-017 T4-c, spoof %q:\n  raw  = %q\n  got  = %q\n  want = %q\nWhat the attacker buys if this ships: %s",
					f.name, f.raw, lines[0], f.want, f.spoof)
			}
		})
	}
}

// TestR8Corpus_NoRowEverBecomesTwo is the U+2028/U+2029 rule stated as the property it
// protects rather than as a rune list, so a fix that strips the two runes but leaves a
// third line-breaking class open still fails.
//
// SnapText's contract is "one string per grid row" (render.go:136). A phone that lays a row
// out as two rows has been handed a second row the machine never rendered, and every row
// index below it is off by one -- which is exactly how a status line ends up reading as
// terminal output.
func TestR8Corpus_NoRowEverBecomesTwo(t *testing.T) {
	// Every rune Unicode classifies as a line or paragraph separator, plus the C0/C1
	// breaks. If any of them survives, some renderer somewhere splits the row.
	var breaking []rune
	for r := rune(0); r <= 0x2FFFF; r++ {
		if unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			breaking = append(breaking, r)
		}
	}
	breaking = append(breaking, '\n', '\r', 0x0b, 0x0c, 0x85)
	for _, r := range breaking {
		lines := SnapText(r8Snap("above"+string(r)+"below", 10))
		if len(lines) != 1 {
			t.Fatalf("rune %U: SnapText returned %d lines, want 1", r, len(lines))
		}
		if strings.ContainsRune(lines[0], r) {
			t.Errorf("ADR-017 T4-c: line-breaking rune %U survived into a phone-displayed row (%q). "+
				"A row that the phone lays out as two rows shifts every row below it, and no control "+
				"byte is present for a control-byte filter to catch.", r, lines[0])
		}
	}
}

// TestR8Corpus_ZeroWidthFormatRunesAreDroppedNotSpaced pins the SECOND half of the two-
// treatment rule: a rune of zero display width must be DROPPED, because replacing it with a
// space adds a column the emulator never counted, and a column added on the phone is a
// column of the row shifted right -- the same class of lie as a split row, in the other
// axis.
func TestR8Corpus_ZeroWidthFormatRunesAreDroppedNotSpaced(t *testing.T) {
	zeroWidth := []rune{
		0x00ad, 0x180e, 0x2028, 0x2029, 0x200b, 0x200c, 0x200d, 0x200e, 0x200f, 0x061c,
		0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2060, 0x2061, 0x2062,
		0x2063, 0x2064, 0x2066, 0x2067, 0x2068, 0x2069, 0xfeff,
		0xfff9, 0xfffa, 0xfffb, 0xE0001, 0xE0041, 0xE007F,
	}
	for _, r := range zeroWidth {
		lines := SnapText(r8Snap("ab"+string(r)+"cd", 4))
		if len(lines) != 1 {
			t.Fatalf("rune %U: SnapText returned %d lines, want 1", r, len(lines))
		}
		if lines[0] != "abcd" {
			t.Errorf("ADR-017 T4-c: rune %U must be DROPPED (zero display width), not replaced and not "+
				"kept: got %q, want %q", r, lines[0], "abcd")
		}
	}
}

// TestR8Corpus_CombiningMarkDepthIsClampedPerCell is the "Zalgo" vector: a single base
// character carrying hundreds of combining marks renders as a vertical smear that overdraws
// the rows above and below it on a phone's text stack, which no per-rune filter sees
// because every rune involved is individually legitimate.
//
// The assertion is a SHAPE, not a magic number, so the clamp GREEN chooses is free within a
// fenced ceiling and cannot be chosen as "no clamp":
//
//	(1) the base character survives;
//	(2) the marks that survive are a PREFIX of the marks supplied -- nothing reordered,
//	    nothing invented;
//	(3) the surviving count is strictly less than what was supplied AND at most 16.
//
// Sixteen is a ceiling and not a recommendation: Unicode's own stream-safe format
// (UAX #15) bounds a defective combining sequence at 30, and no legitimate terminal cell
// carries more than a handful.
func TestR8Corpus_CombiningMarkDepthIsClampedPerCell(t *testing.T) {
	const supplied = 500
	const ceiling = 16
	base := "a"
	marks := strings.Repeat("\u0301", supplied) // COMBINING ACUTE ACCENT
	lines := SnapText(r8Snap(base+marks+"z", 3))
	if len(lines) != 1 {
		t.Fatalf("SnapText returned %d lines, want 1", len(lines))
	}
	got := lines[0]
	if !strings.HasPrefix(got, base) {
		t.Fatalf("the base character must survive the clamp: got %q", got)
	}
	if !strings.HasSuffix(got, "z") {
		t.Errorf("text AFTER the clamped cluster must survive: got %q", got)
	}
	n := strings.Count(got, "\u0301")
	if n >= supplied {
		t.Errorf("ADR-017 T4-c: %d combining marks on one cell survived unclamped (supplied %d). "+
			"A cell with hundreds of marks smears vertically across the rows above and below it on the "+
			"phone, and every rune in it is individually legitimate so no rune filter sees it.", n, supplied)
	}
	if n > ceiling {
		t.Errorf("ADR-017 T4-c: per-cell combining depth clamped to %d, which is above the fenced "+
			"ceiling of %d (UAX #15 stream-safe format bounds a defective combining sequence at 30; a "+
			"real terminal cell carries a handful)", n, ceiling)
	}
	if idx := strings.IndexRune(got, 'z'); idx >= 0 {
		if inner := got[len(base):idx]; inner != strings.Repeat("\u0301", n) {
			t.Errorf("the surviving marks must be a PREFIX of the supplied marks, unreordered and "+
				"uninvented: got %q", inner)
		}
	}
}

// TestR8Corpus_InvalidUnicodeBecomesAnExplicitReplacementGlyph is playbook:457-458's
// "supplies replacement glyphs for invalid Unicode", asserted as BEHAVIOR rather than as a
// side effect of rune iteration.
//
// Both halves matter. Invalid bytes must become U+FFFD (not vanish, which would silently
// shorten a row, and not survive, which would hand the phone's decoder the malformed input
// the machine was supposed to absorb). And a lone surrogate encoded in WTF-8 -- the shape a
// JSON/UTF-16 boundary produces -- must take the same path.
func TestR8Corpus_InvalidUnicodeBecomesAnExplicitReplacementGlyph(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"lone_continuation_byte", "a" + string([]byte{0x80}) + "b", "a\uFFFDb"},
		{"truncated_three_byte_sequence", "a" + string([]byte{0xe2, 0x82}) + "b", "a\uFFFD\uFFFDb"},
		{"overlong_encoding_of_slash", "a" + string([]byte{0xc0, 0xaf}) + "b", "a\uFFFD\uFFFDb"},
		{"wtf8_lone_surrogate", "a" + string([]byte{0xed, 0xa0, 0x80}) + "b", "a\uFFFD\uFFFD\uFFFDb"},
		{"raw_ff_byte", "a" + string([]byte{0xff}) + "b", "a\uFFFDb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := SnapText(r8Snap(c.raw, 4))
			if len(lines) != 1 {
				t.Fatalf("SnapText returned %d lines, want 1", len(lines))
			}
			if !utf8.ValidString(lines[0]) {
				t.Errorf("a phone-displayed row must be valid UTF-8; got %q from %q", lines[0], c.raw)
			}
			if lines[0] != c.want {
				t.Errorf("playbook:457-458 (replacement glyphs for invalid Unicode): got %q, want %q (from %q)",
					lines[0], c.want, c.raw)
			}
		})
	}
}

// TestR8Corpus_ARowIsBoundedByItsDeclaredGeometry is the oversized-cell/oversized-line
// vector, and it is a denial-of-service on the phone rather than a spoof.
//
// A Snap's geometry is DECLARED (Cols/Rows) and its content is not bound to it: buildLine
// clamps each cell to SnapshotTextMax on the producer side, but SnapText is a choke point in
// its own right ("stripping directly rather than trusting the producer-side N-6 filter",
// render.go:140-143) and today it concatenates whatever the runs carry. A skewed or
// compromised producer that declares an 80x24 grid and ships one 4 MB run hands the phone a
// 4 MB single-line text node to lay out, and ships it against the shared 8-appends/s budget.
func TestR8Corpus_ARowIsBoundedByItsDeclaredGeometry(t *testing.T) {
	const cols = 80
	huge := strings.Repeat("A", 4<<20)
	lines := SnapText(r8Snap(huge, cols))
	if len(lines) != 1 {
		t.Fatalf("SnapText returned %d lines, want 1", len(lines))
	}
	// SnapshotTextMax is the producer's own per-cell byte cap; cols cells is the row's
	// ceiling under the declared geometry.
	if max := SnapshotTextMax * cols; len(lines[0]) > max {
		t.Errorf("ADR-017 T4/T5-a: a row of a %d-column grid rendered %d bytes, above the %d-byte "+
			"ceiling its declared geometry implies (SnapshotTextMax=%d per cell). An unbounded row is a "+
			"layout denial-of-service on the phone and it is spent from the shared append budget.",
			cols, len(lines[0]), max, SnapshotTextMax)
	}
}

// TestR8Corpus_AGridIsBoundedByItsDeclaredRowCount is the same vector in the other axis: a
// Snap that declares Rows=24 and carries 50000 Lines.
func TestR8Corpus_AGridIsBoundedByItsDeclaredRowCount(t *testing.T) {
	const rows = 24
	s := &Snap{Version: SnapshotVersion, Cols: 4, Rows: rows, CursorVisible: true}
	for i := 0; i < 50000; i++ {
		s.Lines = append(s.Lines, Line{Runs: []Run{{Text: "row", Width: 3}}})
	}
	if got := len(SnapText(s)); got > rows {
		t.Errorf("ADR-017 T4/T5-a: a grid declaring %d rows flattened to %d lines. The declared "+
			"geometry is what the phone allocates against; a producer that declares small and ships "+
			"large is an unbounded allocation on the handset.", rows, got)
	}
}

// TestR8Corpus_ColumnParityIsPreservedUnderSanitization is the regression the two-treatment
// rule exists for, and it is the one a "just drop everything hostile" fix breaks.
//
// clipRunPrefix (render.go:200-212) re-walks a merged run's text to reproduce the per-cell
// widths the emulator assigned. If sanitization deletes a rune that occupied a column, the
// walk desynchronises from the declared Run.Width and the clipped row is wrong -- silently,
// and only on rows wide enough to clip.
func TestR8Corpus_ColumnParityIsPreservedUnderSanitization(t *testing.T) {
	for _, f := range r8HostileCorpus {
		t.Run(f.name, func(t *testing.T) {
			// The rule as arithmetic: sanitizing removes exactly the zero-width runes and
			// keeps the rune count otherwise, because a column-occupying rune is replaced
			// (one rune out, one rune in) and never deleted.
			var dropped int
			for _, r := range f.raw {
				if r8ZeroWidthClass(r) {
					dropped++
				}
			}
			wantRunes := utf8.RuneCountInString(f.raw) - dropped
			if n := utf8.RuneCountInString(f.want); n != wantRunes {
				t.Fatalf("the CORPUS itself violates the parity rule: %q has %d runes of which %d are "+
					"zero-width, so the expectation must be %d runes, but %q is %d. Fix the fixture, "+
					"not the rule.", f.raw, utf8.RuneCountInString(f.raw), dropped, wantRunes, f.want, n)
			}
			if got := SnapText(r8Snap(f.raw, 8))[0]; utf8.RuneCountInString(got) != wantRunes {
				t.Errorf("column parity: %q sanitized to %q (%d runes), want %d runes. A control rune "+
					"DELETED instead of replaced desynchronises clipRunPrefix's walk from the declared "+
					"Run.Width, and the desync only shows on rows wide enough to clip.",
					f.raw, got, utf8.RuneCountInString(got), wantRunes)
			}
		})
	}
}

// r8ZeroWidthClass is the corpus's statement of which runes ADR-017 T4-c puts in the DROP
// treatment. It is deliberately a second, independent spelling of the rule: if production's
// stripControls and this predicate disagree, the parity test above says so.
func r8ZeroWidthClass(r rune) bool {
	switch {
	case r == 0x00ad, r == 0x061c, r == 0x180e:
		return true
	case r == 0x2028, r == 0x2029:
		return true
	case r >= 0x200b && r <= 0x200f:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2060 && r <= 0x2064:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	case r == 0xfeff:
		return true
	case r >= 0xfff9 && r <= 0xfffb:
		return true
	case r >= 0xE0000 && r <= 0xE007F:
		return true
	}
	return false
}
