package skeleton

// FAILING-FIRST (TDD RED, GG-5), part two of review finding R4: IS-LIFE-4's DAEMON-SIDE
// VALIDATION of an arriving approve, and the daemon-authoritative expiry that outlives the
// phone's countdown.
//
// This half is white-box where the behavioural half (approval_r4_test.go) is not, and both
// reasons are real. The arriving approve has no wire route yet -- opForAction refuses one
// ("approve is not a daemon remote op (D6/D7)", internal/remotegw/command_loop.go) -- so the
// entry point IS the seam under test. And the daemon window is minutes long by design
// (spike-SC measured the CLIs holding 120-300 s), so an expiry test either reaches into the
// stored tuple or sleeps for two minutes.
//
// WHAT THIS PINS. ADR-007 D7, restated by IS-LIFE-4: an approve is "validated daemon-side
// against the binding tuple and expiry BEFORE the adapter applies it", and a stale or
// mismatched one is refused with a code from D10's taxonomy and NEVER applied. Every refusal
// below is a real attack or a real race, not a shape check.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// approveFor builds the approve a phone would send for the session's pending card: every field
// ECHOED VERBATIM off the item as received (IS-APR-2 forbids the phone computing or adjusting
// content_hash or expires_at), plus the chosen decision id from the card's own buttons.
func approveFor(t *testing.T, sk *Daemon, session string, item map[string]any, decision string) protocol.ApproveReq {
	t.Helper()
	inst, ok := item["agent_instance"].(map[string]any)
	if !ok {
		t.Fatalf("the approval_request carries no agent_instance: %v", item)
	}
	pid, _ := inst["shim_pid"].(float64)
	start, _ := inst["shim_start_time"].(float64)
	hash, _ := item["content_hash"].(string)
	expRaw, _ := item["expires_at"].(string)
	exp, err := time.Parse(time.RFC3339Nano, expRaw)
	if err != nil {
		t.Fatalf("expires_at %q does not parse: %v", expRaw, err)
	}
	return protocol.ApproveReq{
		Session:       sk.api.endpointID + "/" + session,
		AgentInstance: protocol.AgentInstanceRef{ShimPID: int(pid), ShimStartTime: int64(start)},
		InteractionID: itemString(t, item, "item_id"),
		ContentHash:   hash,
		ExpiresAt:     &exp,
		Decision:      decision,
	}
}

// openApprovalOn captures one pending approval_request and returns the item as journalled.
func openApprovalOn(t *testing.T, sk *Daemon, session, ref string) map[string]any {
	t.Helper()
	sk.captureInteractions(session, newCaptureAdapter(pendingApprovalInteraction(ref, "write src/main.rs")),
		adapter.HookPayload{Event: "PermissionRequest"})
	for _, item := range awaitItems(t, sk, session, 1) {
		if item["kind"] == adapter.KindApprovalRequest && itemString(t, item, "status") == adapter.StatusInProgress {
			return item
		}
	}
	t.Fatalf("no pending approval_request reached the journal for %s", session)
	return nil
}

// TestApprove_AValidApproveIsAcceptedAndResolvesTheCard. The whole point of the tuple is that
// there is an object to validate AGAINST; this is the arm that proves the object is real and
// that a correct answer is not refused by the checks the next test exercises.
//
// M1.2 REWRITE. The session now shows a recorded permission dialog, because a valid approve is
// APPLIED and not merely recorded: there has to be something to apply it to. And the resolution
// no longer lands on the tap -- it lands when the daemon OBSERVES the dialog leave -- so the
// observation is driven here rather than assumed. What is asserted about that record is
// unchanged, and deliberately: the daemon typed the phone's answer itself, so `by: phone` and
// the echoed operation_id are as true now as they were when the tap wrote them.
func TestApprove_AValidApproveIsAcceptedAndResolvesTheCard(t *testing.T) {
	r := newInjectRig(t, bashDialogGrid, claudeApproval("req-1"))

	code, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-1", approveFor(t, r.sk, r.local, r.item, "allow"))
	if err != nil {
		t.Fatalf("a correctly-bound approve was refused %q: %v. Every field was echoed verbatim off "+
			"the item the daemon itself minted (IS-APR-2), so a refusal here means the daemon cannot "+
			"validate its own tuple", code, err)
	}
	if code != "" {
		t.Errorf("an accepted approve carries error_code %q; a code is a REFUSAL reason (R-PROT.7)", code)
	}

	// The machine's own observation: the dialog the daemon just typed at has left the screen.
	r.sk.emitStatus(r.local, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission})
	r.sk.emitStatus(r.local, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone})

	res := awaitResolution(t, r.sk, r.local, itemString(t, r.item, "item_id"))
	if res["by"] != "phone" {
		t.Errorf("by = %v; want \"phone\" -- §3.6 attributes a resolution driven by a phone "+
			"ActionApprove to the phone", res["by"])
	}
	if res["operation_id"] != "op-1" {
		t.Errorf("operation_id = %v; want \"op-1\" -- §3.6 echoes it when a phone ActionApprove drove "+
			"the resolution, and it is the phone's idempotency key (IS-APR-1: never the interaction_id)",
			res["operation_id"])
	}
	if res["decision"] != "allowed" && res["decision"] != "denied" {
		t.Errorf("decision = %v; §3.6 resolves an applied decision as allowed or denied", res["decision"])
	}
}

