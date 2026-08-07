package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for W4's facade half: the phone's TRANSCRIPT crosses the
// gomobile boundary, and an item that lands raises an event a screen can render on
// (ADR-009, docs/specifications/interaction-schema.md).
//
// WHAT THE SURFACE MUST NOT BE, and why each test exists:
//
//  1. it must not be ReadJournal. The journal page is an in-memory log of RECORD TYPES
//     bounded by journalLogSize and rebuilt empty after every process death; the transcript
//     is the durable, folded model in phonecore (fold by item_id, concatenated agent_message
//     text, latest plan revision). Serving the transcript from the journal page would show an
//     empty conversation on every launch, behind a durable high-water that refuses the
//     redelivery -- so the content would be gone, not merely unread.
//  2. an interaction record must not feed the roster's Need. Session.Need is the verbatim
//     record type that last touched a session; an item setting it to "interaction" would
//     replace "needs_input" on the triage row with a word about carriage (IS-SS-1).
//  3. approvals are exposed READ-ONLY. Answering one is IS-LIFE-4's signed ActionApprove
//     with a new ApproveReq wire body, which this workpackage does not build -- so the phone
//     can SHOW a pending card and cannot yet answer it.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// transcriptApp is an App over an in-memory core: enough to fold records and read them back,
// with no relay, no pairing and no state directory.
func transcriptApp(t *testing.T) *App {
	t.Helper()
	core, err := phonecore.Resume(phonecore.Config{})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}
	a := &App{
		core:       core,
		events:     newDispatcher(),
		needs:      map[string]string{},
		subscribed: true,
		reconciled: true,
	}
	t.Cleanup(a.events.close)
	return a
}

// itemRecord is one wire journal record carrying an item object.
func itemRecord(cursor uint64, session, item string) schema.JournalRecord {
	return schema.JournalRecord{
		Cursor:    cursor,
		SessionID: session,
		Type:      phonecore.RecordTypeInteraction,
		Item:      json.RawMessage(item),
	}
}

// captureListener collects delivered events.
type captureListener struct{ ch chan Event }

func (l *captureListener) OnEvent(e *Event) { l.ch <- *e }

func TestTranscript_ItemsCrossTheBoundAsAPage(t *testing.T) {
	a := transcriptApp(t)
	store := a.core.Router().Items()
	store.Apply(itemRecord(10, "m1/s1",
		`{"v":1,"item_id":"itm-1","ts":"2026-08-07T10:00:00Z","turn_id":"t-1","kind":"agent_message","status":"in_progress","text":"Let me "}`))
	store.Apply(itemRecord(11, "m1/s1",
		`{"v":1,"item_id":"itm-1","ts":"2026-08-07T10:00:01Z","turn_id":"t-1","kind":"agent_message","status":"completed","text":"read it."}`))
	store.Apply(itemRecord(12, "m1/s2",
		`{"v":1,"item_id":"itm-other","ts":"2026-08-07T10:00:02Z","kind":"user_message","text":"hi"}`))

	page, err := a.ReadTranscript("m1/s1", 0, 0)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	n, err := page.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Fatalf("transcript page holds %d items for m1/s1; want 1 (two records of ONE item_id fold "+
			"into one item, and another session's item is not in this session's transcript)", n)
	}
	it, err := page.At(0)
	if err != nil {
		t.Fatalf("At(0): %v", err)
	}
	if it.Text != "Let me read it." {
		t.Errorf("TranscriptItem.Text = %q; want the RECONSTRUCTED body %q -- the phone folds the "+
			"increments (IS-DELTA-1), because a client that read `text` out of the raw body would "+
			"render the last increment as the whole message", it.Text, "Let me read it.")
	}
	if it.Kind != "agent_message" || it.Status != "completed" || it.TurnID != "t-1" || it.ItemID != "itm-1" {
		t.Errorf("TranscriptItem envelope = %+v; want kind agent_message, status completed, turn t-1, id itm-1", *it)
	}
	if it.Body == "" {
		t.Error("TranscriptItem.Body is empty. gomobile binds no map and no variant type, so the " +
			"per-kind fields of §3 cross as the item's raw JSON and are decoded by the client -- " +
			"which is also what makes an unknown kind or field free on this boundary (IS-COMPAT-1/-2)")
	}
	if next, err := page.NextCursor(); err != nil || next != 10 {
		t.Errorf("NextCursor() = %d, %v; want 10 (the item's ordering cursor is its FIRST record's)", next, err)
	}
	if _, err := page.Stale(); err != nil {
		t.Errorf("Stale(): %v -- a transcript is a chronology and reads as complete unless it says "+
			"otherwise (PB-APP-8), exactly as JournalPage.Stale does", err)
	}
}

