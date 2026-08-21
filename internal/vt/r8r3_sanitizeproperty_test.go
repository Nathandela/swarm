package vt

// WAVE R8 / ROUND 3 -- THE DROP CLASS BECOMES A PROPERTY, BECAUSE THE ENUMERATION LEAKED.
//
// THE DEFECT, measured rather than reasoned. Round 2's `zeroWidthFormatRune` was twelve
// hand-written switch arms. Probed directly, these ALL SURVIVED it unchanged, each splicing
// an invisible payload into a rendered row with no control byte present:
//
//	U+206A-U+206F  INHIBIT SYMMETRIC SWAPPING and the deprecated format block -- unicode.Cf,
//	               ONE CODE POINT past the U+2060-U+2064 range this wave added by hand;
//	U+1D173-U+1D17A  the musical format controls -- unicode.Cf;
//	U+115F, U+1160, U+3164, U+FFA0  the Hangul fillers -- Other_Default_Ignorable_Code_Point.
//
// The wave's own corpus already states the line-separator half AS A PROPERTY ("over all
// Unicode Zl/Zp, not a rune list") and then left the format half as an enumeration, so the
// next block Unicode adds -- or the next one a reviewer does not think of -- is through. That
// is "refuse rather than render" inverted: the default was allow.
//
// THE MEASUREMENT THE CLASSIFICATION IS BUILT ON, taken on the real emulator, `a<RUNE>b` into
// an 80-column grid, counting cells rather than runes:
//
//	EVERY unicode.Cf rune          the emulator gives NO CELL. Dropping preserves parity.
//	U+2028 / U+2029 (Zl/Zp)        no cell either -- which is why this wave already drops
//	                               rather than replaces them.
//	ODI \ Cf, four runes only      U+115F (2 cells), U+1160 (1), U+3164 (2), U+FFA0 (1).
//	                               They OCCUPY the terminal's grid and render as NOTHING on a
//	                               phone, so dropping would lose a column the terminal spent.
//	ODI \ Cf, everything else      no cell (the variation selectors, U+034F, the reserved
//	                               ranges, the E0000 block's non-format members).
//
// So there are two classes and the split is a stdlib property, not a list:
//
//	DROP     unicode.Cf | unicode.Zl | unicode.Zp | (ODI and not a Letter)
//	REPLACE  ODI \ Cf that is a LETTER -- today exactly the four Hangul fillers, which are
//	         the only default-ignorable runes with a General_Category the emulator lays out.
//
// The tests below are written as PROPERTIES OVER THE WHOLE UNICODE RANGE, so a future
// Unicode release that adds a format character is covered on the day the Go toolchain's
// tables learn about it, with nobody editing a switch.

import (
	"strings"
	"testing"
	"unicode"
)

// r8r3Cells returns how many grid cells the real emulator gives r (0 = it assigns none).
// It is the measurement the whole classification rests on, run rather than asserted.
func r8r3Cells(t *testing.T, r rune) int {
	t.Helper()
	e := NewEmulator(80, 3)
	defer func() { _ = e.Close() }()
	e.Feed([]byte("a" + string(r) + "b"))
	raw, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snap, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	// Decode WITHOUT the sanitizer, so this measures the emulator and not the fix.
	line := snap.Lines[0]
	var b strings.Builder
	for _, run := range line.Runs {
		b.WriteString(run.Text)
	}
	if !strings.ContainsRune(b.String(), r) {
		return 0
	}
	n := 0
	for range b.String() {
		n++
	}
	return 80 - n + 1
}

// r8r3Invisible is every rune whose display width on a PHONE is zero regardless of what the
// terminal did with it: the format characters, the two Unicode line/paragraph separators, and
// the default-ignorable code points. It is stated once and used by every property below.
func r8r3Invisible(r rune) bool {
	return unicode.Is(unicode.Cf, r) ||
		unicode.Is(unicode.Zl, r) ||
		unicode.Is(unicode.Zp, r) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r)
}

// TestR8R3_NoInvisibleRuneSurvivesSanitization is the property the enumeration approximated.
// It is stated over the WHOLE of Unicode, so the escapees the review found -- and the ones
// nobody has found yet -- are all one assertion.
func TestR8R3_NoInvisibleRuneSurvivesSanitization(t *testing.T) {
	var leaked []rune
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue // surrogates are not scalar values; a Go string cannot carry one
		}
		if !r8r3Invisible(r) {
			continue
		}
		if strings.ContainsRune(stripControls("a"+string(r)+"b"), r) {
			leaked = append(leaked, r)
			if len(leaked) > 12 {
				break
			}
		}
	}
	if len(leaked) > 0 {
		t.Fatalf("%d invisible rune(s) survived stripControls, e.g. %s.\n"+
			"An invisible rune in a rendered terminal row is a second, hidden string riding "+
			"inside a line the user reads as innocuous -- and no control byte is present, so "+
			"nothing upstream sees it. The drop set must be the PROPERTY (unicode.Cf, Zl, Zp "+
			"and Other_Default_Ignorable_Code_Point), not a list of the ones somebody thought "+
			"of: the review got U+206A-206F, U+1D173-1D17A and the Hangul fillers through an "+
			"enumeration that had already been reviewed twice.",
			len(leaked), r8r3FormatRunes(leaked))
	}
}

