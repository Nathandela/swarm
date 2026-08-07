package engine

// agents-tracker-d7vh (spike-SD F3/F4): claude's AskUserQuestion dialog and its
// plan-approval dialog are two MORE modal shapes whose selected option is
// spelled with the composer glyph ("❯ 1. Red", "❯ 1. Yes, and use auto mode"),
// so the claude signature read both standing dialogs as a settled composer —
// ready_for_review while the session was blocked on the human, the same defect
// fix B cured for the "Do you want to proceed?" approval dialog, on two shapes
// that dialog's help line does not cover.
//
// Each dialog carries its own standing row, both replayed here from the live
// captures through the production emulator.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/status"
)

// The markers must not leak into an ordinary idle screen. Claude's idle status
// bar names shift+tab too — "⏵⏵ bypass permissions on (shift+tab to cycle) · ←
// for agents" (live captures spike-sa/claude-plain*) — and it is a mode hint,
// not a dialog: only the plan dialog's "shift+tab to approve" is a wait. The row
// also carries both a parenthesis group and " · " separators with no busy hint
// in them, so it pins the F1 anchoring against the idle bar as well.
func TestD7VH_IdleStatusBarIsNotADialog(t *testing.T) {
	snap := snapFromLines(100, 2, 2, true, []string{
		"⏺ Done. What would you like to do next?",
		"",
		"❯ ",
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents",
	})
	turn, inter, conclusive := evaluateGridSig(snap, sigClaude)
	if turn != status.TurnIdle || inter != status.InteractionNone || !conclusive {
		t.Fatalf("claude idle screen with the shift+tab mode hint read (%s, %s, conclusive=%v); want (idle, none, true)", turn, inter, conclusive)
	}
}

func TestD7VH_ClaudeDialogsReadAsPermission(t *testing.T) {
	cases := []struct{ name, fixture, marker string }{
		{"AskUserQuestion", askUserWaitFixture, "Enter to select · ↑/↓ to navigate · Esc to cancel"},
		{"plan approval", planWaitFixture, "shift+tab to approve with this feedback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx, err := fixtureio.LoadFixture(tc.fixture)
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			// The capture ends on the standing, unanswered dialog. Read at the
			// capture's own geometry and at a shorter window, which scrolls the
			// dialog's head off the top but keeps its standing row on screen.
			for _, rows := range []int{captureRows, 30} {
				snap := snapOfCapture(t, fx.PTYCapture, captureCols, rows)
				if !gridContainsRow(snap, tc.marker) {
					t.Fatalf("at %d rows the final frame does not carry %q; the fixture is not showing the dialog", rows, tc.marker)
				}
				turn, inter, conclusive := evaluateGridSig(snap, sigClaude)
				if turn != status.TurnIdle || inter != status.InteractionPermission || !conclusive {
					t.Errorf("live claude %s dialog at %d rows read (%s, %s, conclusive=%v); want (idle, permission, true)",
						tc.name, rows, turn, inter, conclusive)
					continue
				}
				if g := status.Derive(status.Status{Process: status.ProcessRunning, Turn: turn, Interaction: inter}); g != status.GroupNeedsInput {
					t.Errorf("live claude %s dialog at %d rows derived %v; want needs_input", tc.name, rows, g)
				}
			}
		})
	}
}
