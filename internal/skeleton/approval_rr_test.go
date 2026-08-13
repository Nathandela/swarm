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

// TestApprovalRequest_AReAnnouncementDoesNotForgetAnAnswerAlreadyTyped is the review finding of
// 2026-08-13 (mirror-m1.md M1.8), and it is the second half of the sentence the test above
// starts. A re-announcement is by design NOT a supersede -- and yet openApprovalLocked replaced
// the binding with a fresh pendingApproval on that same branch, dropping the two fields M1.2
// added to it.
//
// WHAT THE DROP COSTS, both halves being ones M1.2 built on purpose:
//
//   - `applied`/`appliedOp` are what tell the OBSERVATION who answered. Forgetting them
//     attributes the phone's own answer to `answered_locally` by `owner` -- a decision put in
//     the mouth of somebody who never touched the keyboard.
//   - `ap.applied != ""` is approveInteraction's second case, the one that refuses a
//     re-delivered approve during the applied-but-unobserved window. Forgetting it reopens a
//     SECOND keystroke into a dialog that has one answer left in it.
//
// IT IS LATENT FOR CLAUDE TODAY and pinned anyway: `approvalFrom` mints its Ref from
// `prefix+tool+":"+ReceivedAtMs`, and ReceivedAtMs is stamped daemon-side per hook arrival, so
// claude's re-announcement mints a NEW item_id and supersedes instead of folding. The first
// adapter with a stable approval ref walks straight into it, and nothing about this daemon's
// side of the contract says an adapter may not have one.
func TestApprovalRequest_AReAnnouncementDoesNotForgetAnAnswerAlreadyTyped(t *testing.T) {
	sk := assemble(t)
	sk.captureInteractions("s-applied", newCaptureAdapter(pendingApprovalInteraction("req-1", "write src/main.rs")),
		adapter.HookPayload{Event: "PermissionRequest"})
	first := awaitItems(t, sk, "s-applied", 1)[0]
	firstID := itemString(t, first, "item_id")

	// The daemon has TYPED the phone's answer and is waiting to observe the dialog leave --
	// exactly the state approveInteraction leaves behind between the keystroke and the record.
	sk.itemMu.Lock()
	ap := sk.approvals["s-applied"]
	if ap == nil {
		sk.itemMu.Unlock()
		t.Fatal("the daemon holds no pending approval for the session it just journalled a card for")
	}
	ap.applied, ap.appliedOp = resolveAllowed, "op-typed"
	sk.itemMu.Unlock()

	// The SAME pending request, announced a second time under the SAME CLI ref.
	sk.captureInteractions("s-applied", newCaptureAdapter(pendingApprovalInteraction("req-1", "write src/main.rs")),
		adapter.HookPayload{Event: "Notification"})

	sk.itemMu.Lock()
	ap = sk.approvals["s-applied"]
	sk.itemMu.Unlock()
	if ap == nil || ap.itemID != firstID {
		t.Fatalf("the re-announcement replaced the pending request %s with %v", firstID, ap)
	}
	if ap.applied != resolveAllowed || ap.appliedOp != "op-typed" {
		t.Fatalf("after its own re-announcement the request records applied=%q appliedOp=%q; want "+
			"%q/%q. The keystroke really was typed into the CLI's dialog, and a binding that forgets "+
			"it attributes the phone's answer to an owner who never touched the keyboard AND reopens "+
			"the second-keystroke window `ap.applied != \"\"` exists to close",
			ap.applied, ap.appliedOp, resolveAllowed, "op-typed")
	}
}
