package skeleton

// FAILING-FIRST (TDD RED, GG-5) for review finding R4 -- the APPROVAL LIFECYCLE's BACK HALF:
// interaction-schema.md IS-LIFE-4 (the D7 binding tuple on the item and its daemon-side
// validation), IS-LIFE-2 (every approval_request reaches exactly one approval_resolved) and
// IS-ST-2 (no in_progress item outlives its agent instance).
//
// A1a shipped the front half: an approval_request reaches the phone and is re-delivered across
// a repair (interaction_e2e_test.go). Its own evidence file recorded what it did not build --
// a1-integration.md §6: "the approval item is incomplete by §3.5: no agent_instance,
// content_hash or expires_at, and the phone can show a card it cannot answer"; "nothing emits
// approval_resolved on the cancel / supersede / expire / answered-locally paths, so a card can
// only be dismissed by a resolution nobody produces"; "no daemon-side pass closes in_progress
// items failed on instance death".
//
// THIS FILE IS BEHAVIOURAL RED, deliberately: every assertion runs against production entry
// points that ALREADY EXIST (captureInteractions, emitStatus, endSession) and reads the
// JOURNAL, so each failure names a missing RULE rather than a missing symbol. The white-box
// half -- the arriving approve and the daemon clock -- is approval_validate_r4_test.go, which
// necessarily names symbols this slice adds.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/status"
)

// ---- helpers ---------------------------------------------------------------
//
// interactionItems / interactionPayloads / awaitItems / itemString are the capture suite's
// (interaction_capture_test.go, interaction_r2_test.go): the journal is what the gateway
// forwards, so anything invisible there is invisible to the phone.

