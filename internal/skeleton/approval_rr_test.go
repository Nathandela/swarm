package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the RE-REVIEW of finding R4.
//
// R4 built IS-LIFE-2's five resolution paths around one rule: "one pending approval per
// session", so a second pending request for a session SUPERSEDES the first. The rule is right
// (IS-LIFE-3: a roster record "cannot hold two pending approvals for one session"), but the
// implementation identifies "a second request" by the SESSION alone, so a CLI that re-announces
// its OWN still-pending request -- the same adapter Ref, therefore the same minted item_id --
// makes the daemon supersede the very card it is re-opening.
//
// WHY THAT IS NOT COSMETIC. Two rules break at once:
//
//   - IS-LIFE-2 ("every approval_request SHALL reach EXACTLY ONE approval_resolved"): the
//     spurious `superseded` is the request's first resolution, and its real one -- allowed,
//     cancelled, expired -- is its second.
//   - IS-LIFE-3's retention exemption, on the phone: ItemStore.resolveLocked marks the request
//     Resolved off that record, which drops it out of PendingApprovals() and LIFTS the trimming
//     exemption. The owner's card disappears from the one surface that shows it while the CLI is
//     still blocked waiting for the answer, and the daemon still holds the request pending.
//
// A re-announcement is ordinary CLI behaviour, not an exotic input: a hook that fires again
// while a permission is outstanding (spike-SB captured Claude Code's Notification firing beside
// PermissionRequest) shapes the same pending interaction under the same Ref, which is exactly
// what Ref is for -- interaction.go's itemIDLocked folds successive records of ONE interaction
// under one item_id.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
)

// TestApprovalResolved_ARequestIsNotSupersededByItsOwnReAnnouncement.
func TestApprovalResolved_ARequestIsNotSupersededByItsOwnReAnnouncement(t *testing.T) {
	sk := assemble(t)
	sk.captureInteractions("s-reann", newCaptureAdapter(pendingApprovalInteraction("req-1", "write src/main.rs")),
		adapter.HookPayload{Event: "PermissionRequest"})
	first := awaitItems(t, sk, "s-reann", 1)[0]
	firstID := itemString(t, first, "item_id")

	// The SAME pending request, announced a second time under the SAME CLI ref.
	sk.captureInteractions("s-reann", newCaptureAdapter(pendingApprovalInteraction("req-1", "write src/main.rs")),
		adapter.HookPayload{Event: "Notification"})
	time.Sleep(600 * time.Millisecond) // several append windows: a wrong resolution would have landed

	for _, it := range interactionItems(t, sk, "s-reann") {
		if it["kind"] != adapter.KindApprovalResolved {
			continue
		}
		t.Fatalf("the still-pending request %s was resolved by its OWN re-announcement: %v.\n"+
			"A second record for the SAME item_id is the same request, not a second one -- "+
			"IS-LIFE-3's \"one pending approval per session\" is what `superseded` names, and "+
			"superseding an item with itself breaks IS-LIFE-2's exactly-one guarantee AND lifts "+
			"the phone's IS-LIFE-3 retention exemption for a card the CLI is still blocked on",
			firstID, it)
	}

	// And the daemon must still hold it: the tuple an arriving approve is validated against
	// (IS-LIFE-4) is the whole reason the card is answerable at all.
	sk.itemMu.Lock()
	ap := sk.approvals["s-reann"]
	sk.itemMu.Unlock()
	if ap == nil || ap.itemID != firstID {
		t.Fatalf("after the re-announcement the daemon holds %v as pending for the session; want the "+
			"same request %s -- a re-announcement RESTAMPS the binding tuple (the phone folds the "+
			"newer record over the older one, so the hash and the expiry it echoes are the newer "+
			"record's) but it does not open a different request", ap, firstID)
	}
}
