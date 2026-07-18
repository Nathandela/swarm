package vt

// Failing-first suite for item 4.3's RENDER half (R4.3.2/R4.3.3) — the committee-
// flagged trap. With merged runs, a run can straddle the client's clip edge;
// RenderSnapshotClipped must emit the fitting grapheme-cluster PREFIX of that run
// instead of dropping the whole run. The load-bearing property is byte-for-byte
// parity: the merged projection and the one-run-per-cell projection of the SAME
// grid must render identically at every clip width. These fail against the pre-
// 4.3 whole-run-drop behavior (a merged run clipped mid-run renders nothing).

import (
	"fmt"
	"strings"
	"testing"
)

// styleEq is a test-local style-field comparison (Text/Width excluded). It
// mirrors the producer merge key without importing it, so the parity assertion
// does not lean on the code path it validates.
func styleEq(a, b Run) bool {
	return a.Fg == b.Fg && a.Bg == b.Bg && a.Bold == b.Bold && a.Faint == b.Faint &&
		a.Italic == b.Italic && a.Underline == b.Underline && a.Reverse == b.Reverse
}

// mergeAdjacent coalesces adjacent same-style runs (text concatenated, width
// summed) — the merged projection buildLine now emits.
func mergeAdjacent(cells []Run) []Run {
	var m []Run
	for _, r := range cells {
		if n := len(m); n > 0 && styleEq(m[n-1], r) {
			m[n-1].Text += r.Text
			m[n-1].Width += r.Width
			continue
		}
		m = append(m, r)
	}
	return m
}

// parityCells is one row mixing default and styled cells with wide graphemes and
// a combining cluster, so clipping crosses run boundaries, mid-run cuts, a wide
// grapheme straddling the edge, and a combining cluster at the edge.
func parityCells() []Run {
	red := func(t string, w int) Run { return Run{Text: t, Width: w, Fg: "#ff0000"} }
	return []Run{
		{Text: "a", Width: 1}, {Text: "b", Width: 1}, {Text: "界", Width: 2}, // default, incl wide
		red("c", 1), red("d", 1), red("世", 2), red("é", 1), // red, incl wide + combining
		{Text: "f", Width: 1, Bold: true},           // bold singleton
		{Text: " ", Width: 1}, {Text: " ", Width: 1}, // trailing default blanks
	}
}

// R4.3.3 — the merged and one-run-per-cell projections of the same grid render
// byte-identically at EVERY clip width (0 = unclipped, run boundaries, mid-run,
// mid-wide-grapheme, and beyond the row).
func TestRenderClipped_MergedMatchesUnmergedAcrossWidths(t *testing.T) {
	cells := parityCells()
	total := 0
	for _, r := range cells {
		total += r.Width
	}
	unmerged := &Snap{Version: SnapshotVersion, Cols: total, Rows: 1, CursorVisible: true,
		Lines: []Line{{Runs: cells}}}
	merged := &Snap{Version: SnapshotVersion, Cols: total, Rows: 1, CursorVisible: true,
		Lines: []Line{{Runs: mergeAdjacent(cells)}}}

	if len(merged.Lines[0].Runs) >= len(unmerged.Lines[0].Runs) {
		t.Fatalf("fixture must actually merge, else the parity check is vacuous: %d vs %d runs",
			len(merged.Lines[0].Runs), len(unmerged.Lines[0].Runs))
	}
	for w := 0; w <= total+2; w++ {
		got := string(RenderSnapshotClipped(merged, w, 0))
		want := string(RenderSnapshotClipped(unmerged, w, 0))
		if got != want {
			t.Fatalf("clip width %d: merged render != unmerged render\n merged=%q\nunmerged=%q", w, got, want)
		}
	}
}

// distinctCells is merging's WORST case: every cell a different fg, so no adjacent
// cells coalesce and the merged projection equals the one-run-per-cell projection. It
// includes wide graphemes so intra-run clipping of a lone wide cell is exercised.
func distinctCells() []Run {
	texts := []string{"a", "b", "界", "c", "d", "世", "e", "f", "g", "h"}
	cells := make([]Run, len(texts))
	for i, tx := range texts {
		w := 1
		if tx == "界" || tx == "世" {
			w = 2
		}
		cells[i] = Run{Text: tx, Width: w, Fg: fmt.Sprintf("#%02x0000", 0x10+i*0x10)}
	}
	return cells
}

// R4.3.3 — merged-vs-unmerged byte parity must hold even when merging wins nothing
// (all-distinct styles). mergeAdjacent must leave the run count unchanged and the
// clipped render must stay byte-identical at every width.
func TestRenderClipped_NoMergeWorstCaseParityAcrossWidths(t *testing.T) {
	cells := distinctCells()
	total := 0
	for _, r := range cells {
		total += r.Width
	}
	merged := mergeAdjacent(cells)
	if len(merged) != len(cells) {
		t.Fatalf("distinct-style cells must NOT merge: %d runs from %d cells", len(merged), len(cells))
	}
	unmergedSnap := &Snap{Version: SnapshotVersion, Cols: total, Rows: 1, CursorVisible: true,
		Lines: []Line{{Runs: cells}}}
	mergedSnap := &Snap{Version: SnapshotVersion, Cols: total, Rows: 1, CursorVisible: true,
		Lines: []Line{{Runs: merged}}}
	for w := 0; w <= total+2; w++ {
		got := string(RenderSnapshotClipped(mergedSnap, w, 0))
		want := string(RenderSnapshotClipped(unmergedSnap, w, 0))
		if got != want {
			t.Fatalf("clip width %d: merged != unmerged for no-merge grid\n merged=%q\nunmerged=%q", w, got, want)
		}
	}
}

// R4.3.2 — a wide grapheme straddling the clip edge INSIDE a merged run is
// dropped whole (parity with per-cell behavior), keeping the fitting prefix.
func TestRenderClipped_WideGraphemeInMergedRunDropsWhole(t *testing.T) {
	merged := &Snap{Version: SnapshotVersion, Cols: 3, Rows: 1, CursorVisible: true,
		Lines: []Line{{Runs: []Run{{Text: "c世", Width: 3, Fg: "#ff0000"}}}}}
	out := stripCSI(string(RenderSnapshotClipped(merged, 2, 1)))

	if !strings.Contains(out, "c") {
		t.Fatalf("prefix cell c must render; got %q", out)
	}
	if strings.Contains(out, "世") {
		t.Fatalf("straddling wide grapheme must drop whole; got %q", out)
	}
}

// R4.3.2 — a combining cluster inside a merged run counts as one column and is
// emitted whole when it fits; the cell beyond the clip width is dropped.
func TestRenderClipped_CombiningClusterInMergedRun(t *testing.T) {
	merged := &Snap{Version: SnapshotVersion, Cols: 3, Rows: 1, CursorVisible: true,
		Lines: []Line{{Runs: []Run{{Text: "aéb", Width: 3}}}}}
	out := stripCSI(string(RenderSnapshotClipped(merged, 2, 1)))

	if !strings.Contains(out, "é") {
		t.Fatalf("combining cluster must render whole within the width; got %q", out)
	}
	if strings.Contains(out, "b") {
		t.Fatalf("cell beyond the clip width must be dropped; got %q", out)
	}
}
