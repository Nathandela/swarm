package vt

// Failing-first suite for SnapText, the phone-display flattener.
//
// SnapText is the hostile-PTY sanitization choke point for the phone viewer: it
// turns a VT snapshot grid into plain-text lines (one per row) that carry NO
// terminal control sequences. Unlike RenderSnapshot (which emits ANSI), the phone
// gets literal text only. These tests pin the security property — every control
// byte class is stripped even when fed directly into a hand-built Snap (defense in
// depth, not merely trusting the producer-side N-6 filter) — and the shape: one
// output line per grid row, plain text round-tripping unchanged.

import (
	"strings"
	"testing"
)

// TestSnapText_StripsAllControlBytes feeds every hostile control class DIRECTLY
// into the runs of a hand-constructed Snap and asserts none survives into any
// returned line: no byte < 0x20 (C0, incl. NUL/BEL/BS and embedded LF/CR), no DEL
// (0x7f), no C1 (0x80-0x9f), and no embedded '\n' or '\r'. Feeding the bytes
// straight into the Snap makes this assert SnapText's own sanitization, not the
// upstream emulator filter.
func TestSnapText_StripsAllControlBytes(t *testing.T) {
	s := &Snap{
		Version: SnapshotVersion, Cols: 8, Rows: 4, CursorVisible: true,
		Lines: []Line{
			// Row 0: a full CSI cursor-move / clear sequence.
			{Runs: []Run{{Text: "\x1b[2J\x1b[H", Width: 1}}},
			// Row 1: assorted C0 controls embedded mid-run — NUL, BEL, BS, ESC.
			{Runs: []Run{{Text: "a\x00b\x07c\x08d\x1be", Width: 1}}},
			// Row 2: embedded newline and carriage return WITHIN a run, plus DEL.
			{Runs: []Run{{Text: "l1\x0al2\x0dl3\x7f", Width: 1}}},
			// Row 3: C1 controls — a valid NEL rune (U+0085) and raw C1 bytes
			// (0x80, 0x9f) that arrive as invalid UTF-8; neither may leak.
			{Runs: []Run{
				{Text: "xy", Width: 1},
				{Text: string([]byte{'p', 0x80, 0x9f, 'q'}), Width: 1},
			}},
		},
	}

	lines := SnapText(s)
	if len(lines) != 4 {
		t.Fatalf("SnapText returned %d lines, want 4", len(lines))
	}

	for i, line := range lines {
		for j := 0; j < len(line); j++ {
			b := line[j]
			switch {
			case b < 0x20:
				t.Errorf("line %d byte %d = %#x: C0 control (< 0x20) leaked; line=%q", i, j, b, line)
			case b == 0x7f:
				t.Errorf("line %d byte %d = %#x: DEL (0x7f) leaked; line=%q", i, j, b, line)
			case b >= 0x80 && b <= 0x9f:
				t.Errorf("line %d byte %d = %#x: C1 control (0x80-0x9f) leaked; line=%q", i, j, b, line)
			}
		}
		if strings.ContainsAny(line, "\n\r") {
			t.Errorf("line %d contains an embedded newline/CR: %q", i, line)
		}
	}
}

// TestSnapText_RowCountMatchesGrid pins the shape: N grid rows yield N output
// lines, and a clean row of plain text round-trips unchanged.
func TestSnapText_RowCountMatchesGrid(t *testing.T) {
	s := &Snap{
		Version: SnapshotVersion, Cols: 5, Rows: 3, CursorVisible: true,
		Lines: []Line{
			{Runs: []Run{{Text: "hello", Width: 5}}},
			{Runs: []Run{{Text: " ", Width: 1}}},
			{Runs: []Run{{Text: "world", Width: 5}}},
		},
	}

	lines := SnapText(s)
	if len(lines) != 3 {
		t.Fatalf("SnapText returned %d lines, want 3 (one per grid row)", len(lines))
	}
	if lines[0] != "hello" {
		t.Errorf("plain row must round-trip: got %q, want %q", lines[0], "hello")
	}
	if lines[2] != "world" {
		t.Errorf("plain row must round-trip: got %q, want %q", lines[2], "world")
	}
}
