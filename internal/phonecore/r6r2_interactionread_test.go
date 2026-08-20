package phonecore

// FAILING-FIRST (TDD RED, GG-5) for Wave R6 review ROUND 2 -- the two blockers the round-1
// fix-pack left in the phone's transcript model, both PROVED by a reviewer's temporary probe
// and frozen here as permanent fences.
//
// (1) AN interaction_detail REPLY CANNOT FOLD. `handleInteractionDetail` builds
//     `JournalRecord{SessionID, Type:"interaction", Item: full}` with NO cursor, and
//     `adoptInteractionRead` handed it to `ItemStore.Apply`. `applyLocked` drops any record
//     whose cursor does not STRICTLY advance the item (a clipped card's item is already at
//     cursor N > 0) and any record arriving after a terminal status (a clipped tool_run or
//     agent_message is `completed`). The reviewer's probe: a completed truncated item at
//     cursor 7, then the detail reply exactly as the daemon builds it -> `applied=false`, the
//     text still "HEAD...", `truncated` still true. Forcing the cursor above the high water
//     does not help while the item is terminal, and with a non-terminal item the
//     agent_message branch CONCATENATES (`next.Text = prev.Text + w.Text`) and produces
//     "HEAD...HEAD AND THE WHOLE REST OF IT" -- a garbled body presented as the whole of it,
//     which is the ambiguity IS-CAP-3 exists to forbid.
//
//     The detail read is therefore NOT a delta and must not travel the delta path: it is a
//     REPLACEMENT of one held item's body by the pre-truncation original, which is what
//     ApplyDetail is.
//
// (2) "LOAD EARLIER" IS A NO-OP AT THE RETENTION BOUND. `insertLocked` puts a page's older
//     records at the FRONT and `trimLocked` evicts oldest-first, so the page and the trim
//     target the same end. The reviewer's probe: 200 items held (== MaxItemsPerSession), then
//     a 50-record page of strictly older items -> 200 held, oldest item UNCHANGED, 0 of the
//     50 survived. The screen goes on offering "load earlier" (nothing sets the floor), so
//     the user taps forever and nothing moves -- the livelock `historyPageStart`'s own doc
//     comment says it refused to create on the daemon side, reproduced on the phone.
//
//     A page is therefore folded through ApplyPage, whose items are held in a SECOND bounded
//     region that the live trim does not touch, and which refuses a page WHOLE rather than
//     landing half of one: half a page is a hole in the middle of a conversation, which is the
//     silent bridge ADR-017 forbids one plane over.
//
// (3) And a durable outcome is a VERDICT, not a payload: RecordOutcome stored the whole
//     schema.Control, journal records and all, into persisted state that nothing prunes.

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// r2item builds one ordinary interaction record.
func r2item(session string, cursor uint64, itemID, kind, text, status string, truncated bool) schema.JournalRecord {
	payload, err := json.Marshal(map[string]any{
		"v": 1, "item_id": itemID, "kind": kind, "text": text,
		"status": status, "truncated": truncated,
	})
	if err != nil {
		panic(err)
	}
	return schema.JournalRecord{
		SessionID: session, Cursor: cursor, Type: RecordTypeInteraction, Item: payload,
	}
}

// r2detail builds the detail reply EXACTLY as internal/protocol's handleInteractionDetail
// does: one interaction record carrying the full pre-truncation body, and NO CURSOR. The
// daemon has no cursor to put there -- the body comes out of the side store, not the journal
// -- and a consumer that needed one would be paging a reply.
func r2detail(session, itemID, kind, full string) schema.JournalRecord {
	payload, err := json.Marshal(map[string]any{
		"v": 1, "item_id": itemID, "kind": kind, "text": full,
		"status": StatusCompleted, "truncated": false, "detail": true,
	})
	if err != nil {
		panic(err)
	}
	return schema.JournalRecord{SessionID: session, Type: RecordTypeInteraction, Item: payload}
}

