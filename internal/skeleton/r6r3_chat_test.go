package skeleton

// Wave R6 review ROUND 3, finding F1: B5's item-boundary fix was correct and UNFENCED ON THE
// PRODUCTION PATH.
//
// All three of B5's tests (r6fix_chat_test.go:312, :342, :359) call `historyPageStart`
// DIRECTLY, on a hand-built slice. None drives `interactionHistory`, so reverting the CALL
// SITE in chat.go from `start := historyPageStart(older, limit)` back to the pre-fix record
// trim (`start := 0; if len(older) > limit { start = len(older) - limit }`) left the whole
// suite GREEN -- the fix could be deleted from the served path without a single test noticing.
// The sibling at r6fix_chat_test.go:375 does exactly the right thing for B4a and says so in
// its own comment, so the distinction was understood and simply not applied here.
//
// This file is the missing half: the page the DAEMON SERVES, over a real journal holding a
// real multi-record `agent_message`, asserted to begin on that item's FIRST record.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestR6R3_TheServedHistoryPageNeverBeginsInTheMiddleOfAnItem drives
// `sk.api.InteractionHistory` -- the exact seam protocol's handler calls -- over a session
// whose middle item is an `agent_message` grown across THREE journal records under one
// item_id (IS-DELTA-1 increments, reconstructed by concatenation in cursor order).
//
// The limit is chosen so the pre-fix record trim would have cut through that item: 4 records
// are strictly older than the anchor, and a limit of 2 slices `older[2:]` -- the message's
// second and third increments with its head missing. The phone folds by item_id and cannot
// know a head is absent, so it renders that tail as the whole of the agent's message; and the
// head is then permanently unreachable, because the next page asks for what is older than the
// item's FIRST record, which is below what was just delivered.
func TestR6R3_TheServedHistoryPageNeverBeginsInTheMiddleOfAnItem(t *testing.T) {
	sk, clock := assembleOnPinnedFloorClock(t)
	const local = "s-r3-page-boundary"

	// One offer per append window: ADR-010 §7 admits at most one item per window, and each
	// admitted increment is its own journal record under the same item_id. That is the shape
	// the whole finding is about, so the rig produces it rather than hand-building it.
	offer := func(items ...adapter.Interaction) {
		sk.captureInteractions(local, newCaptureAdapter(items...), adapter.HookPayload{Event: "PostToolUse"})
		clock.advance(remotegw.DefaultAppendWindow)
	}
	offer(adapter.Interaction{Kind: adapter.KindUserMessage, Text: "ask", Source: adapter.SourceOwner})
	awaitItems(t, sk, local, 1)
	for _, part := range []string{"alpha ", "beta ", "gamma"} {
		offer(adapter.Interaction{
			Kind: adapter.KindAgentMessage, Status: adapter.StatusInProgress, Ref: "msg-grown", Text: part,
		})
	}
	awaitItems(t, sk, local, 4)
	offer(adapter.Interaction{Kind: adapter.KindUserMessage, Text: "and again", Source: adapter.SourceOwner})
	records := awaitItems(t, sk, local, 5)

	anchor, _ := records[len(records)-1]["item_id"].(string)
	grown, _ := records[1]["item_id"].(string)
	if anchor == "" || grown == "" || anchor == grown {
		t.Fatalf("the rig did not produce the shape this test is about: anchor %q, grown item %q, "+
			"records %v", anchor, grown, records)
	}
	// The three increments must have landed as three SEPARATE records under one item_id --
	// if the floor had merged them the trim could not cut through the item and the test would
	// prove nothing.
	if records[1]["item_id"] != records[2]["item_id"] || records[2]["item_id"] != records[3]["item_id"] {
		t.Fatalf("the agent_message did not grow across three records under one item_id: %v %v %v",
			records[1]["item_id"], records[2]["item_id"], records[3]["item_id"])
	}

	session := protocol.NamespacedID(sk.api.endpointID, local)
	page, floor, code, err := sk.api.InteractionHistory(session, anchor, 2)
	if err != nil {
		t.Fatalf("interaction_history: code %q err %v", code, err)
	}
	if len(page) == 0 {
		t.Fatal("the served page is empty; four records are strictly older than the anchor")
	}
	if got := historyItemID(page[0]); got != grown {
		t.Fatalf("the served page begins on item %q, want the grown message %q", got, grown)
	}
	// The page's first record must be the item's FIRST record. Its identity is its cursor:
	// the smallest cursor any record of that item holds in the journal.
	first := firstCursorOfItem(t, sk, local, grown)
	if page[0].Cursor != first {
		t.Fatalf("the served page begins at cursor %d, which is INSIDE item %q -- its first "+
			"record is at cursor %d. A page that starts mid-item delivers the tail of the "+
			"agent's message as the whole of it, and its head can never be paged back: the "+
			"next request asks for what is older than cursor %d, which is below these records.",
			page[0].Cursor, grown, first, first)
	}
	if n := countRecordsOfItem(page, grown); n != 3 {
		t.Fatalf("the served page carries %d of item %q's 3 records; a whole item or none", n, grown)
	}
	if floor {
		t.Errorf("the page claims the honest floor while the older user_message was left behind")
	}
}

// firstCursorOfItem is the cursor of the oldest journal record belonging to itemID.
func firstCursorOfItem(t *testing.T, sk *Daemon, session, itemID string) uint64 {
	t.Helper()
	res, err := sk.Core().JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	for _, rec := range res.Events {
		if rec.SessionID != session {
			continue
		}
		if historyItemID(protocol.JournalRecord{Type: string(rec.Type), Item: rec.Payload}) == itemID {
			return rec.Cursor
		}
	}
	t.Fatalf("no journal record for item %q of session %q", itemID, session)
	return 0
}

func countRecordsOfItem(page []protocol.JournalRecord, itemID string) int {
	n := 0
	for _, rec := range page {
		if historyItemID(rec) == itemID {
			n++
		}
	}
	return n
}
