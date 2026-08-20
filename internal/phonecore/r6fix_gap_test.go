package phonecore

// FAILING-FIRST (TDD RED, GG-5) for Wave R6 review finding B4(b) -- ADR-017's structured_gap,
// silently bridged on the phone's LIVE transcript path.
//
// THE PROBE. ItemStore.applyLocked opened with `if rec.Type != RecordTypeInteraction ... return
// false`, so every daemon-authored structured_gap record was DROPPED. A probe folded item A at
// cursor 1, a structured_gap at cursor 2 and item B at cursor 3, then read the session back:
// A and B were ADJACENT, with nothing between them. The phone drew a continuous conversation
// across a boundary the daemon had PROVED was discontinuous -- and ADR-017's whole T2 rule 2
// is that a proven gap renders honestly and is NEVER silently bridged.
//
// The daemon half of the same finding (interactionHistory dropping the same records out of
// every history page) is fenced in internal/skeleton/r6fix_chat_test.go. Both had to move:
// closing one leaves the tear invisible on the other path.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// gapRecord builds one wire structured_gap record the way skeleton's toWireJournalRecord
// now emits it: the daemon's StructuredGapEvent payload carried as Item.
func gapRecord(session string, cursor uint64, ts time.Time, reason string) schema.JournalRecord {
	payload, err := json.Marshal(struct {
		TS     time.Time `json:"ts"`
		Reason string    `json:"reason"`
	}{TS: ts, Reason: reason})
	if err != nil {
		panic(err)
	}
	return schema.JournalRecord{
		SessionID: session, Cursor: cursor, Type: RecordTypeStructuredGap, Item: payload,
	}
}

// gapItemRecord builds one ordinary interaction record.
func gapItemRecord(session string, cursor uint64, itemID, kind, text string) schema.JournalRecord {
	payload, err := json.Marshal(map[string]any{
		"v": 1, "item_id": itemID, "kind": kind, "text": text, "status": StatusCompleted,
	})
	if err != nil {
		panic(err)
	}
	return schema.JournalRecord{
		SessionID: session, Cursor: cursor, Type: RecordTypeInteraction, Item: payload,
	}
}

// TestR6Fix_AStructuredGapIsAFirstClassTranscriptElement is the probe, frozen.
func TestR6Fix_AStructuredGapIsAFirstClassTranscriptElement(t *testing.T) {
	s := NewItemStore()
	const sess = "m1/s1"
	ts := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	if !s.Apply(gapItemRecord(sess, 1, "01JA", KindUserMessage, "first")) {
		t.Fatal("item A did not land")
	}
	if !s.Apply(gapRecord(sess, 2, ts, "shim spool gap")) {
		t.Fatal("the structured_gap did not land: the tear the daemon PROVED is invisible to the " +
			"phone, so the transcript draws the items either side of it as one continuous " +
			"conversation -- ADR-017 T2 rule 2's silently-bridged gap, exactly")
	}
	if !s.Apply(gapItemRecord(sess, 3, "01JB", KindAgentMessage, "second")) {
		t.Fatal("item B did not land")
	}

	got := s.Session(sess)
	if len(got) != 3 {
		t.Fatalf("transcript has %d elements (%+v), want 3: A, the gap, B", len(got), got)
	}
	if got[0].ItemID != "01JA" || got[2].ItemID != "01JB" {
		t.Fatalf("transcript order = %q, %q, %q; want A, gap, B in cursor order",
			got[0].ItemID, got[1].ItemID, got[2].ItemID)
	}
	mid := got[1]
	if mid.Kind != KindStructuredGap {
		t.Errorf("the element between A and B has kind %q, want %q: a client picks its rendering "+
			"off this field, so a gap that does not say it is a gap cannot be drawn as one",
			mid.Kind, KindStructuredGap)
	}
	if mid.Text != "shim spool gap" {
		t.Errorf("gap reason = %q, want the daemon's own words: the reason is what turns "+
			"'something is missing here' into a sentence a user can act on", mid.Text)
	}
	if mid.TSUnixMs != ts.UnixMilli() {
		t.Errorf("gap ts = %d, want %d (the daemon's instant, never the phone's arrival time)",
			mid.TSUnixMs, ts.UnixMilli())
	}
	if !terminal(mid.Status) {
		t.Errorf("gap status = %q, want a terminal one: a proven boundary is a fact, not a "+
			"process, and nothing may re-open it", mid.Status)
	}
}

// TestR6Fix_ARedeliveredGapFoldsToOneElement pins the identity rule. Identity is the emission
// instant carried IN the payload, not the cursor: ADR-014 §2 says a reconciliation
// legitimately re-delivers records at NEW cursors, so a cursor-keyed gap would grow a second
// row every time the transcript was repaired -- turning an honest boundary into visible noise
// the user learns to ignore.
func TestR6Fix_ARedeliveredGapFoldsToOneElement(t *testing.T) {
	s := NewItemStore()
	const sess = "m1/s1"
	ts := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	s.Apply(gapRecord(sess, 2, ts, "shim spool gap"))
	if s.Apply(gapRecord(sess, 40, ts, "shim spool gap")) {
		t.Error("a re-delivery of the SAME proven boundary at a new cursor folded a SECOND gap " +
			"element; one boundary is one row")
	}
	if n := len(s.Session(sess)); n != 1 {
		t.Fatalf("transcript has %d elements, want exactly 1", n)
	}
}

// TestR6Fix_AGapWithNoDecodablePayloadStillRendersTheTear: the FACT of the tear is the
// load-bearing half. A gap dropped for want of words would put the fold back where finding B4
// found it, for the one case where the daemon's words did not survive.
func TestR6Fix_AGapWithNoDecodablePayloadStillRendersTheTear(t *testing.T) {
	s := NewItemStore()
	const sess = "m1/s1"
	rec := schema.JournalRecord{SessionID: sess, Cursor: 7, Type: RecordTypeStructuredGap}
	if !s.Apply(rec) {
		t.Fatal("a structured_gap with no payload was dropped; the tear is real whether or not " +
			"its reason survived")
	}
	got := s.Session(sess)
	if len(got) != 1 || got[0].Kind != KindStructuredGap {
		t.Fatalf("transcript = %+v, want one structured_gap element", got)
	}
	if got[0].Text != "" {
		t.Errorf("gap text = %q, want empty: absent words stay absent rather than being invented",
			got[0].Text)
	}
}

// TestR6Fix_ASessionNeutralGapNamesNoTranscript: a gap record with no session names no
// transcript to tear, and folding it into every session would be worse than dropping it.
func TestR6Fix_ASessionNeutralGapNamesNoTranscript(t *testing.T) {
	s := NewItemStore()
	if s.Apply(schema.JournalRecord{Cursor: 1, Type: RecordTypeStructuredGap}) {
		t.Error("a session-neutral structured_gap was folded into the transcript")
	}
	if s.Len() != 0 {
		t.Errorf("store holds %d elements, want 0", s.Len())
	}
}
