package engine

// agents-tracker-sskl FIX B: claude's approval dialog must read as needs_input
// from the grid, not as an idle composer.
//
// The dialog's selected option renders "❯ 1. Yes" — the same U+276F the idle
// composer uses — so the claude signature's composer scan classified the live
// approval frame (idle, none) and the session showed ready_for_review while it
// was blocked on the human. The dialog's own help line is the discriminator,
// and, like codex's, it must be read BEFORE the composer/busy checks because it
// is modal.
//
// The frame here is the real one: the whole PTY capture of the live c2
// interactive check, replayed through the production vt emulator. It is read at
// two terminal heights because they fail differently: tall enough to show the
// whole dialog, the composer scan reads (idle, none) — the false
// ready_for_review; too short for it, the frame reads inconclusive and the
// session sticks on whatever it last was. The help line is on screen in both,
// so both must read permission.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// claudeApprovalFixture is the live "This command requires approval" capture
// (docs/verification/spike-SC), whose rendered.txt ends on the dialog.
const claudeApprovalFixture = "../../docs/verification/fixtures/spike-sc/c2-interactive-check/fixture.json"

func TestSSKL_ClaudeApprovalDialogReadsAsPermission(t *testing.T) {
	fx, err := fixtureio.LoadFixture(claudeApprovalFixture)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	// 41 rows is the capture's own terminal height (the whole dialog fits, as in
	// the committed rendered.txt); 30 rows scrolls the option list off the top.
	for _, rows := range []int{41, 30} {
		snap := snapOfCapture(t, fx.PTYCapture, 100, rows)
		turn, inter, conclusive := evaluateGridSig(snap, sigClaude)
		if turn != status.TurnIdle || inter != status.InteractionPermission || !conclusive {
			t.Errorf("live claude approval dialog at %d rows read as (%s, %s, conclusive=%v); want (idle, permission, true)",
				rows, turn, inter, conclusive)
			continue
		}
		if g := status.Derive(status.Status{Process: status.ProcessRunning, Turn: turn, Interaction: inter}); g != status.GroupNeedsInput {
			t.Errorf("approval dialog at %d rows derived %v; want needs_input", rows, g)
		}
	}
}

// The dialog marker must not leak into the ordinary idle screen: claude sitting
// at its composer is still (idle, none), i.e. ready_for_review.
func TestSSKL_ClaudeIdleScreenIsNotAPermission(t *testing.T) {
	turn, inter, conclusive := evaluateGridSig(claudeIdleScreen(), sigClaude)
	if turn != status.TurnIdle || inter != status.InteractionNone || !conclusive {
		t.Fatalf("claude idle screen read as (%s, %s, conclusive=%v); want (idle, none, true)", turn, inter, conclusive)
	}
}

// snapOfCapture feeds a whole capture into a fresh cols x rows emulator and
// returns the resulting snapshot (snapAtOffset's fixed 100x30 cannot show a
// dialog taller than the window).
func snapOfCapture(t *testing.T, capture []byte, cols, rows int) *vt.Snap {
	t.Helper()
	emu := vt.NewEmulator(cols, rows)
	defer emu.Close()
	emu.Feed(capture)
	return decodeSnap(t, emu)
}
