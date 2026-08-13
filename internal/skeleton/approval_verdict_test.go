package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the OWNER RULING of 2026-08-07, daemon half: the daemon
// classifies an approval_resolved as §3.6's `allowed` or `denied` FROM THE CHOSEN DECISION'S
// VERDICT.
//
// WHAT WAS THERE BEFORE. approveInteraction resolved EVERY remote answer as `allowed`, with the
// gap named in its own ponytail comment and recorded in a1-integration.md §8.7: "§3.6's
// allowed/denied split needs a NORMALIZED verdict for an id drawn from the CLI's OWN vocabulary
// ... and adapter.DecisionChoice carries {ID, Label} and no verdict bit". The ruling supplies the
// bit. A `cancel` tapped on the phone must not be transcribed as an approval.
//
// The `by` and `operation_id` halves are already fenced (approval_validate_r4_test.go); this file
// pins the one field that had no source.
//
// M1.2 REWRITE. The verdict now does one more job, and it is a bigger one: it selects WHICH KEY
// the daemon types into the session's own dialog (mirror-program.md section 3). So each arm here
// runs against a session showing a recorded permission dialog, and the resolution it checks is
// the one the daemon emits when it OBSERVES that dialog leave -- the tap resolves nothing. The
// vocabulary is still spike-SB's Codex ids, which is the whole point: `cancel` is a refusal and
// nothing about the string says so, so only the adapter's captured verdict can tell the daemon
// which key answers it.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// approvalWithVerdicts is a pending approval_request whose decisions carry the polarity the
// adapter classified at capture -- spike-SB's Codex vocabulary, which is exactly the case a
// daemon-side guess would get wrong: `cancel` is a refusal and nothing about the string says so.
func approvalWithVerdicts(ref string) adapter.Interaction {
	in := pendingApprovalInteraction(ref, "run rm -rf build")
	in.Decisions = []adapter.DecisionChoice{
		{ID: "accept", Label: "Allow", Verdict: adapter.VerdictAllow},
		{ID: "acceptWithExecpolicyAmendment", Label: "Allow and amend policy", Verdict: adapter.VerdictAllow},
		{ID: "cancel", Label: "Deny", Verdict: adapter.VerdictDeny},
		{ID: "addDirectories", Label: "Add directories", Verdict: adapter.VerdictOther},
	}
	return in
}

// dialogLeaves is the machine's own observation that the dialog the daemon typed at is gone --
// the only thing that resolves anything since M1.2.
func dialogLeaves(sk *Daemon, session string) {
	sk.emitStatus(session, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission})
	sk.emitStatus(session, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone})
}

// TestApprove_ADenyVerdictDecisionResolvesDenied is the ruling's daemon half. The id is the CLI's
// own -- `cancel`, which spike-SB captured Codex offering -- so nothing but the adapter's captured
// verdict can tell the daemon this tap was a refusal.
func TestApprove_ADenyVerdictDecisionResolvesDenied(t *testing.T) {
	r := newInjectRig(t, bashDialogGrid, approvalWithVerdicts("req-deny"))

	code, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-deny", approveFor(t, r.sk, r.local, r.item, "cancel"))
	if err != nil {
		t.Fatalf("a correctly-bound approve carrying a deny decision was refused %q: %v. A refusal is a "+
			"REJECTED answer; a denial is an ACCEPTED one that says no", code, err)
	}
	if got := r.readBack(t); got != "3" {
		t.Errorf("the session's stdin received %q; want the recorded DENY key %q. The id tapped was "+
			"%q, which says nothing about polarity -- only the adapter's captured verdict picks the "+
			"key, and picking the other one would RUN the tool the owner refused", got, "3", "cancel")
	}
	dialogLeaves(r.sk, r.local)

	res := awaitResolution(t, r.sk, r.local, itemString(t, r.item, "item_id"))
	if res["decision"] != "denied" {
		t.Errorf("decision = %v; want \"denied\". The owner tapped %q, which the adapter classified "+
			"%s at capture -- transcribing it as an approval records a grant the owner never gave (§3.6)",
			res["decision"], "cancel", adapter.VerdictDeny)
	}
	if res["by"] != "phone" {
		t.Errorf("by = %v; want \"phone\" -- a denial is still a phone-driven resolution (§3.6)", res["by"])
	}
}

// TestApprove_AnAllowVerdictDecisionResolvesAllowed is the other arm, on an id that is NOT the
// obvious one: `acceptWithExecpolicyAmendment` is a grant whose string says nothing of the kind,
// which is why §3.5 refuses a normalized vocabulary and the adapter classifies instead.
func TestApprove_AnAllowVerdictDecisionResolvesAllowed(t *testing.T) {
	r := newInjectRig(t, bashDialogGrid, approvalWithVerdicts("req-allow"))

	if _, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-allow",
		approveFor(t, r.sk, r.local, r.item, "acceptWithExecpolicyAmendment")); err != nil {
		t.Fatalf("a correctly-bound approve was refused: %v", err)
	}
	if got := r.readBack(t); got != "1" {
		t.Errorf("the session's stdin received %q; want the recorded ALLOW key %q", got, "1")
	}
	dialogLeaves(r.sk, r.local)

	res := awaitResolution(t, r.sk, r.local, itemString(t, r.item, "item_id"))
	if res["decision"] != "allowed" {
		t.Errorf("decision = %v; want \"allowed\" -- the adapter classified %q as %s at capture",
			res["decision"], "acceptWithExecpolicyAmendment", adapter.VerdictAllow)
	}
}

// TestApprove_AnOtherVerdictDecisionCannotBeAppliedAndIsRefused. IS-RES-1's reasoning is
// unchanged and its conclusion has moved, because M1.2 gave the verdict a second job.
//
// WHAT THIS ARM USED TO PIN, verbatim from the assertion it replaces: `decision = %v; want
// "allowed" (IS-RES-1). Only a deny verdict resolves denied -- a decision the adapter declared
// other says nothing about the owner's intent, and a transcript line claiming a refusal is a
// claim nobody made`. That was the honest reading while the tap merely RECORDED an outcome:
// §3.6 has no third value, and `allowed` asserted only "answered from the phone, and not
// refused".
//
// It cannot survive an answer that is APPLIED. The recorded dialog has exactly two answerable
// options, and a decision the adapter could place neither way (spike-SB's `addDirectories`) has
// no key on it. Typing the allow key would not be a weak record any more -- it would RUN
// something on the owner's machine on the strength of a guess. So the same premise now yields a
// refusal, the card stays pending, and nothing is typed.
func TestApprove_AnOtherVerdictDecisionCannotBeAppliedAndIsRefused(t *testing.T) {
	r := newInjectRig(t, bashDialogGrid, approvalWithVerdicts("req-other"))

	code, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-other",
		approveFor(t, r.sk, r.local, r.item, "addDirectories"))
	if err == nil {
		t.Fatalf("an %s decision was applied. Nothing on the dialog answers it, so a key typed here "+
			"presses a button the owner never chose", adapter.VerdictOther)
	}
	if code != protocol.CodeInvalidField {
		t.Errorf("error_code = %q; want %q -- the decision as offered cannot be applied at all, which "+
			"is a permanent property of the request and not a stale card", code, protocol.CodeInvalidField)
	}
	r.assertNothingWasTyped(t)
	r.assertNoResolutionYet(t)
}
