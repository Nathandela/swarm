package protocol

// FAILING-FIRST (TDD RED, GG-5) for the WIRE half of interaction carriage
// (interaction-schema.md §1's wire-carriage clause, IS-LAYER-1/-2).
//
// schema.JournalRecord today carries no payload at all -- by its own doc comment, "the
// daemon-internal payload is not carried on the wire" -- so an interaction record would
// reach the phone as a bare type string with the item stripped off. §1 books exactly one
// additive field to close that: `item`, populated ONLY when type is "interaction".
//
// RED is undefined-only: JournalRecord has no Item field.
//
// NOT fenced here, deliberately: GG-7's drift check (protocolmd_test.go) reflects
// Control/SessionView/LaunchReq/TerminalSnapshot only, and interaction-schema.md §1 states
// in as many words that "no build can fail on a missing `item` ... row" -- the protocol.md
// row is a PROCEDURAL obligation. A test that made it fire would contradict a normative
// sentence of the spec, so the doc row is written in the same change and the existing fence
// is run to prove it stayed green.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// wireItem is one item object as the daemon seals it: §2 envelope, §3 kind fields flat.
const wireItem = `{"v":1,"item_id":"01JBQ4Z0X9M6T7NPKV2RQF8SJD","ts":"2026-08-07T10:00:00Z","kind":"tool_run","tool":"Read"}`

func TestJournalRecordCarriesTheInteractionItem(t *testing.T) {
	b, err := json.Marshal(JournalRecord{Cursor: 9, SessionID: "m/s1", Type: "interaction", Item: json.RawMessage(wireItem)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back JournalRecord
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := compact(t, back.Item); got != compact(t, json.RawMessage(wireItem)) {
		t.Errorf("item = %s; want the item object carried verbatim", got)
	}
	if back.Cursor != 9 || back.SessionID != "m/s1" || back.Type != "interaction" {
		t.Errorf("record = %+v; want cursor 9, session m/s1, type interaction", back)
	}

	// IS-LAYER-2: the item's kind discriminator stays inside the item. It may not become a
	// field of the record, and nothing about it goes near a header the seq bucket keys on.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if _, ok := top["kind"]; ok {
		t.Errorf("wire record carries a top-level `kind`: %s", b)
	}
}

// TestJournalRecordWithoutAnItemEncodesUnchanged: `item` is additive and omitempty, so
// every record type that predates it serializes byte-identically -- the same guarantee
// journal.Record's Agent field was added under.
func TestJournalRecordWithoutAnItemEncodesUnchanged(t *testing.T) {
	b, err := json.Marshal(JournalRecord{Cursor: 4, SessionID: "m/s1", Type: "launched"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"cursor":4,"session_id":"m/s1","type":"launched"}`; string(b) != want {
		t.Errorf("record with no item encoded to %s; want %s unchanged", b, want)
	}
	if strings.Contains(string(b), "item") {
		t.Errorf("record with no item carries an item key: %s", b)
	}
}

// TestJournalEventControlCarriesTheItem: the item has to survive the envelope it actually
// travels in -- Control.journal on a journal_event -- not just a bare struct round trip.
func TestJournalEventControlCarriesTheItem(t *testing.T) {
	b, err := EncodeControl(Control{
		Op:         OpJournalEvent,
		EndpointID: "m",
		Journal:    []JournalRecord{{Cursor: 12, SessionID: "m/s1", Type: "interaction", Item: json.RawMessage(wireItem)}},
	})
	if err != nil {
		t.Fatalf("EncodeControl: %v", err)
	}
	got, err := DecodeControl(b)
	if err != nil {
		t.Fatalf("DecodeControl: %v", err)
	}
	if len(got.Journal) != 1 {
		t.Fatalf("decoded %d journal records; want 1", len(got.Journal))
	}
	if c := compact(t, got.Journal[0].Item); c != compact(t, json.RawMessage(wireItem)) {
		t.Errorf("item through a journal_event = %s; want it verbatim", c)
	}
}

func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return buf.String()
}
