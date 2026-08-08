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

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
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

// openApprovalWithVerdicts captures one and returns the journalled item.
func openApprovalWithVerdicts(t *testing.T, sk *Daemon, session, ref string) map[string]any {
	t.Helper()
	sk.captureInteractions(session, newCaptureAdapter(approvalWithVerdicts(ref)),
		adapter.HookPayload{Event: "PermissionRequest"})
	for _, item := range awaitItems(t, sk, session, 1) {
		if item["kind"] == adapter.KindApprovalRequest && itemString(t, item, "status") == adapter.StatusInProgress {
			return item
		}
	}
	t.Fatalf("no pending approval_request reached the journal for %s", session)
	return nil
}

// TestApprove_ADenyVerdictDecisionResolvesDenied is the ruling's daemon half. The id is the CLI's
// own -- `cancel`, which spike-SB captured Codex offering -- so nothing but the adapter's captured
// verdict can tell the daemon this tap was a refusal.
func TestApprove_ADenyVerdictDecisionResolvesDenied(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print DENY\nidle 60s\n")
	item := openApprovalWithVerdicts(t, sk, m.ID, "req-deny")

	code, err := sk.approveInteraction(sk.api.endpointID, "op-deny", approveFor(t, sk, m.ID, item, "cancel"))
	if err != nil {
		t.Fatalf("a correctly-bound approve carrying a deny decision was refused %q: %v. A refusal is a "+
			"REJECTED answer; a denial is an ACCEPTED one that says no", code, err)
	}

	res := awaitResolution(t, sk, m.ID, itemString(t, item, "item_id"))
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
	sk := assemble(t)
	m := launchFake(t, sk, "print ALLOW\nidle 60s\n")
	item := openApprovalWithVerdicts(t, sk, m.ID, "req-allow")

	if _, err := sk.approveInteraction(sk.api.endpointID, "op-allow",
		approveFor(t, sk, m.ID, item, "acceptWithExecpolicyAmendment")); err != nil {
		t.Fatalf("a correctly-bound approve was refused: %v", err)
	}

	res := awaitResolution(t, sk, m.ID, itemString(t, item, "item_id"))
	if res["decision"] != "allowed" {
		t.Errorf("decision = %v; want \"allowed\" -- the adapter classified %q as %s at capture",
			res["decision"], "acceptWithExecpolicyAmendment", adapter.VerdictAllow)
	}
}

// TestApprove_AnOtherVerdictDecisionResolvesAllowedNotDenied pins IS-RES-1's weaker half, which
// is a ruling and not an accident: §3.6 has no third value for a remote answer, and `denied` is an
// assertion that the owner REFUSED. Manufacturing one from a decision the adapter could place
// neither way (spike-SB's `addDirectories`) would be exactly the guess the verdict exists to
// remove, so `other` lands with `allowed`, which asserts only "answered from the phone, and not
// refused". Without this fence the arm is free to flip silently in either direction.
func TestApprove_AnOtherVerdictDecisionResolvesAllowedNotDenied(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print OTHER\nidle 60s\n")
	item := openApprovalWithVerdicts(t, sk, m.ID, "req-other")

	if _, err := sk.approveInteraction(sk.api.endpointID, "op-other",
		approveFor(t, sk, m.ID, item, "addDirectories")); err != nil {
		t.Fatalf("a correctly-bound approve on an %s decision was refused: %v", adapter.VerdictOther, err)
	}

	res := awaitResolution(t, sk, m.ID, itemString(t, item, "item_id"))
	if res["decision"] != "allowed" {
		t.Errorf("decision = %v; want \"allowed\" (IS-RES-1). Only a %s verdict resolves denied -- a "+
			"decision the adapter declared %s says nothing about the owner's intent, and a transcript "+
			"line claiming a refusal is a claim nobody made", res["decision"], adapter.VerdictDeny, adapter.VerdictOther)
	}
}
