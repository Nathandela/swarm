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
	sigHermes = "hermes"
)

// gridRegionRows bounds the per-adapter multi-line scan to the last N non-blank
// rows, so the 200ms output tap stays cheap while still reaching the composer /
// footer cluster at the bottom of a tall, mostly-blank grid.
const gridRegionRows = 12

// composerMarkers are the leading glyphs of an agent's input composer prompt:
// ASCII '>', U+203A '›' (codex), U+276F '❯' (claude).
const composerMarkers = ">›❯"

// escToInterrupt is the near-universal "a turn is running" hint both codex and
// claude print in their status region while working. It counts only in the shape
// a real status row renders it (busyHintOnRow), never as a bare substring.
const escToInterrupt = "esc to interrupt"

// codexApprovalHint is the standing footer of Codex's modal approval dialog
// ("Would you like to run the following command?" and kin). Codex hides the
// cursor and drops the busy hint while the dialog is up, so without this marker
// the frame reads inconclusive and ADR-007 preserves "working" for the whole
// wait. When the hint is visible, Codex is waiting on the user's decision.
const codexApprovalHint = "Press enter to confirm or esc to cancel"

// claudeDialogHints are the standing rows of Claude's modal dialogs, each from a
// live capture. Every one of these dialogs spells its selected option with the
// composer glyph ("❯ 1. Yes", "❯ 1. Red") — the same U+276F the idle composer
// uses — so without its marker the frame reads as a settled composer and the
// session shows ready_for_review while it is blocked on the human.
//
//   - the tool-approval dialog's help line ("Do you want to proceed?" with its
//     numbered options), docs/verification/fixtures/spike-sc/c2-interactive-check;
//   - the AskUserQuestion dialog's help row (spike-SD F3, fixtures/spike-sd/
//     askuser-wait.json), whose full row ends "· Esc to cancel"; the prefix
//     anchored here is the part that names the dialog's own navigation;
//   - the plan-approval dialog's feedback affordance (spike-SD F4,
//     fixtures/spike-sd/plan-wait.json), which stands under the numbered options
//     of "Claude has written up a plan ... Would you like to proceed?".
//
// The middle dots are U+00B7 and the arrows U+2191/U+2193, byte-exact from the
// captures.
var claudeDialogHints = []string{
	"Esc to cancel · Tab to amend",
	"Enter to select · ↑/↓ to navigate",
	"shift+tab to approve",
}

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
	case sigHermes:
		return evaluateHermesGrid(snap)
	default:
		turn, inter := evaluateGrid(snap)
		return turn, inter, turn != status.TurnUnknown
	}
}

// Hermes classic-CLI chrome markers, characterized against Hermes Agent 0.20.6.
// They are never sufficient as bare substrings: agent output is untrusted and
// may quote any of them. The helpers below require the surrounding navigation,
// status-row, or composer-border shape that Hermes itself renders.
const (
	hermesApprovalNavigation = "↑/↓ to select, Enter to confirm"
	hermesSlashNavigation    = "type 1/2/3, or ↑/↓ to select, Enter to confirm"
	hermesClarifyNavigation  = "↑/↓ to select, Enter to lock, Tab next question"
	hermesFreeTextNavigation = "type your answer and press Enter"
	hermesBusyPlaceholder    = "msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel"
	hermesCommandHint        = "command in progress · "
	hermesPromptSymbols      = "❯>$#›»→"
	hermesCommandSpinners    = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	hermesCompactWidth       = 64
)