// TestTranscript_PendingApprovalsAreExposedReadOnly. IS-LIFE-3 keeps an unresolved
// approval_request alive across a reconnect and a process death precisely so a surface can
// show it. ANSWERING it is out of this workpackage: IS-LIFE-4's decision travels as the
// signed ActionApprove with a new ApproveReq wire body, which no slice has built yet, so the
// facade exposes the card and no verb to answer it.
func TestTranscript_PendingApprovalsAreExposedReadOnly(t *testing.T) {
	a := transcriptApp(t)
	store := a.core.Router().Items()
	store.Apply(itemRecord(10, "m1/s1",
		`{"v":1,"item_id":"apr-1","ts":"2026-08-07T10:00:00Z","kind":"approval_request","status":"in_progress","summary":"write src/main.rs","mode":"card"}`))

	page, err := a.PendingApprovals()
	if err != nil {
		t.Fatalf("PendingApprovals: %v", err)
	}
	n, _ := page.Count()
	if n != 1 {
		t.Fatalf("PendingApprovals holds %d card(s); want 1", n)
	}
	it, _ := page.At(0)
	if it.ItemID != "apr-1" || it.Kind != "approval_request" {
		t.Fatalf("pending card = %+v; want the unresolved apr-1", *it)
	}

	store.Apply(itemRecord(11, "m1/s1",
		`{"v":1,"item_id":"res-1","ts":"2026-08-07T10:01:00Z","kind":"approval_resolved","status":"completed","interaction_id":"apr-1","decision":"allowed","by":"owner"}`))
	page, err = a.PendingApprovals()
	if err != nil {
		t.Fatalf("PendingApprovals after the resolution: %v", err)
	}
	if n, _ := page.Count(); n != 0 {
		t.Errorf("PendingApprovals holds %d card(s) after the resolution; want 0. IS-LIFE-2 makes every "+
			"request reach exactly one resolution so a stale card dismisses on EVERY surface -- including "+
			"one answered at the machine, which is the case this phone cannot otherwise see", n)
	}
}

// TestTranscript_AnItemRaisesItsOwnEventAndLeavesTheRosterAlone. The event is what makes the
// transcript live; the roster's Need is what must not move with it (IS-SS-1).
func TestTranscript_AnItemRaisesItsOwnEventAndLeavesTheRosterAlone(t *testing.T) {
	a := transcriptApp(t)
	l := &captureListener{ch: make(chan Event, 4)}
	a.events.setListener(l)

	a.onJournal(itemRecord(10, "m1/s1",
		`{"v":1,"item_id":"apr-1","ts":"2026-08-07T10:00:00Z","kind":"approval_request","status":"in_progress","mode":"card"}`))

	select {
	case e := <-l.ch:
		if e.Kind != "interaction" {
			t.Fatalf("event Kind = %q; want interaction -- a transcript item is not a roster event, and "+
				"a screen listening for one must not have to filter the other", e.Kind)
		}
		if e.SessionID != "m1/s1" || e.Cursor != 10 {
			t.Errorf("event = %+v; want session m1/s1 at cursor 10", e)
		}
		if e.Message != "approval_request" {
			t.Errorf("event Message = %q; want the item KIND. A wake that cannot say what arrived makes "+
				"every screen re-read the whole transcript to find out whether it was prose or a card", e.Message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event was delivered for an interaction record")
	}

	a.mu.Lock()
	need := a.needs["m1/s1"]
	entries := len(a.journal)
	a.mu.Unlock()
	if need != "" {
		t.Errorf("Session.Need became %q off an interaction record. Need is the verbatim record type "+
			"that last touched the session and the triage row renders it; IS-SS-1 keeps the roster on "+
			"group_transition and the transcript on session_status", need)
	}
	if entries != 0 {
		t.Errorf("the journal PAGE grew %d entry(ies) from an interaction record. The page is the "+
			"activity log of roster events; an item belongs to the transcript, which is durable and "+
			"folded, and putting it in both makes one of the two wrong", entries)
	}
}
