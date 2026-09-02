package engine

// FAILING-FIRST (TDD RED, GG-5), bead swarm-1mq: claude's folder-trust dialog is one more
// modal whose marked option is spelled with the composer glyph ("❯ No, exit"), and its
// standing row ("Enter to confirm · Esc to cancel") was not one the claude signature knew.
// So a session blocked on that dialog read as a settled composer -- ready_for_review --
// while nothing could happen until a human answered, and `swarm watch --until
// needs_input` never fired for it. Both recorded versions of the dialog are replayed
// here; each must read as a wait.

import (
	"os"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

func TestTrustGate_ClaudeFolderTrustDialogReadsAsPermission(t *testing.T) {
	cases := []struct{ name, fixture string }{
		{"2.1.258 (No preselected)", "../adapter/claude/testdata/trustdialog/trust-dialog-2.1.258.snap.json"},
		{"2.1.231 (Yes preselected)", "../adapter/claude/testdata/permdialog/neg-trust-dialog-2.1.231.snap.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			snap, err := vt.DecodeSnapshot(raw)
			if err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			turn, inter, conclusive := evaluateGridSig(snap, sigClaude)
			if turn != status.TurnIdle || inter != status.InteractionPermission || !conclusive {
				t.Fatalf("the folder-trust dialog read (%s, %s, conclusive=%v); want (idle, permission, true)", turn, inter, conclusive)
			}
			if g := status.Derive(status.Status{Process: status.ProcessRunning, Turn: turn, Interaction: inter}); g != status.GroupNeedsInput {
				t.Fatalf("the folder-trust dialog derived %v; want needs_input", g)
			}
		})
	}
}
