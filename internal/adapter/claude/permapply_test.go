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
// version-stamped observation M1.1 recorded off claude 2.1.231.
func TestApprovalKeys_TheAdapterAnswersARecognizedDialogWithItsRecordedKeys(t *testing.T) {
	ap := applier(t)
	for _, fixture := range []string{"bash-approval-2.1.231", "edit-approval-2.1.231"} {
		snap := loadPermGrid(t, fixture+".snap.json")
		if got, ok := ap.ApprovalKeys(snap, adapter.VerdictAllow); !ok || got != "1" {
			t.Errorf("%s: ApprovalKeys(allow) = %q,%v; want \"1\",true -- the recorded key that "+
				"selects option 1 and submits it in one keystroke", fixture, got, ok)
		}
		if got, ok := ap.ApprovalKeys(snap, adapter.VerdictDeny); !ok || got != "3" {
			t.Errorf("%s: ApprovalKeys(deny) = %q,%v; want \"3\",true -- the recorded key that refuses "+
				"the tool. It is ABSOLUTE: a live run answered 3 while option 1 was highlighted and "+
				"the request was denied", fixture, got, ok)
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
			if got, ok := ap.ApprovalKeys(snap, verdict); ok {
				t.Errorf("%s: ApprovalKeys(%s) returned %q. That grid is not a tool approval, and a "+
					"key typed at it presses a button nobody asked for", fixture, verdict, got)
			}
		}
	}
	if got, ok := ap.ApprovalKeys(nil, adapter.VerdictAllow); ok {
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
		if got, ok := ap.ApprovalKeys(snap, verdict); ok {
			t.Errorf("ApprovalKeys(%q) returned %q; the recorded dialog offers exactly two answerable "+
				"options and neither of them is %q", verdict, got, verdict)
		}
	}
}
