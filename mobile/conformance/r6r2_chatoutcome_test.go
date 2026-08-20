package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for Wave R6 review ROUND 2, on the REAL facade over the REAL
// relay: the two M3 reads have an ANSWER, and until this file existed nothing anywhere drove
// one back into a transcript. The reviewer's grep is the finding: "NOTHING anywhere exercises
// adoptInteractionRead -- mobile/*_test.go, mobile/conformance and internal/phonecore return
// no hit", while docs/verification/r6-chat.md claimed both rows GREEN "(wire + daemon +
// gateway + facade + view)".
//
// What each test here measures, and whether it was RED when it was written:
//
//   - TheDetailReplyReplacesTheClippedBody: **RED**. `adoptInteractionRead` handed the reply
//     to `ItemStore.Apply`, which drops a record that does not strictly advance the item's
//     cursor and any record after a terminal status -- and the daemon's detail reply carries
//     NO cursor and the tapped card is `completed`. So the fetch folded nothing while the
//     press reported success.
//   - APageIsHeldEvenWhenTheSessionIsAtItsRetentionBound: **RED**. `insertLocked` puts the
//     page at the front and `trimLocked` evicts oldest-first, so the page and the trim target
//     the same end.
//   - AHistoryPageBecomesTheTranscript: **GREEN when written**, and it is here anyway as the
//     reference-path fence: it is the only test in the tree that drives a claimed reply all
//     the way from the relay into `App.ReadTranscript`, which is exactly the path findings B1
//     and B3 showed nobody was walking. It is stated as a fence rather than as a probe,
//     because a suite that lets a passing test be read as a fixed defect is the defect this
//     round exists to catch.

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// r2Transcript is what a screen sees: this session's folded items, oldest first. The handle
// API is Count/At because gomobile binds no list type.
func r2Transcript(t *testing.T, h *harness) []swarmmobile.TranscriptItem {
	t.Helper()
	p, err := h.App.ReadTranscript(testSession, 0, 0)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	n, err := p.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	out := make([]swarmmobile.TranscriptItem, 0, n)
	for i := 0; i < n; i++ {
		it, err := p.At(i)
		if err != nil {
			t.Fatalf("At(%d): %v", i, err)
		}
		out = append(out, *it)
	}
	return out
}

// r2Item builds one wire interaction record, exactly as skeleton's toWireJournalRecord emits.
func r2Item(session string, cursor uint64, itemID, text, status string, truncated bool) schema.JournalRecord {
	payload, err := json.Marshal(map[string]any{
		"v": 1, "item_id": itemID, "kind": "agent_message", "text": text,
		"status": status, "truncated": truncated,
	})
	if err != nil {
		panic(err)
	}
	return schema.JournalRecord{
		Cursor: cursor, SessionID: session, Type: "interaction", Item: payload,
	}
}

// r2Ready brings the phone up paired, reconciled and holding one item for testSession, which
// is the state every "load earlier" is pressed from: a screen showing a conversation.
func r2Ready(t *testing.T, h *harness, anchorCursor uint64) {
	t.Helper()
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})
	h.PushEvent(r2Item(testSession, anchorCursor, "anchor", "the newest thing said", "completed", false))
	eventually(t, "the anchor item never reached the transcript", func() bool {
		return len(r2Transcript(t, h)) > 0
	})
}

// TestR6R2_AHistoryPageBecomesTheTranscriptWhenItsOutcomeIsClaimed -- the reference path,
// end to end: press, wire, reply, claim, screen.
func TestR6R2_AHistoryPageBecomesTheTranscriptWhenItsOutcomeIsClaimed(t *testing.T) {
	h := newHarness(t)
	r2Ready(t, h, 500)

	op, err := h.App.LoadEarlierInteractions(testSession, "anchor", 50)
	if err != nil {
		t.Fatalf("LoadEarlierInteractions: %v", err)
	}
	cmd := h.AwaitCommand(schema.ActionInteractionHistory)
	if cmd.OperationID != op.OperationID {
		t.Fatalf("the read reached the machine under operation id %q, not %q", cmd.OperationID, op.OperationID)
	}

	h.Reply(schema.Control{
		Op: schema.ActionInteractionHistory, EndpointID: h.Machine, SessionID: testSession,
		OperationID: op.OperationID,
		Journal: []schema.JournalRecord{
			r2Item(testSession, 10, "older-a", "said long ago", "completed", false),
			r2Item(testSession, 11, "older-b", "and then this", "completed", false),
		},
		HistoryFloor: true,
	})

	eventually(t, "the page never reached the transcript after its outcome was claimed", func() bool {
		if _, err := h.App.Outcome(op.OperationID); err != nil {
			return false
		}
		items := r2Transcript(t, h)
		if len(items) != 3 {
			return false
		}
		return items[0].ItemID == "older-a" && items[2].ItemID == "anchor"
	})

	atFloor, err := h.App.HistoryFloor(testSession)
	if err != nil {
		t.Fatalf("HistoryFloor: %v", err)
	}
	if !atFloor {
		t.Error("the machine said nothing older is retained and the phone did not record it, " +
			"so the screen offers 'load earlier' forever")
	}
	if n, err := h.App.PendingOpCount(); err != nil || n != 0 {
		t.Errorf("PendingOpCount = %d (err %v) after the read was answered; an unclaimed read "+
			"stays in flight for the life of the process", n, err)
	}
}

