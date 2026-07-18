package engine

// R-C1/R-C2 (Phase C): descriptor-driven grid rules. An adapter declares
// "heuristic" SignalSources carrying Descriptor{"grid": <rule name>, "value":
// <v>}; RegisterSession parses these ONCE (parseGridRules) into an immutable
// gridRules struct — copying the descriptor strings out, so a caller that
// retains and later mutates its SignalSource slice or Descriptor maps cannot
// change a session's evaluation after registration. OnOutput evaluates via
// evaluateGridWithRules, whose precedence is busy-contains > idle-line-equals >
// the generic evaluateGrid fallback (heuristic.go); "prompt-marker" is
// declarative-only — the generic evaluator always runs as that fallback
// unconditionally, so declaring it changes nothing.
//
// Both rule kinds scan "content lines": grid rows that are non-blank after
// right-trimming trailing spaces, scanned upward from the bottom of the grid —
// exactly like heuristic.go's lastContentLine, so blank rows are skipped and a
// short grid simply yields fewer content lines. Content-index 0 is the
// bottommost content line; content-index i's "next line below" is
// content-index i-1.

import (
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// bottomContentLineBudget / idleWindowBudget bound how many bottom content
// lines each rule kind scans (R-C1).
const (
	bottomContentLineBudget = 6
	idleWindowBudget        = 3
)

// borderRuneSet are the box-drawing/rule glyphs a border line is drawn from
// (R-C1's idle-line-equals corroboration).
const borderRuneSet = "─━═╌╍-_▀▄╹╺╸╻│┃┌┐└┘├┤"

// gridRules is a session's declared grid rules, parsed ONCE at RegisterSession
// from its adapter's heuristic SignalSources (R-C2) and never mutated again —
// an immutable value copied out of the caller's descriptor maps, so later
// mutation of those maps or the originating slice has no effect. The zero
// value (no rules of either kind) is valid and total: evaluateGridWithRules
// then always defers to the generic fallback.
type gridRules struct {
	busy []string // busy-contains values, declaration order
	idle []string // idle-line-equals values, declaration order
}

// parseGridRules extracts busy-contains / idle-line-equals rules from an
// adapter's declared SignalSources. Only Kind "heuristic" entries are
// considered; an empty value, an unrecognized "grid" name (including
// "prompt-marker", which is declarative-only), or a non-heuristic source is
// ignored. It is pure and total: nil sources, a nil Descriptor, or missing
// keys never panic and simply contribute nothing. Every value is copied out of
// the source map into a fresh slice, so a caller-retained reference to sources
// or its Descriptor maps cannot affect the result after this call returns.
func parseGridRules(sources []adapter.SignalSource) gridRules {
	var r gridRules
	for _, src := range sources {
		if src.Kind != "heuristic" {
			continue
		}
		value := src.Descriptor["value"]
		if value == "" {
			continue
		}
		switch src.Descriptor["grid"] {
		case "busy-contains":
			r.busy = append(r.busy, value)
		case "idle-line-equals":
			r.idle = append(r.idle, value)
		}
	}
	return r
}

// evaluateGridWithRules classifies snap using rules' precedence: busy-contains
// > idle-line-equals > the generic evaluateGrid fallback. It is pure and
// total: a nil snap or a zero-value rules is safe and yields the generic
// verdict (evaluateGrid's own nil handling).
func evaluateGridWithRules(snap *vt.Snap, rules gridRules) (status.Turn, status.Interaction) {
	lines := bottomContentLines(snap, bottomContentLineBudget)
	if busyContainsMatch(lines, rules.busy) {
		return status.TurnActive, status.InteractionNone
	}
	if !containsBraille(lines) {
		limit := idleWindowBudget
		if len(lines) < limit {
			limit = len(lines)
		}
		if idx, ok := idleLineMatch(lines[:limit], rules.idle); ok && idx > 0 && isBorderLine(lines[idx-1]) {
			return status.TurnIdle, status.InteractionNone
		}
	}
	return evaluateGrid(snap)
}

// bottomContentLines returns up to k content lines (non-blank after
// right-trim, scanned upward from the grid's last row, blank rows skipped —
// the same discipline as lastContentLine), ordered content-index 0
// (bottommost) upward. A nil snap or an all-blank grid yields nil.
func bottomContentLines(snap *vt.Snap, k int) []string {
	if snap == nil {
		return nil
	}
	lines := make([]string, 0, k)
	for y := len(snap.Lines) - 1; y >= 0 && len(lines) < k; y-- {
		t := strings.TrimRight(lineText(snap.Lines[y]), " ")
		if t != "" {
			lines = append(lines, t)
		}
	}
	return lines
}

// busyContainsMatch reports whether any line contains any non-empty value as a
// substring.
func busyContainsMatch(lines, values []string) bool {
	for _, l := range lines {
		for _, v := range values {
			if v != "" && strings.Contains(l, v) {
				return true
			}
		}
	}
	return false
}

// idleLineMatch scans lines (already limited to the idle window) for the
// first content-index whose TrimSpace'd text equals any non-empty value,
// reporting that index. Declaration order among values does not change the
// result: every line is checked against every value, and the verdict (idle)
// is identical regardless of which specific value matched.
func idleLineMatch(lines, values []string) (idx int, ok bool) {
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		for _, v := range values {
			if v != "" && trimmed == v {
				return i, true
			}
		}
	}
	return 0, false
}

// containsBraille reports whether any line carries a braille spinner rune
// (U+2800..U+28FF) anywhere in its text — the idle-line-equals suppressor
// (R-C1's defense in depth).
// containsBraille reuses the isBraille range U+2800..U+28FF deliberately —
// one rune wider at the low end than the plan's U+2801 floor: U+2800 is the
// blank braille frame some spinners emit, and a broader suppressor only ever
// fails toward unknown, never toward a false idle.
func containsBraille(lines []string) bool {
	for _, l := range lines {
		for _, r := range l {
			if isBraille(r) {
				return true
			}
		}
	}
	return false
}

// isBorderLine reports whether text is a box-border line: at least 3 runes
// total, with at least 80% of its non-space runes drawn from borderRuneSet. An
// all-space or empty text is never a border line.
func isBorderLine(text string) bool {
	runes := []rune(text)
	if len(runes) < 3 {
		return false
	}
	nonSpace, border := 0, 0
	for _, r := range runes {
		if r == ' ' {
			continue
		}
		nonSpace++
		if strings.ContainsRune(borderRuneSet, r) {
			border++
		}
	}
	if nonSpace == 0 {
		return false
	}
	return float64(border)/float64(nonSpace) >= 0.8
}
