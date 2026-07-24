package vt

// Failing-first suite for item 4.3 (agents-tracker-ut0) snapshot run-merging:
// buildLine coalesces adjacent cells that share every style field into one Run
// (R4.3.1), while the reconstructed line text the engine heuristic and the
// adapters read is unchanged (R4.3.4). These fail against the pre-4.3 one-run-
// per-cell projection (a styled row yields Cols runs, not the merged count).

import (
	"strings"
	"testing"
)

// A run of same-style cells collapses to ONE run: text concatenated, widths
// summed. "AAA" (red) then "BBB" + trailing blanks (default) is two runs.
func TestBuildLine_MergesAdjacentSameStyleCells(t *testing.T) {
	e := NewEmulator(10, 1)
	defer e.Close()
	e.Feed([]byte("\x1b[31mAAA\x1b[0mBBB"))
	s := snapshotDecode(t, e)
	runs := s.Lines[0].Runs

	if len(runs) != 2 {
		t.Fatalf("want 2 merged runs, got %d: %+v", len(runs), runs)
	}
	if runs[0].Text != "AAA" || runs[0].Width != 3 || runs[0].Fg == "" {
		t.Errorf("run0 = %+v, want red \"AAA\" width 3", runs[0])
	}
	if !strings.HasPrefix(runs[1].Text, "BBB") || runs[1].Fg != "" {
		t.Errorf("run1 = %+v, want default text starting \"BBB\"", runs[1])
	}
	if w := runs[0].Width + runs[1].Width; w != 10 {
		t.Errorf("row width = %d, want 10 (width invariant preserved under merge)", w)
	}
}

// Cells with differing style are NEVER merged: red 'A' and green 'B' stay two
// distinct runs (plus the trailing default blank run).
func TestBuildLine_DoesNotMergeDifferentStyles(t *testing.T) {
	e := NewEmulator(10, 1)
	defer e.Close()
	e.Feed([]byte("\x1b[31mA\x1b[32mB\x1b[0m"))
	s := snapshotDecode(t, e)
	runs := s.Lines[0].Runs

	if len(runs) != 3 {
		t.Fatalf("want 3 runs (distinct styles unmerged + blanks), got %d: %+v", len(runs), runs)
	}
	if runs[0].Text != "A" || runs[1].Text != "B" {
		t.Errorf("runs = %q,%q, want A,B", runs[0].Text, runs[1].Text)
	}
	if runs[0].Fg == "" || runs[1].Fg == "" || runs[0].Fg == runs[1].Fg {
		t.Errorf("red/green must be distinct non-empty Fg, got %q,%q", runs[0].Fg, runs[1].Fg)
	}
}

// Adjacent wide (2-cell) graphemes of the same style merge with their widths
// summed and each grapheme kept once; the spacer cells stay skipped (R4.3.1
// continuation preservation) so the row width invariant still holds.
func TestBuildLine_MergesWideGraphemesPreservingWidth(t *testing.T) {
	e := NewEmulator(10, 1)
	defer e.Close()
	e.Feed([]byte("\x1b[31m世界\x1b[0m"))
	s := snapshotDecode(t, e)
	runs := s.Lines[0].Runs

	if len(runs) != 2 {
		t.Fatalf("want 2 runs (merged wide pair + blanks), got %d: %+v", len(runs), runs)
	}
	if runs[0].Text != "世界" || runs[0].Width != 4 {
		t.Errorf("run0 = %+v, want \"世界\" width 4 (two wide graphemes merged)", runs[0])
	}
	if total := runs[0].Width + runs[1].Width; total != 10 {
		t.Errorf("row width = %d, want 10", total)
	}
}

// R4.3.4: the per-line text the engine heuristic (heuristic.go:lineText) and the
// codex/claude adapters reconstruct by concatenating Run.Text is unchanged by
// merging. This guards the consumers that read snapshot text.
func TestBuildLine_MergedTextReconstructsLine(t *testing.T) {
	e := NewEmulator(12, 1)
	defer e.Close()
	e.Feed([]byte("\x1b[31mfoo\x1b[0m \x1b[1mbar\x1b[0m"))
	s := snapshotDecode(t, e)

	if got := strings.TrimRight(lineText(s.Lines[0]), " "); got != "foo bar" {
		t.Errorf("reconstructed line = %q, want %q", got, "foo bar")
	}
}