// TestR6R2_TheDetailReplyReplacesTheClippedBodyOnTheRealFacade is the reviewer's probe,
// frozen at the facade: the reply exactly as `handleInteractionDetail` builds it (no cursor),
// against a card exactly as a user taps one (completed, truncated).
func TestR6R2_TheDetailReplyReplacesTheClippedBodyOnTheRealFacade(t *testing.T) {
	h := newHarness(t)
	r2Ready(t, h, 500)
	h.PushEvent(r2Item(testSession, 501, "clipped", "HEAD...", "completed", true))
	eventually(t, "the clipped card never landed", func() bool {
		return len(r2Transcript(t, h)) == 2
	})

	op, err := h.App.LoadInteractionDetail(testSession, "clipped")
	if err != nil {
		t.Fatalf("LoadInteractionDetail: %v", err)
	}
	h.AwaitCommand(schema.ActionInteractionDetail)

	const whole = "HEAD AND THE WHOLE REST OF IT"
	full, err := json.Marshal(map[string]any{
		"v": 1, "item_id": "clipped", "kind": "agent_message", "text": whole,
		"status": "completed", "truncated": false, "detail": true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	h.Reply(schema.Control{
		Op: schema.ActionInteractionDetail, EndpointID: h.Machine, SessionID: testSession,
		OperationID: op.OperationID,
		// NO CURSOR, because the daemon has none to give: the body comes out of the
		// capture-time side store, not the journal.
		Journal: []schema.JournalRecord{{SessionID: testSession, Type: "interaction", Item: full}},
	})

	eventually(t, "the whole body never reached the card the user tapped", func() bool {
		if _, err := h.App.Outcome(op.OperationID); err != nil {
			return false
		}
		items := r2Transcript(t, h)
		if len(items) != 2 {
			return false
		}
		it := items[1]
		return it.ItemID == "clipped" && it.Text == whole && !it.Truncated
	})
}

// TestR6R2_APageIsHeldEvenWhenTheSessionIsAtItsRetentionBound. The screen a user presses
// "load earlier" ON is a busy session's, which is the one case the fold could not serve.
func TestR6R2_APageIsHeldEvenWhenTheSessionIsAtItsRetentionBound(t *testing.T) {
	h := newHarness(t)
	r2Ready(t, h, 1000)
	for i := 1; i < 200; i++ {
		h.PushEvent(r2Item(testSession, uint64(1000+i), "live-"+strconv.Itoa(i), "x", "completed", false))
	}
	eventually(t, "the session never filled to its retention bound", func() bool {
		return len(r2Transcript(t, h)) == 200
	})

	op, err := h.App.LoadEarlierInteractions(testSession, "anchor", 50)
	if err != nil {
		t.Fatalf("LoadEarlierInteractions: %v", err)
	}
	h.AwaitCommand(schema.ActionInteractionHistory)
	page := make([]schema.JournalRecord, 0, 3)
	for i := 0; i < 3; i++ {
		page = append(page, r2Item(testSession, uint64(10+i), "old-"+strconv.Itoa(i), "older", "completed", false))
	}
	h.Reply(schema.Control{
		Op: schema.ActionInteractionHistory, EndpointID: h.Machine, SessionID: testSession,
		OperationID: op.OperationID, Journal: page,
	})

	eventually(t, "the page was evicted by the very trim it was paged in behind", func() bool {
		if _, err := h.App.Outcome(op.OperationID); err != nil {
			return false
		}
		items := r2Transcript(t, h)
		if len(items) != 203 {
			return false
		}
		return items[0].ItemID == "old-0" && items[202].ItemID == "live-199"
	})
	if capped, err := h.App.HistoryAtCapacity(testSession); err != nil || capped {
		t.Errorf("HistoryAtCapacity = %v (err %v) after ONE small page; the phone must not "+
			"tell the reader it is full while it is holding three older items", capped, err)
	}
}
