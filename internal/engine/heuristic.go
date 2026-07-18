package engine

// The grid heuristic (E10.8, T-3/T-4). evaluateGrid is the generic, CLI-agnostic
// reader: it reads the LAST line of visible content and classifies it
// deterministically:
//
//   - a trailing braille/ASCII spinner glyph -> active (the near-universal
//     "working" animation),
//   - a settled trailing prompt sentinel with a parked, visible cursor ->
//     idle/none,
//   - anything else (prose, blank) -> unknown (T-4: never a confident guess).
//
// Per-adapter grid signatures (ADR-007) extend this: an adapter declares which
// signature the engine should apply via its heuristic SignalSource
// (Descriptor["grid"] = "codex"|"claude"|...), and evaluateGridSig dispatches to
// the matching reader. A signature reads a BOUNDED bottom region rather than only
// the last line, because a real agent screen renders its model/status FOOTER below
// the composer, so the last line is the footer, not the prompt. Every reader is a
// pure function of the snapshot (stable, no flip-flop) and reports whether its
// reading is CONCLUSIVE: an inconclusive read is absence of evidence, and the
// engine preserves the prior status rather than committing unknown (ADR-007).

import (
	"strings"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// promptSentinels are the trailing glyphs that mark a settled input prompt. It
// includes U+203A '›' (ADR-007) so a generic last-line reader also settles on the
// codex-style composer marker when it IS the last line.
const promptSentinels = ">$#%❯»›"

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

// Grid signature names an adapter declares in its heuristic SignalSource
// (Descriptor["grid"]) to select how the engine reads its screen (ADR-007). Any
// other value (including the historical "prompt-marker") is the generic reader.
const (
	sigCodex  = "codex"
	sigClaude = "claude"
)

// gridRegionRows bounds the per-adapter multi-line scan to the last N non-blank
// rows, so the 200ms output tap stays cheap while still reaching the composer /
// footer cluster at the bottom of a tall, mostly-blank grid.
const gridRegionRows = 12

// composerMarkers are the leading glyphs of an agent's input composer prompt:
// ASCII '>', U+203A '›' (codex), U+276F '❯' (claude).
const composerMarkers = ">›❯"

// escToInterrupt is the near-universal "a turn is running" hint both codex and
// claude print in their status region while working.
const escToInterrupt = "esc to interrupt"

// evaluateGridSig reads snap under the named per-adapter grid signature, returning
// (turn, interaction, conclusive). A conclusive read (active or idle) is applied
// by the engine; an inconclusive one (conclusive=false) is preserved (ADR-007).
// The generic reader is conclusive exactly when it did not fall back to unknown.
func evaluateGridSig(snap *vt.Snap, sig string) (status.Turn, status.Interaction, bool) {
	switch sig {
	case sigCodex:
		return evaluateCodexGrid(snap)
	case sigClaude:
		return evaluateClaudeGrid(snap)
	default:
		turn, inter := evaluateGrid(snap)
		return turn, inter, turn != status.TurnUnknown
	}
}

// evaluateCodexGrid reads Codex's real screen (q65). Codex has no typed signal in
// v1 (D1), so the grid is its SOLE driver and its idle screen MUST read idle, not
// inconclusive. A busy marker anywhere in the bottom region is active; a composer
// prompt on the parked-cursor row with no busy marker is idle; else inconclusive.
func evaluateCodexGrid(snap *vt.Snap) (status.Turn, status.Interaction, bool) {
	if snap == nil {
		return status.TurnUnknown, status.InteractionUnknown, false
	}
	if hasBusyMarker(snap) {
		return status.TurnActive, status.InteractionNone, true
	}
	if composerOnCursorRow(snap) {
		return status.TurnIdle, status.InteractionNone, true
	}
	return status.TurnUnknown, status.InteractionUnknown, false
}

// evaluateClaudeGrid reads Claude's real screen (dqh). A busy marker is active; a
// composer prompt anywhere in the bottom region with no busy marker is idle — it
// does not require the cursor on the composer row, since Claude's idle footer
// ("Brewed for Ns") renders below the composer box; else inconclusive.
func evaluateClaudeGrid(snap *vt.Snap) (status.Turn, status.Interaction, bool) {
	if snap == nil {
		return status.TurnUnknown, status.InteractionUnknown, false
	}
	if hasBusyMarker(snap) {
		return status.TurnActive, status.InteractionNone, true
	}
	if composerInRegion(snap) {
		return status.TurnIdle, status.InteractionNone, true
	}
	return status.TurnUnknown, status.InteractionUnknown, false
}

// hasBusyMarker reports whether the bottom region carries a "turn is running"
// signal: the literal "esc to interrupt" hint or a braille spinner glyph.
func hasBusyMarker(snap *vt.Snap) bool {
	last, _, ok := lastContentLine(snap)
	if !ok {
		return false
	}
	for y := last; y >= 0 && y > last-gridRegionRows; y-- {
		t := lineText(snap.Lines[y])
		if strings.Contains(t, escToInterrupt) {
			return true
		}
		for _, r := range t {
			if isBraille(r) {
				return true
			}
		}
	}
	return false
}

// composerInRegion reports whether any row in the bottom region begins with a
// composer prompt marker.
func composerInRegion(snap *vt.Snap) bool {
	last, _, ok := lastContentLine(snap)
	if !ok {
		return false
	}
	for y := last; y >= 0 && y > last-gridRegionRows; y-- {
		if startsWithComposer(lineText(snap.Lines[y])) {
			return true
		}
	}
	return false
}

// composerOnCursorRow reports whether the visible cursor rests on a row whose
// first non-blank glyph is a composer prompt marker (the settled "waiting for
// input" posture).
func composerOnCursorRow(snap *vt.Snap) bool {
	if !snap.CursorVisible || snap.CursorY < 0 || snap.CursorY >= len(snap.Lines) {
		return false
	}
	return startsWithComposer(lineText(snap.Lines[snap.CursorY]))
}

// startsWithComposer reports whether text's first non-blank glyph is a composer
// prompt marker.
func startsWithComposer(text string) bool {
	for _, r := range text {
		if r == ' ' || r == '\t' {
			continue
		}
		return strings.ContainsRune(composerMarkers, r)
	}
	return false
}
