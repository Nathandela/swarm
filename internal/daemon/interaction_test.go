package daemon

// FAILING-FIRST (TDD RED, GG-5) for the daemon-side APPEND ENTRY of an interaction item
// (interaction-schema.md §2 envelope, §5 caps, IS-LAYER-1/-3, IS-ENV-3; ADR-010 §3, which
// makes the daemon the sole producer of what goes on the wire).
//
// RED is undefined-only: these production seams do not exist yet.
//
//	const InteractionSchemaVersion = 1                                    // §2 `v`
//	const MaxItemBytes             = 8 << 10                              // §5
//	type  InteractionItem struct{ V; ItemID; TS; TurnID; Kind; Status; Truncated; FullBytes; Detail; Fields }
//	func  (d *Daemon) RecordInteraction(sessionID string, it InteractionItem) error
//
// The four properties fenced here are the ones a carriage seam can get wrong silently:
// the record is BARE and roster-neutral, `ts` is stamped and is a machine instant, an
// incomplete envelope emits NOTHING, and an item over the byte cap is refused rather than
// clipped mid-structure.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
)

const testItemID = "01JBQ4Z0X9M6T7NPKV2RQF8SJD"

// interactionRecords returns every `interaction` record in the daemon-wide journal.
func interactionRecords(t *testing.T, d *Daemon) []journal.Record {
	t.Helper()
	res, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom(0): %v", err)
	}
	var out []journal.Record
	for _, ev := range res.Events {
		if ev.Type == journal.TypeInteraction {
			out = append(out, ev)
		}
	}
	return out
}

// TestDaemon_RecordInteractionAppendsBareInteractionRecord: the item object lands in the
// payload of a bare `interaction` record carrying the session id and nothing roster-shaping,
// with the §3 per-kind fields FLAT beside the §2 envelope ("fields below are additional to
// the envelope"), and a daemon-stamped `ts`.
func TestDaemon_RecordInteractionAppendsBareInteractionRecord(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	before := time.Now().Add(-time.Second)

	err := d.RecordInteraction("m/s1", InteractionItem{
		V:      InteractionSchemaVersion,
		ItemID: testItemID,
		TurnID: "turn-1",
		Kind:   "agent_message",
		Status: "in_progress",
		Fields: json.RawMessage(`{"text":"hello"}`),
	})
	if err != nil {
		t.Fatalf("RecordInteraction: %v", err)
	}

	recs := interactionRecords(t, d)
	if len(recs) != 1 {
		t.Fatalf("journal holds %d interaction records; want exactly 1", len(recs))
	}
	rec := recs[0]
	if rec.SessionID != "m/s1" {
		t.Errorf("session_id = %q; want m/s1", rec.SessionID)
	}
	if rec.Group != "" || rec.Agent != "" {
		t.Errorf("record carries group=%q agent=%q; an item is the transcript's record and shapes no "+
			"roster row (IS-SS-1), so both stay unset", rec.Group, rec.Agent)
	}
	if rec.Cursor == 0 {
		t.Errorf("cursor = 0; ordering IS the journal cursor (IS-LAYER-3)")
	}

	var item map[string]any
	if err := json.Unmarshal(rec.Payload, &item); err != nil {
		t.Fatalf("payload is not a JSON object: %v (%s)", err, rec.Payload)
	}
	if item["v"] != float64(InteractionSchemaVersion) || item["item_id"] != testItemID ||
		item["kind"] != "agent_message" || item["status"] != "in_progress" || item["turn_id"] != "turn-1" {
		t.Errorf("envelope did not survive: %s", rec.Payload)
	}
	if item["text"] != "hello" {
		t.Errorf("kind field `text` is not flat beside the envelope: %s -- §3's fields are "+
			"additional to the envelope, one object, not a nested body", rec.Payload)
	}

	ts, ok := item["ts"].(string)
	if !ok {
		t.Fatalf("item carries no `ts`; §2 makes it required and the wire journal record has none to substitute")
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("ts %q is not an RFC3339 instant: %v", ts, err)
	}
	if !parsed.After(before) {
		t.Errorf("ts = %s, not after %s; it must be the MACHINE instant for this record (PB-APP-11), "+
			"never a zero value and never an arrival time", parsed, before)
	}
}

