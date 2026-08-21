package vt

// WAVE R8 / ROUND 2 -- "UNDECLARED" MUST NOT READ AS "UNLIMITED", ON THE ROW COUNT TOO.
//
// ROUND-2 MINOR 12. `SnapText`'s row-count clause read `if s.Rows > 0 && rows > s.Rows`, so
// a Snap declaring `Rows = 0` with fifty thousand lines emitted all fifty thousand -- while
// nineteen lines below, `snapRowByteCap` handles the IDENTICAL undeclared case explicitly and
// states the reason in its own comment: "A Snap that declares no geometry at all still gets a
// ceiling -- 'undeclared' must not read as 'unlimited'." The row-COUNT path broke the rule
// the same file states.
//
// IT IS REACHABLE IN PRINCIPLE from renderInitialView, which decodes ANOTHER PROCESS's
// initial snapshot. `clipPeek` bounds it on today's legacy wire, which is why the review
// ranked it minor -- and that mitigation disappears the moment the versioned TerminalViewV1
// body lands, which is this wave's own direction of travel.

import (
	"strings"
	"testing"
)

// snapWithLines builds a Snap of n single-run rows under a declared geometry.
func snapWithLines(cols, rows, n int) *Snap {
	s := &Snap{Cols: cols, Rows: rows, Lines: make([]Line, n)}
	for i := range s.Lines {
		s.Lines[i] = Line{Runs: []Run{{Text: "x"}}}
	}
	return s
}

// TestR8RowCap_AnUndeclaredRowCountIsStillBounded is the defect, driven.
func TestR8RowCap_AnUndeclaredRowCountIsStillBounded(t *testing.T) {
	const supplied = 50_000
	got := SnapText(snapWithLines(80, 0, supplied))
	if len(got) == supplied {
		t.Fatalf("a Snap declaring Rows=0 with %d lines rendered ALL %d rows. `snapRowByteCap` "+
			"nineteen lines away states the rule this path broke: undeclared must not read as "+
			"unlimited. The phone allocates against this, from a snapshot decoded out of another "+
			"process's bytes.", supplied, len(got))
	}
	if len(got) > snapDefaultRowCeiling {
		t.Fatalf("an undeclared row count rendered %d rows, above the %d ceiling",
			len(got), snapDefaultRowCeiling)
	}
	if len(got) == 0 {
		t.Fatalf("the clamp emitted nothing; a ceiling is not a blank")
	}
}

// TestR8RowCap_AnAbsurdDeclaredRowCountIsAlsoBounded is the same rule for a producer that
// declares a hostile geometry rather than none. A declared count is believed only where it is
// LOWER than the ceiling -- exactly clampBound's rule one field over, and for the same reason:
// the producer does not get to raise the reader's own limit.
func TestR8RowCap_AnAbsurdDeclaredRowCountIsAlsoBounded(t *testing.T) {
	got := SnapText(snapWithLines(80, 1_000_000, 50_000))
	if len(got) > snapDefaultRowCeiling {
		t.Fatalf("a Snap DECLARING a million rows rendered %d; a declared geometry is believed "+
			"only where it is lower than the reader's own ceiling", len(got))
	}
}

// TestR8RowCap_ADeclaredGeometryStillClipsBelowTheCeiling is the vacuity guard: the new
// ceiling must not have replaced the declared clip, which is the property the original
// clause was there for.
func TestR8RowCap_ADeclaredGeometryStillClipsBelowTheCeiling(t *testing.T) {
	got := SnapText(snapWithLines(80, 24, 500))
	if len(got) != 24 {
		t.Fatalf("a Snap declaring 24 rows with 500 lines rendered %d rows, want 24", len(got))
	}
}

// TestR8RowCap_AShortGridIsNotPaddedToTheCeiling pins the other direction: the clamp is a
// ceiling and never a floor. A grid padded to a thousand blank rows would be a denial of
// service the clamp itself introduced.
func TestR8RowCap_AShortGridIsNotPaddedToTheCeiling(t *testing.T) {
	got := SnapText(snapWithLines(80, 24, 3))
	if len(got) != 3 {
		t.Fatalf("a 3-line Snap rendered %d rows, want 3", len(got))
	}
	if strings.Join(got, "") != "xxx" {
		t.Fatalf("the rows themselves changed: %q", got)
	}
}
