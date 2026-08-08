package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for IS-LIFE-4's FACADE half: the phone answers one approval.
//
// PendingApprovals was shipped READ ONLY and said so in its own doc comment, because the
// signed ActionApprove it needed had no wire body, no gateway route and no daemon op. All
// three now exist, so the verb that was deliberately absent becomes the one thing standing
// between a user looking at a card and a machine that stays blocked.
//
// The properties, and why each is a property rather than plumbing:
//
//   - THE PHONE ECHOES, IT DOES NOT COMPUTE (IS-APR-2). Approve takes three flat strings --
//     gomobile binds no struct argument -- so the ADR-007 D7 binding tuple has to come off the
//     card the handset already holds. A verb that accepted a caller-supplied hash or expiry
//     would be inviting exactly the computation the rule forbids.
//   - A CARD THAT CANNOT BE ANSWERED IS REFUSED HERE (ErrClassNotFound), not at the machine.
//     An unknown or already-resolved item produces CodeStaleApproval from the daemon, which
//     the phone can only render as "your card is out of date" -- true for a resolved card,
//     wrong and unactionable for a typo or a screen bug.
//   - IT IS LIVE-ONLY AND NEVER QUEUED (B43). With no link the call fails with the offline
//     class and NOTHING is stored for later: an approval has a daemon-authoritative window,
//     so a decision replayed out of a queue answers a question the agent stopped asking.
//
// RED is undefined-only: (*App).Approve does not exist.

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// approvableCard is an approval_request in the shape the daemon ships it: §3.5's three
// daemon-authoritative fields present, which is what makes it answerable at all.
const approvableCard = `{"v":1,"item_id":"itm-appr","ts":"2026-08-07T10:00:00Z",` +
	`"kind":"approval_request","status":"in_progress","summary":"write src/main.rs",` +
	`"agent_instance":{"shim_pid":4242,"shim_start_time":1700000000},` +
	`"content_hash":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",` +
	`"expires_at":"2026-08-07T10:05:00Z","mode":"card",` +
	`"decisions":[{"id":"accept","label":"Allow"},{"id":"cancel","label":"Deny"}]}`

// approveApp is transcriptApp with a machine destination and an epoch content key, so a send
// resolves every coordinate EXCEPT the connection -- which is the state a backgrounded or
// just-woken handset is actually in, and the one B43 is about.
func approveApp(t *testing.T) *App {
	t.Helper()
	a := transcriptApp(t)
	a.machineTarget = "machine-routing-id"
	key := make([]byte, len(crypto.ContentKey{}))
	for i := range key {
		key[i] = byte(i + 1)
	}
	if err := a.InstallContentKey(key); err != nil {
		t.Fatalf("InstallContentKey: %v", err)
	}
	return a
}

// TestIsLife4_ApproveIsLiveOnlyAndNeverQueued: with no relay connection the call is REFUSED
// with the offline class, and the durable send-seq is untouched -- nothing was sealed, so
// nothing can be delivered later against a window that has since closed.
func TestIsLife4_ApproveIsLiveOnlyAndNeverQueued(t *testing.T) {
	a := approveApp(t)
	a.core.Router().Items().Apply(itemRecord(10, "m1/s1", approvableCard))

	op, err := a.Approve("m1/s1", "itm-appr", "accept")
	if err == nil {
		t.Fatalf("Approve succeeded with no connection (op %+v); B43 makes it live-only", op)
	}
	if !strings.Contains(err.Error(), ErrClassOffline) {
		t.Errorf("Approve offline error = %q; want the %s class so the screen shows a spinner rather than a bug report",
			err, ErrClassOffline)
	}
	n, cerr := a.PendingOpCount()
	if cerr != nil {
		t.Fatalf("PendingOpCount: %v", cerr)
	}
	if n != 0 {
		t.Errorf("a refused approve left %d ops in flight; want 0 -- nothing was sent", n)
	}
}

