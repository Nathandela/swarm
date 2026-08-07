package journal

// FAILING-FIRST (TDD RED, GG-5) for the `interaction` record type
// (docs/specifications/interaction-schema.md IS-LAYER-1/-2/-3, ADR-009, ADR-010).
//
// An interaction item travels as a journal.Record whose type is `interaction` and whose
// payload IS the item object, sealed as a BARE journal record -- no mailbox kind, no new
// demux branch. Record already carries an opaque Payload, so the whole of this package's
// share of the carriage is the type constant plus two properties worth fencing rather
// than asserting by confidence:
//
//  1. the item's `kind` discriminator stays INSIDE the payload and never surfaces as a
//     top-level record key (IS-LAYER-2), and the payload survives the on-disk round trip
//     verbatim -- a journal line is what a daemon restart re-reads;
//  2. adding the type did NOT bump SchemaVersion (IS-COMPAT-3), for exactly the reason
//     agentfield_test.go records for Record.Agent: DecodeRecord rejects a future version
//     outright, so a bump costs every older daemon the whole journal to gain nothing.
//
// RED is undefined-only: TypeInteraction does not exist yet.

import (
	"encoding/json"
	"testing"
)

// preInteractionSchemaVersion is the constant shipped by the last build with no
// `interaction` record type. Hardcoded because it is history, not configuration.
const preInteractionSchemaVersion = 1

// interactionItemLine is one item object as the daemon produces it: the §2 envelope with
// the §3 per-kind fields flat beside it. It is a literal so this file never depends on the
// producer that will mint it.
const interactionItemLine = `{"v":1,"item_id":"01JBQ4Z0X9M6T7NPKV2RQF8SJD","ts":"2026-08-07T10:00:00Z","kind":"agent_message","status":"in_progress","text":"hello"}`

func TestInteractionRecordCarriesTheItemInItsPayload(t *testing.T) {
	dir := t.TempDir()
	j, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := j.Append(Record{SessionID: "m/s1", Type: TypeInteraction, Payload: json.RawMessage(interactionItemLine)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen: the assertion must be about what a restarted daemon reads off disk, not
	// about the in-memory mirror it was just handed.
	j2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = j2.Close() }()
	res, err := j2.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("read %d events off disk; want the 1 interaction record appended", len(res.Events))
	}
	got := res.Events[0]
	if got.Type != TypeInteraction {
		t.Errorf("type = %q; want %q (IS-LAYER-1)", got.Type, TypeInteraction)
	}
	if got.SessionID != "m/s1" {
		t.Errorf("session_id = %q; want m/s1", got.SessionID)
	}
	if got.Group != "" {
		t.Errorf("group = %q; an item is a transcript record, not a roster transition (IS-SS-1)", got.Group)
	}

	var item map[string]any
	if err := json.Unmarshal(got.Payload, &item); err != nil {
		t.Fatalf("payload is not a JSON object after the round trip: %v (%s)", err, got.Payload)
	}
	if item["kind"] != "agent_message" || item["item_id"] != "01JBQ4Z0X9M6T7NPKV2RQF8SJD" || item["text"] != "hello" {
		t.Errorf("payload lost item fields across the round trip: %s", got.Payload)
	}

	// IS-LAYER-2: the kind lives ONLY inside the payload. Nothing this spec defines may
	// become a key of the record itself -- the record's own key space is the side of the
	// wall PB-SYNC-1 draws.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(EncodeRecord(got), &top); err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	for _, leaked := range []string{"kind", "v", "item_id"} {
		if _, ok := top[leaked]; ok {
			t.Errorf("record carries a top-level %q key; IS-LAYER-2 keeps every item field inside payload", leaked)
		}
	}
}

// TestInteractionTypeDidNotBumpTheSchema is IS-COMPAT-3 stated as the property: a new
// record TYPE is a new value of an existing string field, so the on-disk field set is
// unchanged and a build predating the type still parses the line (it will not understand
// the type, which is the consumer-side skip of IS-COMPAT-1, not a decode failure).
func TestInteractionTypeDidNotBumpTheSchema(t *testing.T) {
	if SchemaVersion != preInteractionSchemaVersion {
		t.Fatalf("SchemaVersion is %d, was %d before the interaction type. A bump makes every older "+
			"daemon reject every record this build writes; IS-COMPAT-3 forbids it for a kind or an "+
			"optional field, and anything else needs its own ADR entry", SchemaVersion, preInteractionSchemaVersion)
	}
	line := EncodeRecord(Record{
		SchemaVersion: SchemaVersion,
		Cursor:        51,
		SessionID:     "m/s1",
		Type:          TypeInteraction,
		Payload:       json.RawMessage(interactionItemLine),
	})
	var old struct {
		SchemaVersion int             `json:"schema_version"`
		Cursor        uint64          `json:"cursor"`
		Type          string          `json:"type"`
		Payload       json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &old); err != nil {
		t.Fatalf("a build predating the interaction type could not parse a record carrying one: %v", err)
	}
	if old.SchemaVersion > preInteractionSchemaVersion {
		t.Fatalf("the record declares schema_version %d, which a build supporting %d REJECTS outright, "+
			"costing it the whole journal", old.SchemaVersion, preInteractionSchemaVersion)
	}
	if old.Type != "interaction" || old.Cursor != 51 || len(old.Payload) == 0 {
		t.Errorf("the older shape decoded to %+v; want type interaction, cursor 51, payload intact", old)
	}
}
