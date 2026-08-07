package engine

// agents-tracker-c7i4 (spike-SE F4): the grid half of the workflow fix.
//
// While a claude orchestrates BACKGROUND children its main loop can be idle —
// composer rendered, no "esc to interrupt" — with the workflow still running.
// The claude signature then falls through to the composer rule and reads the
// frame CONCLUSIVELY idle, which is the false "done" this bead removes. Claude's
// status bar does distinguish the two states: while background work is
// outstanding it renders extra dot-separated FIELDS naming that work.
//
// Which fields, read off the committed captures rather than assumed:
//
//	spike-se, idle with a child running (the false-done window):
//	  "⏸ manual mode on · ? for shortcuts · ← 2 agents · ↓ to manage"
//	  "⏸ manual mode on · 1 shell · ← 2 agents · ↓ to manage"
//	spike-se, same session BEFORE anything was backgrounded:
//	  "⏸ manual mode on · ? for shortcuts · ← 2 agents        ● high · /effort"
//	spike-sd, the genuinely quiet idle tail (no children ever):
//	  "⏸ manual mode on · ? for shortcuts · ← 3 agents"
//
// So "← N agents" is NOT a children marker: it is the standing keyboard
// affordance for the agent picker (it renders "← for agents" before the count is
// known, and it is on screen at session start with nothing running). The fields
// that appear exactly when background work exists, and in no other fixture in
// the corpus, are the background-shell count ("· 1 shell") and the background-task
// manager affordance ("· ↓ to manage").

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/status"
)

// workflowBackgroundFixture is the live capture of a claude that launched one
// background agent and ended its turn while the child ran (spike-SE).
const workflowBackgroundFixture = "../../docs/verification/fixtures/spike-se/workflow-background.json"

// The two idle-with-children status rows the capture renders, byte-exact from the
// emulator: U+23F8 pause glyph, U+00B7 middle dots with one ASCII space on each
// side, U+2190 left arrow, U+2193 down arrow. Neither carries "esc to interrupt",
// and the ❯ composer is on screen in both — today they read idle.
const (
	claudeIdleWithChildrenRow       = "⏸ manual mode on · ? for shortcuts · ← 2 agents · ↓ to manage"
	claudeIdleWithChildShellRow     = "⏸ manual mode on · 1 shell · ← 2 agents · ↓ to manage"
	claudeQuietBarBeforeAnyChild    = "⏸ manual mode on · ? for shortcuts · ← 2 agents"
	claudeBackgroundedTranscriptRow = "  ⎿  Backgrounded agent (↓ to manage · ctrl+o to expand)"
)

// The live frames: an idle main loop with background work outstanding must read
// ACTIVE, conclusively, even though the composer is rendered.
func TestC7I4_LiveIdleWithChildrenReadsActive(t *testing.T) {
	fx, err := fixtureio.LoadFixture(workflowBackgroundFixture)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, row := range []string{claudeIdleWithChildrenRow, claudeIdleWithChildShellRow} {
		snap, off := feedUntilRow(t, fx.PTYCapture, row)
		if gridContainsRow(snap, escToInterrupt) {
			t.Fatalf("the frame carrying %q still shows %q; it is meant to be an IDLE main loop with children", row, escToInterrupt)
		}
		turn, inter, conclusive := evaluateGridSig(snap, sigClaude)
		if turn != status.TurnActive || inter != status.InteractionNone || !conclusive {
			t.Errorf("live idle-with-children frame at offset %d (status row %q) read (%s, %s, conclusive=%v); want (active, none, true)",
				off, row, turn, inter, conclusive)
		}
	}
}

// The over-anchoring guard, live: the spike-SD idle tail carries "· ← 3 agents"
// with no background work at all, and must stay idle. This is the frame that
// falsifies "← N agents" as a children marker.
func TestC7I4_QuietBarWithAgentAffordanceStaysIdle(t *testing.T) {
	fx, err := fixtureio.LoadFixture(claudeBusyStreamFixture)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	snap := snapOfCapture(t, fx.PTYCapture, captureCols, captureRows)
	if !gridContainsRow(snap, "← 3 agents") {
		t.Fatalf("the spike-SD idle tail no longer carries the agent affordance; this guard has lost its subject")
	}
	turn, inter, conclusive := evaluateGridSig(snap, sigClaude)
	if turn != status.TurnIdle || inter != status.InteractionNone || !conclusive {
		t.Fatalf("the quiet claude bar read (%s, %s, conclusive=%v); want (idle, none, true)", turn, inter, conclusive)
	}
}

// The rule one row at a time: a background-work field of the dot-separated bar
// counts; the same words in scrollback prose do not.
func TestC7I4_WorkflowRowShapes(t *testing.T) {
	workflow := []string{
		"  " + claudeIdleWithChildrenRow,
		"  " + claudeIdleWithChildShellRow,
		"  ⏸ manual mode on · 2 shells · ↓ to manage", // a plural count
		"  ⏸ manual mode on · 1 shell",                // the shell field closing the bar
	}
	quiet := []string{
		"  " + claudeQuietBarBeforeAnyChild,
		"  ⏸ manual mode on · ? for shortcuts · ← 3 agents",
		"  ⏸ manual mode on · ? for shortcuts",
		// The transcript line claude prints when it backgrounds an agent. It quotes
		// the affordance inside a parenthetical, not as a field of the status bar,
		// and it stays in scrollback for the rest of the session — the fji failure
		// mode, so it must not fire.
		claudeBackgroundedTranscriptRow,
		"  We launched 3 agents and 2 shell sessions; use the ↓ to manage them.",
		"  the bar reads 1 shell when a background shell is alive",
	}
	for _, row := range workflow {
		if !hasWorkflowMarker(snapFromLines(100, 0, 0, false, []string{row})) {
			t.Errorf("status row %q read as carrying no background work; want workflow marker", row)
		}
	}
	for _, row := range quiet {
		if hasWorkflowMarker(snapFromLines(100, 0, 0, false, []string{row})) {
			t.Errorf("row %q read as carrying background work; want no workflow marker", row)
		}
	}
}
