package engine

// The generic, CLI-agnostic grid heuristic (E10.8, T-3/T-4). It reads the LAST
// line of visible content and classifies it deterministically:
//
//   - a trailing braille/ASCII spinner glyph -> active (the near-universal
//     "working" animation),
//   - a settled trailing prompt sentinel with a parked, visible cursor ->
//     idle/none,
//   - anything else (prose, blank) -> unknown (T-4: never a confident guess).
//
// It is intentionally minimal and CLI-independent: per-CLI grid rules are Epic 11
// adapter work. The classification is a pure function of the snapshot, so
// re-evaluating the same grid is stable (no flip-flop).

import (
	"strings"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// promptSentinels are the trailing glyphs that mark a settled input prompt.
const promptSentinels = ">$#%❯»"

// asciiSpinnerFrames are the classic single-character spinner frames. They count
// as a spinner only as an isolated leading token ("/ Working"), so ordinary prose
// or a markdown rule is not misread as activity.
const asciiSpinnerFrames = `|/-\`

// evaluateGrid classifies a snapshot into (turn, interaction). An inconclusive or
// empty grid maps to (unknown, unknown) — the humble reading (T-4).
func evaluateGrid(snap *vt.Snap) (status.Turn, status.Interaction) {
	if snap == nil {
		return status.TurnUnknown, status.InteractionUnknown
	}
	idx, text, ok := lastContentLine(snap)
	if !ok {
		return status.TurnUnknown, status.InteractionUnknown // blank grid
	}
	if hasSpinner(text) {
		return status.TurnActive, status.InteractionNone
	}
	if endsWithSentinel(text) && cursorParked(snap, idx) {
		return status.TurnIdle, status.InteractionNone
	}
	return status.TurnUnknown, status.InteractionUnknown
}

// lastContentLine returns the index and trailing-trimmed text of the last grid
// row that carries any non-blank content. ok is false for an all-blank grid.
func lastContentLine(snap *vt.Snap) (idx int, text string, ok bool) {
	for y := len(snap.Lines) - 1; y >= 0; y-- {
		t := strings.TrimRight(lineText(snap.Lines[y]), " ")
		if t != "" {
			return y, t, true
		}
	}
	return 0, "", false
}

// lineText concatenates a row's per-cell runs back into its text.
func lineText(line vt.Line) string {
	var b strings.Builder
	for _, r := range line.Runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

// hasSpinner reports whether text carries a spinner glyph in an ANIMATION
// position, so ordinary content is not misread as activity. A braille pattern
// (U+2800..U+28FF, the dominant modern spinner) counts only as the LEADING or
// TRAILING glyph of the line — the "⠋ Working" / "Working ⠋" posture — never a
// braille rune buried mid-prose. A classic ASCII frame (|/-\) counts only as a
// lone leading animation token: the whole line, or the frame followed by a space
// with no further occurrence of that same frame, so a "| a | b |" markdown table
// row or a "----" rule never trips it.
func hasSpinner(text string) bool {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return false
	}
	if isBraille(runes[0]) || isBraille(runes[len(runes)-1]) {
		return true
	}
	first := runes[0]
	if strings.ContainsRune(asciiSpinnerFrames, first) {
		if len(runes) == 1 {
			return true
		}
		if runes[1] == ' ' && !strings.ContainsRune(string(runes[1:]), first) {
			return true
		}
	}
	return false
}

// isBraille reports whether r is a braille pattern glyph (U+2800..U+28FF), the
// near-universal modern spinner animation frame.
func isBraille(r rune) bool { return r >= 0x2800 && r <= 0x28FF }

// endsWithSentinel reports whether text ends with a settled prompt sentinel.
func endsWithSentinel(text string) bool {
	r := []rune(text)
	if len(r) == 0 {
		return false
	}
	return strings.ContainsRune(promptSentinels, r[len(r)-1])
}

// cursorParked reports whether the cursor is visibly resting on row idx — the
// "settled, waiting for input" posture that distinguishes an idle prompt from a
// prompt merely scrolled into view.
func cursorParked(snap *vt.Snap, idx int) bool {
	return snap.CursorVisible && snap.CursorY == idx
}
