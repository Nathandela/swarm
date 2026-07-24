package vt

// Failing-first test for audit finding F7: SnapText (the phone-display flattener)
// stripped C0/DEL/C1 control bytes but let Unicode BIDIRECTIONAL control characters
// and ZERO-WIDTH characters through unchanged. A hostile PTY can emit these to mount
// a Trojan-Source style visual-spoofing attack on the phone viewer: U+202E (RIGHT-TO-
// LEFT OVERRIDE) reorders how the following text is DISPLAYED without changing its
// bytes, and zero-width characters (e.g. U+200B) can hide or splice content invisibly.
// Neither class is a C0/C1 control byte, so stripControls' pre-fix predicate (r < 0x20
// || 0x7f <= r <= 0x9f) let them pass straight through to the phone.

import (
	"strings"
	"testing"
)

// TestSnapText_StripsBidiAndZeroWidth feeds every bidi-formatting/override/isolate
// codepoint and every zero-width codepoint from the finding's required drop set
// directly into a Snap's run text and asserts NONE of them survive into SnapText's
// output. RED (pre-fix): every one of these runes round-trips unchanged because
// stripControls only strips C0/DEL/C1.
func TestSnapText_StripsBidiAndZeroWidth(t *testing.T) {
	hostile := []rune{
		0x061C, // ALM - Arabic Letter Mark
		0x200B, // ZWSP - zero width space
		0x200C, // ZWNJ - zero width non-joiner
		0x200D, // ZWJ - zero width joiner
		0x200E, // LRM - left-to-right mark
		0x200F, // RLM - right-to-left mark
		0x202A, // LRE - left-to-right embedding
		0x202B, // RLE - right-to-left embedding
		0x202C, // PDF - pop directional formatting
		0x202D, // LRO - left-to-right override
		0x202E, // RLO - right-to-left override (the classic Trojan-Source rune)
		0x2066, // LRI - left-to-right isolate
		0x2067, // RLI - right-to-left isolate
		0x2068, // FSI - first strong isolate
		0x2069, // PDI - pop directional isolate
		0xFEFF, // zero width no-break space / BOM
	}

	for _, r := range hostile {
		// Sandwich the hostile rune between plain ASCII so a naive "drop the whole run"
		// bug would also be caught: the plain text on either side must survive intact.
		text := "safe" + string(r) + "text"
		s := &Snap{
			Version: SnapshotVersion, Cols: 8, Rows: 1, CursorVisible: true,
			Lines: []Line{{Runs: []Run{{Text: text, Width: 1}}}},
		}
		lines := SnapText(s)
		if len(lines) != 1 {
			t.Fatalf("rune %U: SnapText returned %d lines, want 1", r, len(lines))
		}
		if strings.ContainsRune(lines[0], r) {
			t.Errorf("rune %U leaked into SnapText output: %q", r, lines[0])
		}
		if got, want := lines[0], "safetext"; got != want {
			t.Errorf("rune %U: surrounding plain text corrupted: got %q, want %q", r, got, want)
		}
	}
}