// evaluateHermesGrid reads Hermes Agent's classic prompt_toolkit interface.
// Modal navigation rows outrank all other chrome because a dialog can coexist
// with a stale busy row and its composer uses the same arrow marker as ordinary
// idle input. A structurally anchored status row is active; a composer in the
// source-defined top/lower rule geometry is idle. Anything clipped or mid-redraw
// is inconclusive, allowing ADR-007 to preserve the committed state instead of
// guessing idle.
func evaluateHermesGrid(snap *vt.Snap) (status.Turn, status.Interaction, bool) {
	if snap == nil {
		return status.TurnUnknown, status.InteractionUnknown, false
	}
	if hermesModalInRegion(snap, hermesApprovalNavigation, "⚠") ||
		hermesModalInRegion(snap, hermesSlashNavigation, "⚠") {
		return status.TurnIdle, status.InteractionPermission, true
	}
	if hermesModalInRegion(snap, hermesClarifyNavigation, "?") ||
		hermesModalInRegion(snap, hermesApprovalNavigation, "?") ||
		hermesModalInRegion(snap, hermesFreeTextNavigation, "✎") {
		return status.TurnIdle, status.InteractionPrompt, true
	}
	if hermesCommandInRegion(snap) {
		return status.TurnActive, status.InteractionNone, true
	}
	if hermesBusyInRegion(snap) {
		return status.TurnActive, status.InteractionNone, true
	}
	if hermesBorderedComposerInRegion(snap) {
		return status.TurnIdle, status.InteractionNone, true
	}
	return status.TurnUnknown, status.InteractionUnknown, false
}

// hermesModalInRegion recognizes a modal only when its exact navigation row is
// followed by the matching state composer in terminal input-rule geometry. The
// title may scroll off a short screen, so it is not required. Conversely,
// requiring the composer prevents model output that reproduces the complete
// navigation line from manufacturing a human-input state above an ordinary
// idle prompt.
func hermesModalInRegion(snap *vt.Snap, navigation, stateIcon string) bool {
	last, _, ok := lastContentLine(snap)
	if !ok {
		return false
	}
	first := last - gridRegionRows + 1
	if first < 0 {
		first = 0
	}
	for y := first; y <= last; y++ {
		row := strings.TrimSpace(lineText(snap.Lines[y]))
		if hermesNavigationRow(row, navigation) {
			return hermesComposerBetween(snap, y+1, last, func(candidate string, cols int) bool {
				return hermesStateComposerRow(candidate, cols, stateIcon)
			})
		}
	}
	return false
}

// hermesBusyInRegion recognizes Hermes's live bottom status row. Both the
// caduceus/composer prefix and source placeholder are required so a transcript
// sentence containing the shortcut does not manufacture activity. Compact
// chrome can expose only the caduceus, or a viewport-clipped placeholder. Wide
// chrome has top and lower rules; compact chrome retains only the top rule.
func hermesBusyInRegion(snap *vt.Snap) bool {
	last, _, ok := lastContentLine(snap)
	if !ok {
		return false
	}
	first := last - gridRegionRows + 1
	if first < 0 {
		first = 0
	}
	for y := first; y <= last; y++ {
		if hermesAgentBusyRow(lineText(snap.Lines[y]), snap.Cols) &&
			hermesTerminalComposerChrome(snap, y, last) {
			return true
		}
	}
	return false
}

// hermesCommandInRegion recognizes the separate synchronous-command state.
// Both its exact hint row and the spinner-prefixed composer are needed: a
// braille rune alone is common in transcript output and generic animations.
func hermesCommandInRegion(snap *vt.Snap) bool {
	last, _, ok := lastContentLine(snap)
	if !ok {
		return false
	}
	first := last - gridRegionRows + 1
	if first < 0 {
		first = 0
	}
	for y := first; y <= last; y++ {
		if !hermesCommandNavigationRow(lineText(snap.Lines[y]), snap.Cols < hermesCompactWidth) {
			continue
		}
		if hermesComposerBetween(snap, y+1, last, hermesCommandComposerRow) {
			return true
		}
	}
	return false
}

// hermesBorderedComposerInRegion recognizes the settled prompt_toolkit composer:
// a prompt row carrying any upstream-supported arrow suffix. The ordinary
// prompt may be bare or carry a valid profile prefix ("coder ❯"). State
// composers are handled separately with their navigation/status rows.
func hermesBorderedComposerInRegion(snap *vt.Snap) bool {
	last, _, ok := lastContentLine(snap)
	if !ok {
		return false
	}
	first := last - gridRegionRows + 1
	if first < 0 {
		first = 0
	}
	return hermesComposerBetween(snap, first, last, func(row string, _ int) bool {
		return hermesComposerRow(row)
	})
}

