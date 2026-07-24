package vt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// audit-006 producer-side clamps: a snapshot's two free-form fields (Run.Text and
// Snap.Title) are byte-bounded so a pathological/hostile agent cannot inflate a
// snapshot (N-6), and the Epic 6 reassembly cap derived from these bounds stays
// sound.

// TestClampBytes_RuneBoundary asserts the clamp bounds the byte length without
// splitting a multi-byte rune, and leaves an already-short string untouched.
func TestClampBytes_RuneBoundary(t *testing.T) {
	if got := clampBytes("hello", SnapshotTextMax); got != "hello" {
		t.Errorf("short string changed: %q", got)
	}
	// 2-byte runes: 100*"é" = 200 bytes -> clamp to <= 64, still valid UTF-8.
	out := clampBytes(strings.Repeat("é", 100), 64)
	if len(out) > 64 || !utf8.ValidString(out) {
		t.Errorf("2-byte clamp: len=%d valid=%v", len(out), utf8.ValidString(out))
	}
	// 3-byte runes straddling the boundary are dropped whole, never split.
	out3 := clampBytes(strings.Repeat("中", 30), 64) // 90 bytes -> 63 (21 runes)
	if len(out3) > 64 || !utf8.ValidString(out3) {
		t.Errorf("3-byte clamp split a rune: len=%d valid=%v", len(out3), utf8.ValidString(out3))
	}
}

// TestSnapshot_ClampsHostileTextAndTitle asserts that an absurdly long single-cell
// grapheme run and an absurdly long window title both serialize to bounded fields.
func TestSnapshot_ClampsHostileTextAndTitle(t *testing.T) {
	e := NewEmulator(20, 5)
	// One cell fed thousands of combining marks (a single, absurdly long grapheme
	// cluster). Wrapped in its own SGR (red) so item 4.3 run-merging keeps it a
	// SINGLE-cell run distinct from the trailing default blanks — otherwise the
	// hostile cell would merge with those blanks and the per-run length below would
	// measure the whole span, not the per-cell clamp. (A base char + marks would
	// instead split into two same-style cells that re-merge, so the marks stand
	// alone here.) The N-6 aggregate bound is unchanged: a merged line's text is
	// still <= cols * SnapshotTextMax.
	e.Feed([]byte("\x1b[31m" + strings.Repeat("́", 4000) + "\x1b[0m"))
	// An absurdly long OSC window title.
	e.Feed([]byte("\x1b]0;" + strings.Repeat("T", 4000) + "\x07"))

	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	s, err := DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}

	for y, ln := range s.Lines {
		for _, r := range ln.Runs {
			if len(r.Text) > SnapshotTextMax {
				t.Errorf("row %d Run.Text = %d bytes, want <= %d (clamped)", y, len(r.Text), SnapshotTextMax)
			}
		}
	}
	if len(s.Title) == 0 {
		t.Fatalf("window title was not captured; cannot verify the title clamp")
	}
	if len(s.Title) > SnapshotTitleMax {
		t.Errorf("Title = %d bytes, want <= %d (clamped)", len(s.Title), SnapshotTitleMax)
	}
}
