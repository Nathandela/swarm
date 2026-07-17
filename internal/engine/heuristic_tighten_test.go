package engine

// F5 (E10.8): tighten the spinner heuristic so it does not read activity into
// ordinary content. The frozen fixtures already pin the true positives (a leading
// braille spinner "⠋ Working" -> active); these add the NEGATIVES the audit flagged
// — a markdown table row and a braille rune buried in prose must NOT be read as
// active — plus a couple of positives (trailing braille, a lone ASCII frame) to
// pin that tightening did not over-correct. evaluateGrid is exercised directly:
// the classification is a pure function of the grid.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

func TestSpinnerHeuristicTightened(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		turn  status.Turn
		inter status.Interaction
	}{
		// Negatives (the F5 false-actives).
		{"markdown-table-row", "| col | value |", status.TurnUnknown, status.InteractionUnknown},
		{"markdown-table-divider", "|-----|-------|", status.TurnUnknown, status.InteractionUnknown},
		{"braille-mid-prose", "see the ⠿ glyph in this text", status.TurnUnknown, status.InteractionUnknown},
		{"ascii-rule", "-----------", status.TurnUnknown, status.InteractionUnknown},

		// Positives (must survive the tightening).
		{"leading-braille-spinner", "⠋ Working", status.TurnActive, status.InteractionNone},
		{"trailing-braille-spinner", "Working ⠸", status.TurnActive, status.InteractionNone},
		{"lone-ascii-frame", "/", status.TurnActive, status.InteractionNone},
		{"leading-ascii-spinner", "/ Working", status.TurnActive, status.InteractionNone},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// A parked cursor after the line so a sentinel case (none here) is not the
			// variable under test; only the spinner/prose classification matters.
			snap := snapFromLines(40, 0, 0, false, []string{tc.line})
			turn, inter := evaluateGrid(snap)
			if turn != tc.turn || inter != tc.inter {
				t.Fatalf("evaluateGrid(%q) = (%s, %s), want (%s, %s)",
					tc.line, turn, inter, tc.turn, tc.inter)
			}
		})
	}
}