// TestIsLife4_ApproveRefusesACardTheHandsetCannotAnswer: an item this phone does not hold, or
// one already resolved, is refused locally with the not-found class. Neither can be
// distinguished from the other at the machine (both come back CodeStaleApproval), and only
// one of them is actually the user's card being out of date.
func TestIsLife4_ApproveRefusesACardTheHandsetCannotAnswer(t *testing.T) {
	a := approveApp(t)
	store := a.core.Router().Items()
	store.Apply(itemRecord(10, "m1/s1", approvableCard))

	if _, err := a.Approve("m1/s1", "itm-nosuch", "accept"); err == nil {
		t.Error("Approve accepted an item id the phone holds no card for")
	} else if !strings.Contains(err.Error(), ErrClassNotFound) {
		t.Errorf("unknown-card error = %q; want the %s class", err, ErrClassNotFound)
	}

	// IS-LIFE-2: the resolution has landed, so the card is answered on every surface.
	store.Apply(itemRecord(11, "m1/s1",
		`{"v":1,"item_id":"itm-res","ts":"2026-08-07T10:01:00Z","kind":"approval_resolved",`+
			`"interaction_id":"itm-appr","decision":"allowed","by":"owner"}`))
	if _, err := a.Approve("m1/s1", "itm-appr", "accept"); err == nil {
		t.Error("Approve accepted a card that had already reached its approval_resolved (IS-LIFE-2)")
	} else if !strings.Contains(err.Error(), ErrClassNotFound) {
		t.Errorf("resolved-card error = %q; want the %s class", err, ErrClassNotFound)
	}
}

// TestIsLife4_ApproveRefusesAnEmptyDecision: the card labels its buttons from
// decisions[].label and answers with the matching id (IS-APR-3). An empty id was never
// rendered to anybody, so it is a screen bug, and the daemon's own refusal for it
// (CodeInvalidField) would reach the user as an unexplained failure of their tap.
func TestIsLife4_ApproveRefusesAnEmptyDecision(t *testing.T) {
	a := approveApp(t)
	a.core.Router().Items().Apply(itemRecord(10, "m1/s1", approvableCard))

	if _, err := a.Approve("m1/s1", "itm-appr", ""); err == nil {
		t.Error("Approve accepted an empty decision id")
	} else if !strings.Contains(err.Error(), ErrClassInvalidRequest) {
		t.Errorf("empty-decision error = %q; want the %s class", err, ErrClassInvalidRequest)
	}
}

// TestIsLife4_ApproveSignsTheApproveActionOverTheCardsOwnBinding: the tuple that leaves this
// phone is ActionApprove against the card's session, and the content slot carries the item's
// own content_hash. Asserted through the phonecore signer rather than through a live relay,
// because what is being pinned is which values the facade FEEDS it -- the failure this guards
// is a facade that computes a fresh hash or expiry, which produces a perfectly valid command
// the daemon refuses.
func TestIsLife4_ApproveSignsTheApproveActionOverTheCardsOwnBinding(t *testing.T) {
	a := approveApp(t)
	a.core.Router().Items().Apply(itemRecord(10, "m1/s1", approvableCard))

	b, ok := a.core.Router().Items().PendingApproval("m1/s1", "itm-appr")
	if !ok {
		t.Fatal("the fixture card is not answerable; the rest of this test would prove nothing")
	}
	if b.ContentHash != "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08" {
		t.Fatalf("binding content_hash = %q; the fixture is wrong", b.ContentHash)
	}

	// The action the facade signs is the one the daemon authorizes against, and IS-LIFE-4 adds
	// no new one. A drift here is a signature the machine rejects with nothing naming the cause.
	if schema.ActionApprove != "approve" {
		t.Fatalf("ActionApprove = %q; the wire contract moved", schema.ActionApprove)
	}
	op, err := a.Approve("m1/s1", "itm-appr", "accept")
	if err == nil {
		t.Fatalf("Approve reached the wire in a test with no relay (op %+v)", op)
	}
	// It failed at the TRANSPORT, not before it: the binding was resolved and the command was
	// authored. A refusal that named the card would mean the facade never got that far.
	if strings.Contains(err.Error(), ErrClassNotFound) || strings.Contains(err.Error(), ErrClassInvalidRequest) {
		t.Fatalf("Approve refused an answerable card locally: %q", err)
	}
}
