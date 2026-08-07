package engine

// agents-tracker-h7s5 (spike-SD F1): live claude 2.1.224 prints the busy hint in
// its bottom STATUS BAR, flanked by U+00B7 middle dots, not inside a parenthesis
// group:
//
//	  ⏸ manual mode on · esc to interrupt · ← 3 agents
//
// The 66dfbe4 anchoring (a parenthesis group opening and closing on the same
// row, derived from codex and pre-2.1.224 claude captures) rejects that row, and
// because claude keeps its ❯ composer rendered while it streams, the claude
// signature fell through to the composer rule and read a working session as
// CONCLUSIVELY idle for the whole turn. Both shapes must count; prose that
// merely quotes the phrase still must not (the fji pins).

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// claudeBusyStreamFixture is the live 15.3s streaming turn (spike-SD run 1):
// prompt, 700 streamed words, Stop, then the idle tail.
const claudeBusyStreamFixture = "../../docs/verification/fixtures/spike-sd/busy-stream.json"

// claudeLiveBusyRow is that capture's status row as the emulator renders it,
// byte-exact: U+23F8 pause glyph, U+00B7 middle dots with one ASCII space on
// each side, U+2190 arrow. When the turn ends the same row reads
// "? for shortcuts" in place of the hint.
const claudeLiveBusyRow = "⏸ manual mode on · esc to interrupt · ← 3 agents"

// captureCols/captureRows are the terminal the spike-SD runs were captured at
// (swarm-char -geometry 100x40).
const captureCols, captureRows = 100, 40

func TestH7S5_LiveClaudeBusyStatusRowReadsActive(t *testing.T) {
	fx, err := fixtureio.LoadFixture(claudeBusyStreamFixture)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	snap, off := feedUntilRow(t, fx.PTYCapture, claudeLiveBusyRow)
	turn, inter, conclusive := evaluateGridSig(snap, sigClaude)
	if turn != status.TurnActive || inter != status.InteractionNone || !conclusive {
		t.Fatalf("live claude busy frame at offset %d (status row %q on screen) read (%s, %s, conclusive=%v); want (active, none, true)",
			off, claudeLiveBusyRow, turn, inter, conclusive)
	}
}

// The over-anchoring guard: at the end of the capture the turn is over, the hint
// is gone from the status row, and the same grid must read idle.
func TestH7S5_LiveClaudeIdleTailStillReadsIdle(t *testing.T) {
	fx, err := fixtureio.LoadFixture(claudeBusyStreamFixture)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	snap := snapOfCapture(t, fx.PTYCapture, captureCols, captureRows)
	if gridContainsRow(snap, escToInterrupt) {
		t.Fatalf("the capture's final frame still carries %q; it is meant to be the idle tail", escToInterrupt)
	}
	turn, inter, conclusive := evaluateGridSig(snap, sigClaude)
	if turn != status.TurnIdle || inter != status.InteractionNone || !conclusive {
		t.Fatalf("live claude idle tail read (%s, %s, conclusive=%v); want (idle, none, true)", turn, inter, conclusive)
	}
}

// TestH7S5_DotFlankedBusyRowShapes pins the rule one row at a time: the hint
// counts when a " · " separator abuts it on either side, and a middle dot
// elsewhere on a prose row does not make prose busy.
func TestH7S5_DotFlankedBusyRowShapes(t *testing.T) {
	busy := []string{
		"  " + claudeLiveBusyRow,
		"  ⏸ manual mode on · esc to interrupt", // the hint closing the bar
		"  esc to interrupt · ← 3 agents",       // the hint opening it
	}
	prose := []string{
		"  The bar reads manual mode on · ? for shortcuts, and esc to interrupt while working.",
		"  esc to interrupt is the hint · as documented in heuristic.go",
	}
	for _, row := range busy {
		if !hasBusyMarker(snapFromLines(100, 0, 0, false, []string{row})) {
			t.Errorf("status row %q read as not busy; want busy", row)
		}
	}
	for _, row := range prose {
		if hasBusyMarker(snapFromLines(100, 0, 0, false, []string{row})) {
			t.Errorf("prose row %q read as busy; want not busy", row)
		}
	}
}

// feedUntilRow feeds capture into one fresh emulator at the capture's own
// geometry, in 256-byte steps, and returns the first snapshot carrying want
// together with the byte offset fed to reach it.
func feedUntilRow(t *testing.T, capture []byte, want string) (*vt.Snap, int) {
	t.Helper()
	emu := vt.NewEmulator(captureCols, captureRows)
	defer emu.Close()
	const step = 256
	for off := 0; off < len(capture); off += step {
		end := off + step
		if end > len(capture) {
			end = len(capture)
		}
		emu.Feed(capture[off:end])
		snap := decodeSnap(t, emu)
		if gridContainsRow(snap, want) {
			return snap, end
		}
	}
	t.Fatalf("replaying the whole capture never put %q on the grid", want)
	return nil, 0
}

// gridContainsRow reports whether any row of snap contains want. It is the
// test's own scan, so the window it selects does not depend on the production
// region logic under test.
func gridContainsRow(snap *vt.Snap, want string) bool {
	for _, ln := range snap.Lines {
		if strings.Contains(lineText(ln), want) {
			return true
		}
	}
	return false
}