// TestR6R2_TheDetailReplyReplacesTheClippedBodyWhereTheCardStands is the reviewer's probe,
// frozen: the reply the daemon actually sends, against the item a user actually taps.
func TestR6R2_TheDetailReplyReplacesTheClippedBodyWhereTheCardStands(t *testing.T) {
	s := NewItemStore()
	const sess, id = "m1/s1", "01JITEM"

	if !s.Apply(r2item(sess, 7, id, KindAgentMessage, "HEAD...", StatusCompleted, true)) {
		t.Fatal("the clipped card did not land")
	}

	const whole = "HEAD AND THE WHOLE REST OF IT"
	if !s.ApplyDetail(r2detail(sess, id, KindAgentMessage, whole)) {
		t.Fatal("the detail reply folded NOTHING: IS-CAP-2's whole-body read cannot reach the " +
			"card the user tapped, so the press reports success and the screen does not change")
	}

	got, ok := s.Resolve(sess, id)
	if !ok {
		t.Fatal("the item vanished")
	}
	if got.Text != whole {
		t.Errorf("text after the detail fold = %q; want %q. A detail read is a REPLACEMENT of "+
			"the clipped body, never an IS-DELTA-1 increment: concatenating it produces a "+
			"garbled body presented as the whole of it, which is the ambiguity IS-CAP-3 forbids",
			got.Text, whole)
	}
	if got.Truncated {
		t.Error("the item still reads truncated after the machine sent its whole body, so the " +
			"card goes on offering a fetch that has already happened")
	}
	if got.Cursor != 7 {
		t.Errorf("cursor moved to %d; the detail reply carries none, and an item that moved "+
			"would jump out of the conversation the reader is looking at", got.Cursor)
	}
}

// TestR6R2_ADetailForAnItemThisPhoneDoesNotHoldFoldsNothing. The reply carries no cursor, so
// there is no position to insert one at; inventing one would put a body somewhere in the
// conversation nobody can defend. IS-ENV-2's identity rule with nothing to identify.
func TestR6R2_ADetailForAnItemThisPhoneDoesNotHoldFoldsNothing(t *testing.T) {
	s := NewItemStore()
	const sess = "m1/s1"
	if !s.Apply(r2item(sess, 7, "01JHELD", KindAgentMessage, "held", StatusCompleted, false)) {
		t.Fatal("setup item did not land")
	}
	if s.ApplyDetail(r2detail(sess, "01JSTRANGER", KindAgentMessage, "a body from nowhere")) {
		t.Fatal("a detail reply for an item this phone does not hold was INSERTED; it has no " +
			"cursor, so it would land at an arbitrary position in someone's conversation")
	}
	if n := len(s.Session(sess)); n != 1 {
		t.Errorf("session holds %d items; want the one it started with", n)
	}
}

// TestR6R2_APagedBackfillSurvivesTheRetentionBound is the reviewer's second probe, frozen.
func TestR6R2_APagedBackfillSurvivesTheRetentionBound(t *testing.T) {
	s := NewItemStore()
	const sess = "m1/s1"

	// A busy session, at the bound exactly.
	for i := 0; i < MaxItemsPerSession; i++ {
		c := uint64(1000 + i)
		if !s.Apply(r2item(sess, c, "live-"+strconv.Itoa(i), KindAgentMessage, "x", StatusCompleted, false)) {
			t.Fatalf("live item %d did not land", i)
		}
	}
	if n := len(s.Session(sess)); n != MaxItemsPerSession {
		t.Fatalf("setup holds %d items; want %d", n, MaxItemsPerSession)
	}

	// One "load earlier" page of strictly older items, exactly as the daemon serves it.
	page := make([]schema.JournalRecord, 0, 50)
	for i := 0; i < 50; i++ {
		page = append(page, r2item(sess, uint64(100+i), "old-"+strconv.Itoa(i),
			KindAgentMessage, "older", StatusCompleted, false))
	}
	if !s.ApplyPage(page) {
		t.Fatal("the page was refused whole while the backfill window was empty")
	}

	held := s.Session(sess)
	if len(held) != MaxItemsPerSession+50 {
		t.Fatalf("after the page the session holds %d items; want %d. insertLocked puts a "+
			"page's older records at the FRONT and trimLocked evicts oldest-first, so the page "+
			"and the trim target the same end and the user taps 'load earlier' forever",
			len(held), MaxItemsPerSession+50)
	}
	if held[0].ItemID != "old-0" {
		t.Errorf("oldest held item is %q; want the page's oldest. The page landed and was "+
			"immediately evicted", held[0].ItemID)
	}
	if last := held[len(held)-1]; last.ItemID != "live-"+strconv.Itoa(MaxItemsPerSession-1) {
		t.Errorf("newest held item is %q; a page must never evict the LIVE tail the retention "+
			"bound exists to protect", last.ItemID)
	}
}