// TestApprove_AStaleOrMismatchedApproveIsRefusedWithACodeAndAppliesNothing is the finding's
// centre. Each row is a real failure mode:
//
//   - a foreign machine: the signed tuple binds `machine` (D4), and an approve routed to the
//     wrong daemon must not resolve a same-named session here;
//   - an unknown interaction: the card was already resolved, expired, or never existed;
//   - a different agent instance: the CLI that asked is GONE and its pid was reused (S3/F6) --
//     applying here answers a question a DIFFERENT agent is now asking;
//   - a rewritten content_hash: the gateway is the documented D4/D5 owner-uid residual and can
//     alter an unsealed field, so the hash is what pins the approve to the content the owner saw;
//   - a pushed-out expiry: a phone countdown is display-only (§3.5), so an echoed expiry that
//     does not match the daemon's own is an attempt to extend the window;
//   - a decision the card never offered: the buttons come from decisions[].label (IS-APR-3), so
//     an id outside that set was never rendered to anybody.
//
// EVERY ROW MUST ALSO APPLY NOTHING. A refusal that still resolved the card would dismiss it on
// every surface while the machine stayed blocked -- worse than the approve landing.
func TestApprove_AStaleOrMismatchedApproveIsRefusedWithACodeAndAppliesNothing(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print REFUSE\nidle 60s\n")
	item := openApprovalOn(t, sk, m.ID, "req-1")
	itemID := itemString(t, item, "item_id")

	later := time.Now().Add(2 * time.Hour)
	cases := []struct {
		name    string
		machine string
		mutate  func(*protocol.ApproveReq)
		want    protocol.ErrorCode
	}{
		{"a foreign machine", "some-other-endpoint", nil, protocol.CodeInvalidField},
		{"an unknown interaction", "", func(r *protocol.ApproveReq) { r.InteractionID = "01ZZZZZZZZZZZZZZZZZZZZZZZZ" }, protocol.CodeStaleApproval},
		{"a different agent instance", "", func(r *protocol.ApproveReq) { r.AgentInstance.ShimStartTime++ }, protocol.CodeStaleApproval},
		{"a rewritten content hash", "", func(r *protocol.ApproveReq) { r.ContentHash = "00" + r.ContentHash[2:] }, protocol.CodeStaleApproval},
		{"a pushed-out expiry", "", func(r *protocol.ApproveReq) { r.ExpiresAt = &later }, protocol.CodeStaleApproval},
		{"a decision the card never offered", "", func(r *protocol.ApproveReq) { r.Decision = "acceptEverythingForever" }, protocol.CodeInvalidField},
		{"no decision at all", "", func(r *protocol.ApproveReq) { r.Decision = "" }, protocol.CodeInvalidField},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := approveFor(t, sk, m.ID, item, "accept")
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			machine := tc.machine
			if machine == "" {
				machine = sk.api.endpointID
			}
			code, err := sk.approveInteraction(machine, "op-x", req)
			if err == nil {
				t.Fatalf("%s was ACCEPTED. ADR-007 D7: a stale or mismatched approve is rejected "+
					"daemon-side and never translated into a blind keystroke", tc.name)
			}
			if code != tc.want {
				t.Errorf("error_code = %q; want %q. D10's taxonomy is what lets the phone tell a "+
					"permanent refusal from a retryable one (ErrorCode.Transient)", code, tc.want)
			}
		})
	}

	// The card is still pending after every refusal: nothing was applied and nothing dismissed.
	time.Sleep(500 * time.Millisecond)
	for _, it := range interactionItems(t, sk, m.ID) {
		if it["kind"] == adapter.KindApprovalResolved {
			t.Fatalf("a refused approve still resolved the card: %v. The machine is STILL blocked on "+
				"%s, and a card dismissed on every surface leaves nobody able to answer it", it, itemID)
		}
	}
}

