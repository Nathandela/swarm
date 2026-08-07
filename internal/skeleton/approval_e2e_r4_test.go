package skeleton

// FAILING-FIRST (TDD RED, GG-5), part three of review finding R4: the RESOLUTION reaching the
// PHONE, over the shipped stack.
//
// The phone's consumption half was built and tested by W4 (internal/phonecore's resolveLocked,
// mobile's PendingApprovals) against hand-written records, because nothing produced one. This
// is the join: the machine's own resolver, through the append floor, the journal, a real
// gateway process, a real relay and the durable phone core, to the read the owner sees.
//
// WHAT IT PROVES THAT A UNIT TEST CANNOT. IS-LIFE-3 exempts an unresolved approval_request from
// the phone's transcript trimming until its approval_resolved lands. That exemption is what
// keeps a card the machine is blocked on from being evicted -- and it is also a LEAK if nothing
// ever resolves the request: the item becomes both unanswerable and unevictable. The exemption
// lifting is only observable end to end, because the producer and the retention rule live in
// different processes.
//
// It reuses the s19 rig on interaction_e2e_test.go's terms, and does not touch TestPBE2E1.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
)

// TestInteractionE2E_AResolvedApprovalDismissesThePhoneCard.
func TestInteractionE2E_AResolvedApprovalDismissesThePhoneCard(t *testing.T) {
	rig := newS19Rig(t)
	rig.Pair()
	rig.StartGateway()
	rig.Eventually("the machine's reconcile record reached the phone", func() bool {
		return rig.Summary().Reconciled
	})

	sessionID := rig.LaunchOnMachine("print E2E_RESOLVE\nidle 600s\n")
	rig.Eventually("the phone's roster shows the session the machine launched", func() bool {
		return rig.RosterHas(sessionID)
	})
	_, localID, ok := protocol.ParseID(sessionID)
	if !ok {
		t.Fatalf("owner Launch returned %q, which is not a namespaced id", sessionID)
	}

	// One CLI request, then the CLI WITHDRAWING it -- the same ref reaching a terminal status,
	// which is IS-LIFE-2's `cancelled` path as the capture side sees it.
	withdrawn := pendingApprovalInteraction("e2e-withdrawn", "write src/main.rs")
	withdrawn.Status = adapter.StatusDeclined
	script := &interactionScript{items: [][]adapter.Interaction{
		{pendingApprovalInteraction("e2e-withdrawn", "write src/main.rs")},
		{withdrawn},
	}}
	rig.sk.adapterFor = func(string) (adapter.Adapter, bool) {
		return &captureAdapter{Adapter: newPlainAdapter(), script: script}, true
	}
	token := hookTokenFor(t, rig.stateDir, localID)

	post := func(seq uint64, event string) {
		t.Helper()
		if err := hookclient.Post(rig.sk.SocketPath(), engine.Callback{
			SessionID: localID, Token: token, Sequence: seq, Event: event,
		}); err != nil {
			t.Fatalf("post hook %d: %v", seq, err)
		}
	}

	// ---- the card arrives, unanswered --------------------------------------
	post(1, "PermissionRequest")
	var cardID string
	rig.Eventually("the phone holds the pending approval card", func() bool {
		pending := readPendingApprovals(t, rig)
		if len(pending) == 1 {
			cardID = pending[0].ItemID
			return true
		}
		return false
	})

	// ---- the CLI withdraws it, and the card must dismiss --------------------
	post(2, "Notification")
	rig.Eventually("the approval_resolved reached the phone's transcript", func() bool {
		for _, it := range readTranscript(t, rig, sessionID) {
			if it.Kind == "approval_resolved" {
				return true
			}
		}
		return false
	})
	pending := readPendingApprovals(t, rig)
	if len(pending) != 0 {
		t.Fatalf("PendingApprovals still holds %d card(s) %v after the request resolved; want none. "+
			"IS-LIFE-2 makes every approval_request reach exactly one approval_resolved so a STALE "+
			"CARD DISMISSES ON EVERY SURFACE -- and until it does, IS-LIFE-3's retention exemption "+
			"keeps the item unevictable as well as unanswerable%s",
			len(pending), transcriptIDs(pending), rig.gatewayTail())
	}

	// The request itself is still IN the transcript: a resolution dismisses a card, it does not
	// delete history (the transcript is a cursor-ordered log, IS-LAYER-3).
	held := false
	for _, it := range readTranscript(t, rig, sessionID) {
		if it.ItemID == cardID {
			held = true
		}
	}
	if !held {
		t.Errorf("the resolved approval_request %q vanished from the transcript. Resolving lifts the "+
			"IS-LIFE-3 retention EXEMPTION; it does not evict the record, and a transcript that "+
			"deletes what it answered cannot show the owner what was decided", cardID)
	}
}
