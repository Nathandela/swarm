package remotegw

// FAILING-FIRST (TDD RED, GG-5) for the RE-REVIEW: the append floor's record collapse is
// applied to an `approval_request`, which interaction-schema.md IS-DELTA-3 exempts.
//
// IS-DELTA-3 scopes ADR-010 §7's record collapse to exactly two kinds -- "`tool_run` and
// `file_change` are subject instead to ADR-010 §7's record collapse ... EVERY REMAINING KIND
// SHALL KEEP ITS OWN RECORD" -- and then says so again for this one: "*Never merged* and *never
// delayed* are different guarantees, and only the first is compatible with the ceiling: an
// `approval_request` waits at most one window, at the front."
//
// pendingItem.fold applies the union to every kind that is not `agent_message`, so two records
// of ONE approval_request landing inside one window are re-marshalled into one. The existing
// fence (TestItemAdmission_ApprovalRequestHeadsTheQueueAndIsNeverMerged) states the rule in as
// many words -- "an approval_request is NEVER merged -- its bytes are the content the daemon
// hashed (IS-APR-2)" -- but only ever offers the item ONCE, so the fold is never reached.
//
// WHAT IT COSTS NOW THAT THE ITEM CARRIES A DIGEST. R4 made §3.5's `content_hash` real: it is
// SHA-256 over the item AS SHIPPED with its own slot zeroed, and daemon.RecordInteractionRaw's
// contract says the floor "forwards an UNMERGED item byte-exact -- which is what keeps an
// approval_request's bytes the bytes the daemon hashed (IS-APR-2)". A union re-marshals the
// object, so the shipped card carries a digest that does not name it: nothing holding the item
// can re-derive the hash, which is the only property that makes it a binding over CONTENT
// rather than an opaque token.

import (
	"encoding/json"
	"testing"
)

// TestItemAdmission_TwoRecordsOfOneApprovalRequestAreNeverCollapsed.
func TestItemAdmission_TwoRecordsOfOneApprovalRequestAreNeverCollapsed(t *testing.T) {
	adm, log, clk := newAdmissionHarness()
	offerItem(t, adm, "m/s1", itemJSON(t, "seed", "session_status", map[string]any{"process": "running"}))

	open := itemJSON(t, "ap1", "approval_request", map[string]any{
		"summary": "Bash: rm -rf build", "content_hash": "beef", "expires_at": "2026-08-07T12:02:00Z",
		"mode": "card", "status": "in_progress",
		"decisions": []any{map[string]any{"id": "accept", "label": "Allow"}},
	})
	// The CLI withdraws it inside the SAME window: a second record for one item_id.
	withdrawn := itemJSON(t, "ap1", "approval_request", map[string]any{"status": "declined"})
	offerItem(t, adm, "m/s1", open)
	offerItem(t, adm, "m/s1", withdrawn)

	for i := 0; i < 4; i++ {
		clk.Advance(DefaultAppendWindow)
		if err := adm.Flush(); err != nil {
			t.Fatalf("Flush: %v", err)
		}
	}

	var approvals []json.RawMessage
	for _, rec := range log.all() {
		if decodeItem(t, rec.item).Kind == "approval_request" {
			approvals = append(approvals, rec.item)
		}
	}
	if len(approvals) != 2 {
		t.Fatalf("the floor released %d approval_request record(s) for two offered; want 2. "+
			"IS-DELTA-3 scopes record collapse to tool_run and file_change and says every remaining "+
			"kind KEEPS ITS OWN RECORD, and again for this one: an approval_request is never merged, "+
			"only delayed at most one window. Released: %s", len(approvals), approvals)
	}
	if string(approvals[0]) != string(open) {
		t.Errorf("the pending approval_request was rewritten in flight:\n got %s\nwant %s\n"+
			"its bytes ARE the content the daemon hashed -- §3.5's content_hash is SHA-256 over the "+
			"item as shipped, so a field-wise union produces a card carrying a digest that does not "+
			"name it (IS-APR-2 forbids the phone recomputing one)", approvals[0], open)
	}
	if string(approvals[1]) != string(withdrawn) {
		t.Errorf("the withdrawal record was rewritten in flight:\n got %s\nwant %s", approvals[1], withdrawn)
	}
}