// TestR6R2_APageThatCannotBeHeldWholeIsRefusedWhole. Half a page is a hole in the middle of a
// conversation with nothing marking it, which is the silent bridge ADR-017 forbids. The
// refusal is what a screen renders as "this phone is holding as much of this conversation as
// it can" -- an honest stop rather than a control that does nothing forever.
func TestR6R2_APageThatCannotBeHeldWholeIsRefusedWhole(t *testing.T) {
	s := NewItemStore()
	const sess = "m1/s1"
	if !s.Apply(r2item(sess, 9000, "live", KindAgentMessage, "now", StatusCompleted, false)) {
		t.Fatal("live item did not land")
	}

	// Fill the backfill window to its bound, one page at a time.
	const page = 50
	for p := 0; p*page < MaxBackfillPerSession; p++ {
		recs := make([]schema.JournalRecord, 0, page)
		for i := 0; i < page; i++ {
			n := p*page + i
			recs = append(recs, r2item(sess, uint64(5000-n), "back-"+strconv.Itoa(n),
				KindAgentMessage, "older", StatusCompleted, false))
		}
		if !s.ApplyPage(recs) {
			t.Fatalf("page %d was refused while the window still had room", p)
		}
	}

	over := []schema.JournalRecord{
		r2item(sess, 1, "over-a", KindAgentMessage, "older still", StatusCompleted, false),
	}
	if s.ApplyPage(over) {
		t.Fatal("a page landed past the backfill bound; the phone's transcript would grow " +
			"without limit, which is the unpruned-outcomes residual reproduced on a plane " +
			"that grows per tool call")
	}
	if _, ok := s.Resolve(sess, "over-a"); ok {
		t.Error("the refused page folded part of itself anyway")
	}
	if _, ok := s.Resolve(sess, "live"); !ok {
		t.Error("the live tail was evicted to make room for history")
	}
}

// TestR6R2_ADurableOutcomeIsAVerdictAndNotAPayload. Core.RecordOutcome stored the WHOLE
// schema.Control -- journal records and all -- into persisted state, and nothing prunes
// OpOutcomes (this package's own recorded residual: "never pruned, so every launch re-offers
// every outcome ever recorded"). So each history page wrote up to `limit` full item bodies
// into the phone's durable state file permanently, and each detail read wrote the FULL
// PRE-TRUNCATION BODY -- precisely the payload that was too large to ship inline.
func TestR6R2_ADurableOutcomeIsAVerdictAndNotAPayload(t *testing.T) {
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c, err := Resume(Config{Dir: t.TempDir(), WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	ctrl := schema.Control{
		Op: "interaction_history", OperationID: "op-1", SessionID: "m1/s1",
		Journal: []schema.JournalRecord{
			r2item("m1/s1", 1, "01JA", KindAgentMessage, "a body nobody asked to keep", StatusCompleted, false),
		},
		HistoryFloor: true,
	}
	if err := c.RecordOutcome(ctrl); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	got, ok := c.State().OpOutcomes["op-1"]
	if !ok {
		t.Fatal("the outcome was not recorded at all")
	}
	if len(got.Journal) != 0 {
		t.Errorf("the durable outcome carries %d journal records; a verdict is what survives a "+
			"process death, and the records a read delivered are the live transcript's",
			len(got.Journal))
	}
	if got.Op != ctrl.Op || got.OperationID != ctrl.OperationID {
		t.Errorf("the verdict itself was damaged: %+v", got)
	}
}