// awaitResolution polls until an approval_resolved naming interactionID lands, and returns it.
// The wait is the append floor's: ADR-010 §7 releases at most one item per window.
func awaitResolution(t *testing.T, sk *Daemon, session, interactionID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, item := range interactionItems(t, sk, session) {
			if item["kind"] != adapter.KindApprovalResolved {
				continue
			}
			if interactionID == "" || item["interaction_id"] == interactionID {
				return item
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no approval_resolved for %q ever reached the journal for %s. IS-LIFE-2: EVERY "+
		"approval_request reaches exactly one approval_resolved -- including when it is cancelled, "+
		"superseded, expired or answered at the machine -- and that guarantee is the whole of what "+
		"makes a stale card dismiss on every surface. Items seen: %v",
		interactionID, session, interactionItems(t, sk, session))
	return nil
}

// pendingApprovalInteraction is the adapter's shaped approval_request, with the CLI's own
// decision vocabulary (§3.5: the ids are the CLI's, never a normalized set).
func pendingApprovalInteraction(ref, summary string) adapter.Interaction {
	return adapter.Interaction{
		Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress, Ref: ref,
		Summary: summary, Mode: adapter.ModeCard,
		Action:    adapter.ToolAction{Type: "write", Path: "src/main.rs"},
		Decisions: []adapter.DecisionChoice{{ID: "accept", Label: "Allow"}, {ID: "cancel", Label: "Deny"}},
	}
}

// ---- IS-LIFE-4 / §3.5: the three daemon-authoritative fields ---------------

// TestApprovalRequest_ShipsTheD7BindingTupleAndTheDaemonAuthoritativeExpiry.
//
// §3.5 makes agent_instance, content_hash and expires_at fields OF THE ITEM, and IS-LIFE-4
// makes them the tuple an arriving approve is validated against. Without them the phone renders
// a card it CANNOT ANSWER: IS-APR-2 forbids it computing either value ("a phone SHALL echo
// content_hash and expires_at verbatim as received; it SHALL NOT compute or adjust either"), so
// a card that arrives without them has nothing to echo back.
func TestApprovalRequest_ShipsTheD7BindingTupleAndTheDaemonAuthoritativeExpiry(t *testing.T) {
	sk := assemble(t)
	// A REAL launched session, so agent_instance names a live shim rather than a zero: the
	// tuple's whole job is to bind the approval to THE instance that asked (ADR-007 D7).
	m := launchFake(t, sk, "print APPROVAL\nidle 60s\n")
	sk.captureInteractions(m.ID, newCaptureAdapter(pendingApprovalInteraction("req-1", "write src/main.rs")),
		adapter.HookPayload{Event: "PermissionRequest"})

	item := awaitItems(t, sk, m.ID, 1)[0]
	inst, ok := item["agent_instance"].(map[string]any)
	if !ok {
		t.Fatalf("the approval_request carries no `agent_instance` object: %v. §3.5 makes it the "+
			"ADR-007 D7 instance binding {shim_pid, shim_start_time}, and without it an approve "+
			"cannot be refused for naming a DIFFERENT agent than the one that asked", item)
	}
	if pid, _ := inst["shim_pid"].(float64); int(pid) != m.ShimPID {
		t.Errorf("agent_instance.shim_pid = %v; want the session's own shim %d", inst["shim_pid"], m.ShimPID)
	}
	if st, _ := inst["shim_start_time"].(float64); int64(st) != m.ShimStartTime {
		t.Errorf("agent_instance.shim_start_time = %v; want %d -- the start time is what makes a "+
			"REUSED pid a mismatch rather than a match (S3/F6)", inst["shim_start_time"], m.ShimStartTime)
	}

	hash, _ := item["content_hash"].(string)
	if len(hash) != 64 {
		t.Errorf("content_hash = %q (%d chars); §3.5 wants a daemon-computed SHA-256 over the "+
			"daemon's byte-exact canonicalization, which is 64 hex characters", hash, len(hash))
	}
	if _, err := hex.DecodeString(hash); err != nil && hash != "" {
		t.Errorf("content_hash %q is not hex: %v", hash, err)
	}

	exp, _ := item["expires_at"].(string)
	at, err := time.Parse(time.RFC3339Nano, exp)
	if err != nil {
		t.Fatalf("expires_at = %v, which does not parse as a time: %v. §3.5 makes expiry "+
			"DAEMON-AUTHORITATIVE and a phone countdown display-only, so the item must carry the "+
			"daemon's instant", item["expires_at"], err)
	}
	if !at.After(time.Now()) {
		t.Errorf("expires_at %s is not in the future; a card that arrives already expired can never be answered", exp)
	}
	// spike-SC measured the CLIs holding a decision 120 s (Codex, the shorter) to 300 s (Claude).
	// A daemon window beyond the CLI's own hold would promise an answer the CLI no longer wants.
	if at.After(time.Now().Add(300 * time.Second)) {
		t.Errorf("expires_at %s is more than 300 s out; spike-SC measured the CLIs' own holds at "+
			"120 s (Codex) to 300 s (Claude), and the daemon's window must sit inside them", exp)
	}
}

// TestApprovalRequest_TheContentHashCoversTheItemAsShipped is R2's rule made checkable:
// TRUNCATE, THEN HASH. A hash over pre-truncation content would name a body no surface holds,
// the rendered card could not reproduce it, and every approve from a truncated card would be
// refused as stale (ADR-007 D7). The canonical form is the SHIPPED BYTES with the hash's own
// slot zeroed -- self-reference resolved the only way it can be, and re-derivable by anyone
// holding the item.
func TestApprovalRequest_TheContentHashCoversTheItemAsShipped(t *testing.T) {
	sk := assemble(t)
	sk.captureInteractions("s-hash", newCaptureAdapter(pendingApprovalInteraction("req-h", "rm -rf /tmp/x")),
		adapter.HookPayload{Event: "PermissionRequest"})
	awaitItems(t, sk, "s-hash", 1)

	payloads := interactionPayloads(t, sk, "s-hash")
	if len(payloads) != 1 {
		t.Fatalf("the journal holds %d interaction record(s) for s-hash; want 1", len(payloads))
	}
	var item map[string]any
	if err := json.Unmarshal(payloads[0], &item); err != nil {
		t.Fatalf("the journalled item is not a JSON object: %v", err)
	}
	hash, _ := item["content_hash"].(string)
	if len(hash) != 64 {
		t.Fatalf("content_hash = %q; want a 64-character SHA-256 (§3.5)", hash)
	}
	zeroed := strings.Replace(string(payloads[0]),
		`"content_hash":"`+hash+`"`, `"content_hash":"`+strings.Repeat("0", 64)+`"`, 1)
	if zeroed == string(payloads[0]) {
		t.Fatalf("the item's content_hash slot is not recoverable from the shipped bytes: %s", payloads[0])
	}
	sum := sha256.Sum256([]byte(zeroed))
	if got := hex.EncodeToString(sum[:]); got != hash {
		t.Errorf("content_hash = %s, but SHA-256 over the shipped bytes with the hash slot zeroed is %s.\n"+
			"The hash must name the item AS SHIPPED (truncate, then hash) -- a hash over anything else "+
			"names a body no surface holds, and IS-APR-2 makes the phone echo it verbatim rather than "+
			"recompute it, so nothing downstream can correct the mismatch.\nitem: %s", hash, got, payloads[0])
	}
}

// ---- IS-LIFE-2: the four daemon-observed resolutions ------------------------

// TestApprovalResolved_ANewerRequestSupersedesTheOlderOne. IS-LIFE-2 names `superseded`
// explicitly, and IS-LIFE-3 says why the target is the SESSION: a roster record "cannot hold two
// pending approvals for one session", so a second pending request for one session is a state the
// lifecycle does not have. Without this the first card sits on the phone forever, and IS-LIFE-3's
// retention exemption never lifts for it either.
func TestApprovalResolved_ANewerRequestSupersedesTheOlderOne(t *testing.T) {
	sk := assemble(t)
	sk.captureInteractions("s-sup", newCaptureAdapter(pendingApprovalInteraction("req-1", "first")),
		adapter.HookPayload{Event: "PermissionRequest"})
	first := awaitItems(t, sk, "s-sup", 1)[0]
	firstID := itemString(t, first, "item_id")

	sk.captureInteractions("s-sup", newCaptureAdapter(pendingApprovalInteraction("req-2", "second")),
		adapter.HookPayload{Event: "PermissionRequest"})

	res := awaitResolution(t, sk, "s-sup", firstID)
	if res["decision"] != "superseded" {
		t.Errorf("decision = %v; want \"superseded\" (§3.6) -- a newer request for the same session "+
			"replaced this one", res["decision"])
	}
	if res["by"] != "agent" {
		t.Errorf("by = %v; want \"agent\" -- §3.6 attributes a cancel/supersede to the agent", res["by"])
	}
}

// TestApprovalResolved_ACLIWithdrawalCancelsTheRequest. IS-LIFE-2's `cancelled`: the CLI
// withdrew the prompt. The signal is the adapter's own -- a further record for the SAME CLI
// request id carrying a terminal status (§4) -- which is exactly what a withdrawal looks like
// from the capture side, and needs no contract change to express.
func TestApprovalResolved_ACLIWithdrawalCancelsTheRequest(t *testing.T) {
	sk := assemble(t)
	sk.captureInteractions("s-cancel", newCaptureAdapter(pendingApprovalInteraction("req-1", "write")),
		adapter.HookPayload{Event: "PermissionRequest"})
	opened := awaitItems(t, sk, "s-cancel", 1)[0]
	openedID := itemString(t, opened, "item_id")

	withdrawn := pendingApprovalInteraction("req-1", "write")
	withdrawn.Status = adapter.StatusDeclined
	sk.captureInteractions("s-cancel", newCaptureAdapter(withdrawn), adapter.HookPayload{Event: "Notification"})

	res := awaitResolution(t, sk, "s-cancel", openedID)
	if res["decision"] != "cancelled" {
		t.Errorf("decision = %v; want \"cancelled\" (§3.6) -- the CLI withdrew the prompt and every "+
			"surface must dismiss the card", res["decision"])
	}
	if res["by"] != "agent" {
		t.Errorf("by = %v; want \"agent\"", res["by"])
	}
}

// TestApprovalResolved_TheDesktopAnsweringResolvesLocally. IS-LIFE-2's `answered_locally`, and
// the one path with no adapter event at all: the owner answered at the machine, so the pending
// interaction leaves the session's status WITHOUT a remote decision ever arriving. That
// transition is the only observation the daemon gets, and it is enough.
func TestApprovalResolved_TheDesktopAnsweringResolvesLocally(t *testing.T) {
	sk := assemble(t)
	sk.captureInteractions("s-local", newCaptureAdapter(pendingApprovalInteraction("req-1", "write")),
		adapter.HookPayload{Event: "PermissionRequest"})
	opened := awaitItems(t, sk, "s-local", 1)[0]
	openedID := itemString(t, opened, "item_id")

	// The session is waiting on the permission, then is not: the owner answered it at the desk.
	sk.emitStatus("s-local", status.Status{
		Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission})
	sk.emitStatus("s-local", status.Status{
		Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone})

	res := awaitResolution(t, sk, "s-local", openedID)
	if res["decision"] != "answered_locally" {
		t.Errorf("decision = %v; want \"answered_locally\" (§3.6) -- the machine answered and the "+
			"phone's card is now stale", res["decision"])
	}
	if res["by"] != "owner" {
		t.Errorf("by = %v; want \"owner\"", res["by"])
	}
}

// TestApprovalResolved_AnApprovalStillWaitingIsNotResolvedByAnyStatusEmit is the negative
// control for the case above, and it is the one that would let the rule ship broken: a resolver
// that fires on ANY status emit dismisses a live card the moment the session reports anything at
// all, and the owner loses the request the machine is blocked on.
func TestApprovalResolved_AnApprovalStillWaitingIsNotResolvedByAnyStatusEmit(t *testing.T) {
	sk := assemble(t)
	sk.captureInteractions("s-live", newCaptureAdapter(pendingApprovalInteraction("req-1", "write")),
		adapter.HookPayload{Event: "PermissionRequest"})
	awaitItems(t, sk, "s-live", 1)

	sk.emitStatus("s-live", status.Status{
		Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone})
	sk.emitStatus("s-live", status.Status{
		Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission})

	time.Sleep(500 * time.Millisecond) // several append windows: a wrong resolution would have landed
	for _, item := range interactionItems(t, sk, "s-live") {
		if item["kind"] == adapter.KindApprovalResolved {
			t.Fatalf("an approval_resolved landed for a request still waiting: %v. The request is "+
				"live -- the session is IN the permission state -- and dismissing its card here loses "+
				"the one thing telling the owner the machine is blocked", item)
		}
	}
}

// ---- IS-ST-2: the orphan sweep ---------------------------------------------

// TestOrphanSweep_InstanceDeathClosesEveryOpenItemBeforeTheTerminalSessionStatus.
//
// IS-ST-2 (Unwanted): "IF a session's agent instance dies with items still in_progress, THEN the
// daemon SHALL close each with failed before the session's terminal session_status. A transcript
// SHALL NOT be left with a permanently spinning card."
//
// The approval carries a second obligation on the same event: IS-LIFE-2 is unconditional, so an
// unresolved request whose agent is gone must STILL reach a resolution -- otherwise the phone's
// IS-LIFE-3 retention exemption never lifts and the card is both unanswerable and unevictable.
func TestOrphanSweep_InstanceDeathClosesEveryOpenItemBeforeTheTerminalSessionStatus(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print SWEEP\nidle 60s\n")

	sk.captureInteractions(m.ID, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindToolRun, Status: adapter.StatusInProgress, Ref: "call-1",
		Tool: "Bash", Action: adapter.ToolAction{Type: "execute", Command: "sleep 300"},
	}), adapter.HookPayload{Event: "PreToolUse"})
	sk.captureInteractions(m.ID, newCaptureAdapter(pendingApprovalInteraction("req-1", "write src/main.rs")),
		adapter.HookPayload{Event: "PermissionRequest"})
	opened := awaitItems(t, sk, m.ID, 2)
	openIDs := map[string]string{} // item_id -> kind
	for _, it := range opened {
		openIDs[itemString(t, it, "item_id")] = itemString(t, it, "kind")
	}
	if len(openIDs) != 2 {
		t.Fatalf("want two distinct open items before the sweep; got %v", openIDs)
	}

	// The agent instance dies. endSession is the daemon's OnSessionEnd hook -- the one event
	// fired for a shim exit, a lost session and a delete alike.
	sk.endSession(m.ID)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		items := interactionItems(t, sk, m.ID)
		closed := map[string]bool{}
		resolved := false
		statusAt := -1
		for i, it := range items {
			if it["kind"] == adapter.KindSessionStatus {
				statusAt = i
			}
			if it["kind"] == adapter.KindApprovalResolved {
				resolved = true
			}
			if it["status"] == adapter.StatusFailed {
				closed[itemString(t, it, "item_id")] = true
			}
		}
		if len(closed) == 2 && resolved && statusAt == len(items)-1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the sweep never completed. Want a `failed` record for BOTH open items %v, one "+
		"approval_resolved (IS-LIFE-2 is unconditional -- an unresolved request whose agent is gone "+
		"still resolves, or the phone's IS-LIFE-3 exemption never lifts), and a terminal "+
		"session_status LAST (IS-ST-2 orders the failures before it).\nJournal: %v",
		openIDs, interactionItems(t, sk, m.ID))
}
