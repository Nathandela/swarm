package claude

// FAILING-FIRST (TDD RED, GG-5) for Mirror M1.2's adapter half: the claude adapter is the
// party that knows how to ANSWER the dialog it recognized, and it says so through the optional
// adapter.ApprovalApplier seam.
//
// WHY A SEAM AND NOT A DIRECT CALL. The daemon holds the grid and the phone's chosen decision;
// only the per-CLI adapter holds the recorded key map, and mirror-program.md's table says every
// other CLI answers by a different mechanism entirely (Codex by native RPC, opencode by HTTP).
// So "which bytes answer this screen" is per-adapter by construction, and an adapter that
// cannot answer by keystroke simply does not implement it -- ADR-010 §5's own posture, where
// ABSENCE is a signal and not a defect.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

// applier is the production claude adapter as the seam the daemon reaches it through.
func applier(t *testing.T) adapter.ApprovalApplier {
	t.Helper()
	ap, ok := adapter.AsApprovalApplier(New())
	if !ok {
		t.Fatal("the claude adapter is not an adapter.ApprovalApplier. It is the only party holding " +
			"the recorded key map for its own dialog, so nothing else can answer one")
	}
	return ap
}

// TestApprovalKeys_TheAdapterAnswersARecognizedDialogWithItsRecordedKeys. The verdict is the
// normalized polarity the adapter itself attached to the decision at capture; the keys are the
// version-stamped observation M1.1 recorded off claude 2.1.231. The action is the pending
// request's own ToolAction.Type, which must name the dialog on screen -- see
// TestApprovalKeys_RefusesADialogThatIsNotTheRequestsOwnTool.
func TestApprovalKeys_TheAdapterAnswersARecognizedDialogWithItsRecordedKeys(t *testing.T) {
	ap := applier(t)
	for _, tc := range []struct{ fixture, action string }{
		{"bash-approval-2.1.231", "execute"},
		{"edit-approval-2.1.231", "edit"},
		{"edit-approval-2.1.231", "write"},
	} {
		snap := loadPermGrid(t, tc.fixture+".snap.json")
		if got, ok := ap.ApprovalKeys(snap, adapter.VerdictAllow, tc.action); !ok || got != "1" {
			t.Errorf("%s/%s: ApprovalKeys(allow) = %q,%v; want \"1\",true -- the recorded key that "+
				"selects option 1 and submits it in one keystroke", tc.fixture, tc.action, got, ok)
		}
		if got, ok := ap.ApprovalKeys(snap, adapter.VerdictDeny, tc.action); !ok || got != "3" {
			t.Errorf("%s/%s: ApprovalKeys(deny) = %q,%v; want \"3\",true -- the recorded key that refuses "+
				"the tool. It is ABSOLUTE: a live run answered 3 while option 1 was highlighted and "+
				"the request was denied", tc.fixture, tc.action, got, ok)
		}
	}
}

// TestApprovalKeys_RefusesADialogThatIsNotTheRequestsOwnTool is the review finding of
// 2026-08-13 (mirror-m1.md M1.8): the grid gate proved that AN answerable dialog was on screen
// and never that it was THIS request's dialog.
//
// THE ROUTE IT CLOSES. `hookclient.Post` is fire-and-forget, so a dialog raised at the terminal
// is on the glass before its hook has been shaped into an item. The owner answers dialog A at
// the keyboard, claude raises dialog B immediately; A is still pending daemon-side (the
// interaction dimension never left `permission`, so nothing resolved it) and a phone approve
// for A arriving in that window used to pass the tuple check, pass the gate on B's dialog, and
// type A's verdict into B -- approving a tool the owner's card never named.
//
// IT IS A PARTIAL BIND AND SAYS SO. Bash-after-Bash stays ambiguous, because the recognizer
// reads a variant and not a command. What it removes is the CROSS-TOOL case, which is the one
// where the typed key answers a question of a different kind than the one the card showed.
func TestApprovalKeys_RefusesADialogThatIsNotTheRequestsOwnTool(t *testing.T) {
	ap := applier(t)
	for _, tc := range []struct{ fixture, action, why string }{
		{"bash-approval-2.1.231", "edit", "an Edit request answered on the Bash dialog runs a command"},
		{"bash-approval-2.1.231", "write", "a Write request answered on the Bash dialog runs a command"},
		{"edit-approval-2.1.231", "execute", "a Bash request answered on the Edit dialog writes a file"},
		{"bash-approval-2.1.231", "read", "no recorded dialog is the Read tool's"},
		{"bash-approval-2.1.231", "other", "IS-TOOL-2's unclassifiable action names no dialog at all"},
		{"bash-approval-2.1.231", "", "an action the capture never carried names no dialog either"},
	} {
		snap := loadPermGrid(t, tc.fixture+".snap.json")
		for _, verdict := range []string{adapter.VerdictAllow, adapter.VerdictDeny} {
			if got, ok := ap.ApprovalKeys(snap, verdict, tc.action); ok {
				t.Errorf("%s: ApprovalKeys(%s, action=%q) on %s returned %q. %s -- the gate must prove "+
					"the dialog on screen is THIS request's, not merely that some answerable dialog is up",
					tc.fixture, verdict, tc.action, tc.fixture, got, tc.why)
			}
		}
	}
}

// TestApprovalKeys_RefusesAGridThatIsNotARecognizedDialog. The refusal is the whole safety
// property: a key returned for a screen that is not the dialog is typed into whatever has
// focus. Every negative grid M1.1 recorded -- including the adversarial folder-trust dialog,
// which is modal, numbered and preselected but is NOT a tool approval -- must yield nothing.
func TestApprovalKeys_RefusesAGridThatIsNotARecognizedDialog(t *testing.T) {
	ap := applier(t)
	for _, fixture := range []string{
		"neg-composer-idle-2.1.231", "neg-working-2.1.231", "neg-trust-dialog-2.1.231",
	} {
		snap := loadPermGrid(t, fixture+".snap.json")
		for _, verdict := range []string{adapter.VerdictAllow, adapter.VerdictDeny} {
			if got, ok := ap.ApprovalKeys(snap, verdict, "execute"); ok {
				t.Errorf("%s: ApprovalKeys(%s) returned %q. That grid is not a tool approval, and a "+
					"key typed at it presses a button nobody asked for", fixture, verdict, got)
			}
		}
	}
	if got, ok := ap.ApprovalKeys(nil, adapter.VerdictAllow, "execute"); ok {
		t.Errorf("ApprovalKeys(nil) returned %q; a missing grid is not evidence of a dialog", got)
	}
}

// TestApprovalKeys_RefusesAVerdictItHasNoKeyFor. `other` is IS-TOOL-2's posture applied to a
// decision the adapter could place neither way. There is no third button on this dialog, so
// there is no third key -- and answering with the allow key would record a grant nobody gave.
func TestApprovalKeys_RefusesAVerdictItHasNoKeyFor(t *testing.T) {
	ap := applier(t)
	snap := loadPermGrid(t, "bash-approval-2.1.231.snap.json")
	for _, verdict := range []string{adapter.VerdictOther, "", "maybe"} {
		if got, ok := ap.ApprovalKeys(snap, verdict, "execute"); ok {
			t.Errorf("ApprovalKeys(%q) returned %q; the recorded dialog offers exactly two answerable "+
				"options and neither of them is %q", verdict, got, verdict)
		}
	}
}