// TestDaemon_RecordInteractionKeepsAProducerSuppliedTS: a producer that captured the event
// earlier owns the instant; the daemon stamps only when it was left unset. Substituting the
// append time for a known capture time is the PB-APP-11 mistake in the other direction.
func TestDaemon_RecordInteractionKeepsAProducerSuppliedTS(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	want := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)

	if err := d.RecordInteraction("m/s1", InteractionItem{
		V: InteractionSchemaVersion, ItemID: testItemID, TS: want, Kind: "session_status",
	}); err != nil {
		t.Fatalf("RecordInteraction: %v", err)
	}
	recs := interactionRecords(t, d)
	if len(recs) != 1 {
		t.Fatalf("journal holds %d interaction records; want 1", len(recs))
	}
	var item struct {
		TS time.Time `json:"ts"`
	}
	if err := json.Unmarshal(recs[0].Payload, &item); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !item.TS.Equal(want) {
		t.Errorf("ts = %s; want the producer's %s carried verbatim", item.TS, want)
	}
}

// TestDaemon_RecordInteractionEmitsNothingForAnIncompleteEnvelope is IS-ENV-3: a producer
// that would emit an item lacking v, item_id or kind emits NOTHING -- not a partial item a
// consumer would have to skip, and not a cursor burned on an unreadable record.
func TestDaemon_RecordInteractionEmitsNothingForAnIncompleteEnvelope(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	for _, tc := range []struct {
		name string
		item InteractionItem
	}{
		{"no v", InteractionItem{ItemID: testItemID, Kind: "agent_message"}},
		{"no item_id", InteractionItem{V: InteractionSchemaVersion, Kind: "agent_message"}},
		{"no kind", InteractionItem{V: InteractionSchemaVersion, ItemID: testItemID}},
	} {
		if err := d.RecordInteraction("m/s1", tc.item); err == nil {
			t.Errorf("%s: RecordInteraction accepted an incomplete envelope; IS-ENV-3 requires emitting nothing", tc.name)
		}
	}
	if n := len(interactionRecords(t, d)); n != 0 {
		t.Fatalf("%d interaction records appended for incomplete envelopes; want none", n)
	}
}

// TestDaemon_RecordInteractionRefusesAnItemOverTheByteCap is §5's MaxItemBytes at the one
// boundary that can see the serialized item. Refusing is IS-ENV-3's answer: an item over the
// cap had its per-field truncation skipped upstream, and clipping a JSON object here would
// produce the partial item that rule forbids.
func TestDaemon_RecordInteractionRefusesAnItemOverTheByteCap(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	fields, err := json.Marshal(map[string]string{"text": strings.Repeat("x", MaxItemBytes)})
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	if err := d.RecordInteraction("m/s1", InteractionItem{
		V: InteractionSchemaVersion, ItemID: testItemID, Kind: "agent_message", Fields: fields,
	}); err == nil {
		t.Errorf("RecordInteraction accepted an item over the %d-byte cap (§5)", MaxItemBytes)
	}
	if n := len(interactionRecords(t, d)); n != 0 {
		t.Fatalf("%d interaction records appended past the cap; want none", n)
	}
}

// TestInteractionItem_KindFieldsMayNotCollideWithTheEnvelope: the flat merge is only safe
// while the two key spaces are disjoint, so a kind field named like an envelope field is a
// hard error rather than a silent overwrite of `kind` or `item_id`.
func TestInteractionItem_KindFieldsMayNotCollideWithTheEnvelope(t *testing.T) {
	_, err := json.Marshal(InteractionItem{
		V: InteractionSchemaVersion, ItemID: testItemID, Kind: "tool_run",
		Fields: json.RawMessage(`{"kind":"something_else"}`),
	})
	if err == nil {
		t.Fatal("marshalling an item whose kind fields collide with the envelope succeeded; a silent " +
			"overwrite of `kind` would re-label the item")
	}
}