// r8r3FormatRunes renders a rune list as code points for a failure message.
func r8r3FormatRunes(rs []rune) string {
	var b strings.Builder
	for i, r := range rs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(runeHex(r))
	}
	return b.String()
}

func runeHex(r rune) string {
	const hexdigits = "0123456789ABCDEF"
	var out []byte
	for v := uint32(r); v > 0; v >>= 4 {
		out = append([]byte{hexdigits[v&0xF]}, out...)
	}
	if len(out) < 4 {
		out = append([]byte(strings.Repeat("0", 4-len(out))), out...)
	}
	return "U+" + string(out)
}

// TestR8R3_TheReviewsEscapeesAreEachNamed keeps the measured attack on the record. The
// property above subsumes them; this fails with the SPOOF's name so a reader of a failing run
// learns what was possible rather than only that a set shrank.
func TestR8R3_TheReviewsEscapeesAreEachNamed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		r     rune
		spoof string
	}{
		{"inhibit_symmetric_swapping_u206a", 0x206A, "one code point past the hand-written U+2060-U+2064 arm"},
		{"deprecated_format_u206f", 0x206F, "the far end of the same block, likewise unlisted"},
		{"musical_format_u1d173", 0x1D173, "a format control in the musical-symbols plane"},
		{"musical_format_u1d17a", 0x1D17A, "the far end of that block"},
		{"hangul_choseong_filler_u115f", 0x115F, "occupies two terminal cells and renders as nothing on a phone"},
		{"hangul_jungseong_filler_u1160", 0x1160, "one terminal cell, invisible on a phone"},
		{"hangul_filler_u3164", 0x3164, "two terminal cells, invisible on a phone"},
		{"halfwidth_hangul_filler_uffa0", 0xFFA0, "one terminal cell, invisible on a phone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stripControls("pay" + string(tc.r) + "pal.com")
			if strings.ContainsRune(got, tc.r) {
				t.Fatalf("%s survived sanitization (%s): rendered %q", runeHex(tc.r), tc.spoof, got)
			}
		})
	}
}

// TestR8R3_ADroppedRuneCostTheTerminalNoColumnAndAReplacedOneDid is the parity half, and it
// is what makes the two classes two classes rather than one. It MEASURES each invisible rune
// against the real emulator and asserts the sanitizer's treatment matches what the terminal
// actually spent:
//
//   - a rune the emulator gave no cell must be DROPPED (a space would add a column nothing
//     counted, which is the parity break the drop treatment exists to prevent);
//   - a rune the emulator DID lay out must leave a blank behind (dropping would shift every
//     column after it left, against a terminal that did not).
func TestR8R3_ADroppedRuneCostTheTerminalNoColumnAndAReplacedOneDid(t *testing.T) {
	// The whole-plane sweep is the property test above; this one measures, which costs an
	// emulator per rune, so it runs over the runes that can differ: ODI outside Cf is the
	// only region where anything occupies a cell at all.
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		if !unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		occupies := r8r3Cells(t, r) > 0
		got := stripControls("a" + string(r) + "b")
		if occupies && got != "a b" {
			t.Errorf("%s occupies a terminal cell but sanitized to %q; want a blank in its "+
				"place so the phone's columns line up with the terminal's", runeHex(r), got)
		}
		if !occupies && got != "ab" {
			t.Errorf("%s occupies NO terminal cell but sanitized to %q; a space here adds a "+
				"column the terminal never spent", runeHex(r), got)
		}
	}
}

// TestR8R3_EveryFormatRuneIsZeroWidthOnTheRealEmulator is the measurement the DROP class for
// unicode.Cf rests on, run rather than assumed. If a future Unicode release adds a format
// character the emulator lays out, this fails and the classification is revisited -- rather
// than the sanitizer silently eating a column.
func TestR8R3_EveryFormatRuneIsZeroWidthOnTheRealEmulator(t *testing.T) {
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		if !unicode.Is(unicode.Cf, r) && !unicode.Is(unicode.Zl, r) && !unicode.Is(unicode.Zp, r) {
			continue
		}
		if n := r8r3Cells(t, r); n != 0 {
			t.Errorf("%s is a format/separator rune the emulator lays out in %d cell(s); "+
				"dropping it would lose a column the terminal spent", runeHex(r), n)
		}
	}
}

// TestR8R3_VisibleRunesAreStillVisible is the vacuity guard for the whole file: a sanitizer
// that dropped everything would pass every property above and render an empty terminal.
func TestR8R3_VisibleRunesAreStillVisible(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    rune
	}{
		{"object_replacement_character_ufffc", 0xFFFC},
		{"braille_blank_u2800", 0x2800},
		{"latin_a", 'a'},
		{"cjk_ideograph", 0x4E2D},
		{"emoji", 0x1F600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.ContainsRune(stripControls("x"+string(tc.r)+"y"), tc.r) {
				t.Fatalf("%s was dropped; it is a VISIBLE glyph and a terminal that shows it "+
					"must show it on the phone too", runeHex(tc.r))
			}
		})
	}
}