// TestApprove_AnApproveAfterTheDaemonWindowIsRefusedEvenWhenEveryFieldMatches. Expiry is
// DAEMON-AUTHORITATIVE (§3.5) and is checked against the DAEMON's clock, not against the value
// the phone echoed -- a phone whose clock ran slow (or a relay that replayed a captured
// envelope) must not buy a second of extra window.
func TestApprove_AnApproveAfterTheDaemonWindowIsRefusedEvenWhenEveryFieldMatches(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print EXPIRE\nidle 60s\n")
	item := openApprovalOn(t, sk, m.ID, "req-1")
	req := approveFor(t, sk, m.ID, item, "accept")

	// Wind the window back on BOTH sides -- the daemon's stored tuple and the phone's echoed copy
	// -- which is what a card minted 121 s ago and tapped now actually looks like. Winding back
	// only the daemon's copy would be refused by the ECHO check instead, and would leave the
	// clock check untested while the test passed (found by mutation 5; see a1-integration.md).
	past := time.Now().Add(-time.Second)
	sk.itemMu.Lock()
	ap := sk.approvals[m.ID]
	if ap == nil {
		sk.itemMu.Unlock()
		t.Fatal("the daemon holds no pending approval for the session it just journalled a card for; " +
			"IS-LIFE-4 needs a stored tuple to validate an arriving approve against")
	}
	ap.expiresAt = past
	sk.itemMu.Unlock()
	req.ExpiresAt = &past

	code, err := sk.approveInteraction(sk.api.endpointID, "op-late", req)
	if err == nil {
		t.Fatal("an approve past the daemon's own window was accepted. §3.5 makes expiry " +
			"daemon-authoritative and a phone countdown display-only")
	}
	if code != protocol.CodeStaleApproval {
		t.Errorf("error_code = %q; want %q", code, protocol.CodeStaleApproval)
	}

	// IS-LIFE-2 still owes a resolution, and the daemon is the one that observed the lapse.
	res := awaitResolution(t, sk, m.ID, itemString(t, item, "item_id"))
	if res["decision"] != "expired" {
		t.Errorf("decision = %v; want \"expired\" (§3.6) -- the daemon's window passed", res["decision"])
	}
	if res["by"] != "daemon" {
		t.Errorf("by = %v; want \"daemon\" -- §3.6 attributes an expiry to the daemon", res["by"])
	}
}

// TestApproveReq_CarriesTheChosenDecisionOnTheWire is the wire half of IS-LIFE-4: "it needs the
// ApproveReq body (agent_instance, interaction_id, content_hash, expires_at) ... plus the chosen
// decision id". Without the field the daemon cannot tell WHICH button was tapped, and §3.5's
// decision ids are the CLI's own vocabulary, so nothing else on the envelope carries it.
//
// It is asserted on the JSON because that is the contract: the gateway reconstructs
// Control.Approve from the sealed RemoteCommand body, so a field that does not serialize does
// not exist.
func TestApproveReq_CarriesTheChosenDecisionOnTheWire(t *testing.T) {
	raw, err := json.Marshal(protocol.ApproveReq{
		Session:       "endpoint/local",
		InteractionID: "01ZZZZZZZZZZZZZZZZZZZZZZZZ",
		ContentHash:   "deadbeef",
		Decision:      "acceptWithExecpolicyAmendment",
	})
	if err != nil {
		t.Fatalf("marshal ApproveReq: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back["decision"] != "acceptWithExecpolicyAmendment" {
		t.Errorf("ApproveReq serialized %s; want a `decision` field carrying the CLI's OWN chosen "+
			"decision id (spike-SB captured Codex offering accept | acceptWithExecpolicyAmendment | "+
			"cancel). The decision is deliberately UNSIGNED -- content_hash is the signed tuple's one "+
			"content slot and D7 spends it on the interaction content (IS-LIFE-4)", raw)
	}
}