func hermesComposerBetween(snap *vt.Snap, first, last int, matches func(string, int) bool) bool {
	for y := first; y <= last; y++ {
		if matches(lineText(snap.Lines[y]), snap.Cols) && hermesTerminalComposerChrome(snap, y, last) {
			return true
		}
	}
	return false
}

// hermesTerminalComposerChrome validates the prompt_toolkit layout, including
// its responsive breakpoint. The top separator is always present. At widths
// below 64 columns Hermes hides the lower separator, so the composer itself is
// the final nonblank row. On a wide screen one or more lower-rule fragments may
// exist during a redraw, but every remaining nonblank row must be a rule. This
// terminal constraint prevents a complete-looking quoted state plus Markdown
// rule higher in agent output from overriding the real composer below it.
func hermesTerminalComposerChrome(snap *vt.Snap, row, last int) bool {
	if !hermesPreviousContentIsBorder(snap, row) {
		return false
	}
	if snap.Cols < hermesCompactWidth {
		return row == last
	}
	if row >= last {
		return false
	}
	found := false
	for below := row + 1; below <= last; below++ {
		candidate := strings.TrimSpace(lineText(snap.Lines[below]))
		if candidate == "" {
			continue
		}
		if !hermesHorizontalBorder(candidate) {
			return false
		}
		found = true
	}
	return found
}

func hermesPreviousContentIsBorder(snap *vt.Snap, row int) bool {
	for above := row - 1; above >= 0; above-- {
		candidate := strings.TrimSpace(lineText(snap.Lines[above]))
		if candidate == "" {
			continue
		}
		return hermesHorizontalBorder(candidate)
	}
	return false
}

func hermesComposerRow(row string) bool {
	fields := strings.Fields(row)
	if len(fields) == 0 {
		return false
	}
	if hermesPromptSymbol(fields[0]) {
		return true
	}
	return len(fields) >= 2 && validHermesProfilePrefix(fields[0]) && hermesPromptSymbol(fields[1])
}

func hermesStateComposerRow(row string, cols int, stateIcon string) bool {
	fields := strings.Fields(row)
	if len(fields) == 0 || fields[0] != stateIcon {
		return false
	}
	if cols < hermesCompactWidth {
		switch stateIcon {
		case "⚠":
			return hermesCompactSourceRow(row, "⚠", cols) ||
				hermesCompactSourceRow(row, "⚠ type 1/2/3, or use ↑/↓ then Enter", cols)
		case "?":
			return hermesCompactSourceRow(row, "?", cols)
		case "✎":
			return hermesCompactSourceRow(row, "✎ type your answer here and press Enter", cols)
		}
		return false
	}
	return len(fields) >= 2 && hermesPromptSymbol(fields[1])
}

func hermesAgentBusyRow(row string, cols int) bool {
	fields := strings.Fields(row)
	if cols < hermesCompactWidth {
		return hermesCompactSourceRow(row, "⚕", cols) ||
			hermesCompactSourceRow(row, "⚕ "+hermesBusyPlaceholder, cols)
	}
	if len(fields) < 3 || fields[0] != "⚕" || !hermesPromptSymbol(fields[1]) {
		return false
	}
	prefix := fields[0] + " " + fields[1] + " "
	return strings.TrimSpace(row) == prefix+hermesBusyPlaceholder
}

func hermesCommandComposerRow(row string, cols int) bool {
	fields := strings.Fields(row)
	if cols < hermesCompactWidth {
		return len(fields) == 1 && hermesCommandSpinner(fields[0]) ||
			len(fields) >= 2 && hermesCommandSpinner(fields[0]) && hermesCommandSpinner(fields[1])
	}
	return len(fields) >= 2 && hermesCommandSpinner(fields[0]) && hermesPromptSymbol(fields[1])
}

// hermesCompactSourceRow accepts an exact compact row or a viewport-clipped
// prefix of it. A clipped row must consume the viewport (allowing one cell for
// terminal width accounting), so arbitrary prose that merely begins with the
// same state icon is not treated as live chrome.
func hermesCompactSourceRow(row, full string, cols int) bool {
	row = strings.TrimSpace(row)
	if row == full {
		return true
	}
	return strings.HasPrefix(full, row) && len([]rune(row)) >= cols-1
}

func hermesCommandNavigationRow(row string, compact bool) bool {
	fields := strings.Fields(row)
	if len(fields) < 5 || !hermesCommandSpinner(fields[0]) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(row), fields[0]))
	for _, expected := range []string{
		hermesCommandHint + "input temporarily disabled",
		hermesCommandHint + "input stays active; Enter queues",
	} {
		if rest == expected || compact && strings.HasPrefix(rest, hermesCommandHint) && strings.HasPrefix(expected, rest) {
			return true
		}
	}
	return false
}

func hermesNavigationRow(row, navigation string) bool {
	row = strings.TrimSpace(row)
	if row == navigation {
		return true
	}
	tail, ok := strings.CutPrefix(row, navigation)
	if !ok {
		return false
	}
	tail = strings.TrimSpace(tail)
	if len(tail) < 4 || tail[0] != '(' || !strings.HasSuffix(tail, "s)") {
		return false
	}
	for _, r := range tail[1 : len(tail)-2] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hermesPromptSymbol(token string) bool {
	runes := []rune(token)
	return len(runes) == 1 && strings.ContainsRune(hermesPromptSymbols, runes[0])
}

func hermesCommandSpinner(token string) bool {
	runes := []rune(token)
	return len(runes) == 1 && strings.ContainsRune(hermesCommandSpinners, runes[0])
}

// validHermesProfilePrefix mirrors the upstream profile identifier grammar
// used by Hermes 0.20.6: [a-z0-9][a-z0-9_-]{0,63}. Besides accepting real
// named-profile composers, this rejects partial redraw debris such as "──❯",
// which otherwise looks like a one-token prompt immediately above the border
// and would be a dangerous false-idle classification during work.
func validHermesProfilePrefix(prefix string) bool {
	if len(prefix) < 1 || len(prefix) > 64 {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && (c == '_' || c == '-') {
			continue
		}
		return false
	}
	return true
}

func hermesHorizontalBorder(row string) bool {
	runes := []rune(strings.TrimSpace(row))
	if len(runes) < 6 {
		return false
	}
	for _, r := range runes {
		if !strings.ContainsRune("─━═╌╍", r) {
			return false
		}
	}
	return true
}

// evaluateCodexGrid reads Codex's real screen (q65). Codex has no typed signal in
// v1 (D1), so the grid is its SOLE driver and its idle screen MUST read idle, not
// inconclusive. The approval dialog is checked FIRST: it is modal, so it outranks
// any busy remnant in the region (including one quoted inside the command under
// approval). Then a busy marker anywhere in the bottom region is active; a
// composer prompt on the parked-cursor row with no busy marker is idle; else
// inconclusive.
func evaluateCodexGrid(snap *vt.Snap) (status.Turn, status.Interaction, bool) {
	if snap == nil {
		return status.TurnUnknown, status.InteractionUnknown, false
	}
	if regionContains(snap, codexApprovalHint) {
		return status.TurnIdle, status.InteractionPermission, true
	}
	if hasBusyMarker(snap) {
		return status.TurnActive, status.InteractionNone, true
	}
	if composerOnCursorRow(snap) {
		return status.TurnIdle, status.InteractionNone, true
	}
	return status.TurnUnknown, status.InteractionUnknown, false
}

// evaluateClaudeGrid reads Claude's real screen (dqh). The modal dialogs are
// checked FIRST: each is modal, and each spells its selected option with the
// composer glyph, so any later check would misread them. Then a busy marker is
// active; then the WORKFLOW marker — background children running behind an idle
// main loop (c7i4), which the busy row cannot see because claude drops the busy
// hint the moment the main turn ends; a composer prompt anywhere in the bottom
// region with neither marker is idle — it does not require the cursor on the
// composer row, since Claude's idle footer ("Brewed for Ns") renders below the
// composer box; else inconclusive. The busy and workflow markers both mean
// active, so their relative order is immaterial.
func evaluateClaudeGrid(snap *vt.Snap) (status.Turn, status.Interaction, bool) {
	if snap == nil {
		return status.TurnUnknown, status.InteractionUnknown, false
	}
	for _, hint := range claudeDialogHints {
		if regionContains(snap, hint) {
			return status.TurnIdle, status.InteractionPermission, true
		}
	}
	if hasBusyMarker(snap) || hasWorkflowMarker(snap) {
		return status.TurnActive, status.InteractionNone, true
	}
	if composerInRegion(snap) {
		return status.TurnIdle, status.InteractionNone, true
	}
	return status.TurnUnknown, status.InteractionUnknown, false
}

// hasBusyMarker reports whether the bottom region carries a "turn is running"
// signal: the busy hint in its status-row shape, or a braille spinner glyph.
func hasBusyMarker(snap *vt.Snap) bool {
	last, _, ok := lastContentLine(snap)
	if !ok {
		return false
	}
	for y := last; y >= 0 && y > last-gridRegionRows; y-- {
		t := lineText(snap.Lines[y])
		if busyHintOnRow(t) {
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

// busyHintOnRow reports whether row carries the busy hint in one of the two
// shapes a live status line renders it in.
//
// The first is a parenthesis group that both opens and closes on that same row —
// "• Working (1s • esc to interrupt)", "Baking… (12s · esc to interrupt)" — the
// shape of every codex frame in the corpus
// (docs/verification/fixtures/spike-sa/codex-*), including the two-group row
// "• Starting MCP servers (2/4): codex_apps, context7 (0s • esc to interrupt)",
// where the earlier group must not be mistaken for the enclosing one.
//
// The second is a FIELD of a dot-separated status bar, which is how live claude
// 2.1.224 prints it: "⏸ manual mode on · esc to interrupt · ← 3 agents"
// (docs/verification/spike-SD.md F1, fixtures/spike-sd/busy-stream.json). Version
// note: 2.1.214 did not print the phrase at all, so this shape is new evidence,
// not a correction of the first.
//
// The bare substring is NOT enough: a session whose scrolled OUTPUT carries the
// phrase — an agent editing this very file, a quoted doc, a pasted transcript —
// then read WORKING while sitting idle at its composer, and stuck there, since
// the grid tap that would correct it kept re-reading the same prose
// (agents-tracker-fji). Prose quoting a WHOLE status row verbatim, separators and
// all, still fools both shapes; that residual case is accepted, as it was for the
// parenthesized one.
func busyHintOnRow(row string) bool {
	for off := 0; ; {
		i := strings.Index(row[off:], escToInterrupt)
		if i < 0 {
			return false
		}
		i += off
		end := i + len(escToInterrupt)
		if enclosedInParens(row, i, end) || abutsDotSeparator(row, i, end) {
			return true
		}
		off = end
	}
}

// statusBarSeparator is the field separator of claude's bottom status bar:
// U+00B7 MIDDLE DOT with one ASCII space on each side (byte-exact from
// fixtures/spike-sd/busy-stream.json).
const statusBarSeparator = " · "

// The status-bar fields claude renders while BACKGROUND WORK is outstanding
// (agents-tracker-c7i4, spike-SE F4). When the main turn ends the busy hint
// disappears and the composer comes back, so the busy row cannot see a workflow
// that is still running; these fields can. Byte-exact from
// fixtures/spike-se/workflow-background.json, whose idle-with-children rows read
//
//	"⏸ manual mode on · ? for shortcuts · ← 2 agents · ↓ to manage"
//	"⏸ manual mode on · 1 shell · ← 2 agents · ↓ to manage"
//
// manageBackgroundHint is U+2193 DOWNWARDS ARROW + " to manage", the affordance
// that opens the background-task list. shellCountField is the count of live
// background shells; the anchor is the trailing " shell" so a plural ("2 shells")
// counts too.
//
// "← N agents" is deliberately NOT a marker despite sitting in the same bar: the
// same capture shows it on screen at session start with nothing running (and as
// "← for agents" before the count resolves), and the spike-SD idle tail —
// conclusively idle, no child ever launched — ends "· ← 3 agents". It is the
// agent-picker keyboard affordance, not a running count.
const (
	manageBackgroundHint = "↓ to manage"
	shellCountField      = " shell"
)

// hasWorkflowMarker reports whether the bottom region carries a background-work
// field of claude's status bar.
func hasWorkflowMarker(snap *vt.Snap) bool {
	last, _, ok := lastContentLine(snap)
	if !ok {
		return false
	}
	for y := last; y >= 0 && y > last-gridRegionRows; y-- {
		if workflowHintOnRow(lineText(snap.Lines[y])) {
			return true
		}
	}
	return false
}

// workflowHintOnRow reports whether row carries a background-work FIELD of the
// dot-separated status bar, mirroring busyHintOnRow's discipline: the bare
// substring is not enough, because claude prints the same words in its own
// transcript ("⎿  Backgrounded agent (↓ to manage · ctrl+o to expand)") where they
// stay in scrollback for the rest of the session — the agents-tracker-fji failure
// mode. A field of the bar is preceded by the separator; that parenthetical is
// preceded by "(". Prose quoting a whole bar row verbatim, separators and all,
// still fools it; that residual is accepted here exactly as it is for the busy
// hint.
func workflowHintOnRow(row string) bool {
	return barFieldOnRow(row, manageBackgroundHint) || shellCountOnRow(row)
}

// barFieldOnRow reports whether field occurs in row immediately after a status-bar
// separator.
func barFieldOnRow(row, field string) bool {
	for off := 0; ; {
		i := strings.Index(row[off:], field)
		if i < 0 {
			return false
		}
		i += off
		if strings.HasSuffix(row[:i], statusBarSeparator) {
			return true
		}
		off = i + len(field)
	}
}

// shellCountOnRow reports whether row carries a "<digits> shell[s]" field of the
// status bar — the digits run back to a separator, so the count is the field's
// first token and prose that merely ends a clause with a number does not qualify.
func shellCountOnRow(row string) bool {
	for off := 0; ; {
		i := strings.Index(row[off:], shellCountField)
		if i < 0 {
			return false
		}
		i += off
		start := i
		for start > 0 && row[start-1] >= '0' && row[start-1] <= '9' {
			start--
		}
		if start < i && strings.HasSuffix(row[:start], statusBarSeparator) {
			return true
		}
		off = i + len(shellCountField)
	}
}

// abutsDotSeparator reports whether row[start:end] is a FIELD of a dot-separated
// status bar — a separator immediately before it, or immediately after it.
func abutsDotSeparator(row string, start, end int) bool {
	return strings.HasSuffix(row[:start], statusBarSeparator) || strings.HasPrefix(row[end:], statusBarSeparator)
}

// enclosedInParens reports whether row[start:end] sits inside a "(...)" group
// opened and closed on this row.
func enclosedInParens(row string, start, end int) bool {
	if open := strings.LastIndexByte(row[:start], '('); open < 0 || strings.LastIndexByte(row[:start], ')') > open {
		return false
	}
	tail := row[end:]
	closing := strings.IndexByte(tail, ')')
	if closing < 0 {
		return false
	}
	opening := strings.IndexByte(tail, '(')
	return opening < 0 || opening > closing
}

// regionContains reports whether any row in the bottom region contains the
// literal substring.
func regionContains(snap *vt.Snap, literal string) bool {
	last, _, ok := lastContentLine(snap)
	if !ok {
		return false
	}
	for y := last; y >= 0 && y > last-gridRegionRows; y-- {
		if strings.Contains(lineText(snap.Lines[y]), literal) {
			return true
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
