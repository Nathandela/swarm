package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the HOP: toWireJournalRecord is the one place a
// daemon journal record becomes the wire record the relay carries, so it is the one place
// an interaction item can cross (the agentwire_test.go argument, applied to the payload).
// A schema.JournalRecord.Item field that nothing populates is the defect class that file
// names: a symbol that exists and compiles with no path that gives it a value.
//
// The conversion's own doc comment currently excludes the payload BY NAME ("the opaque
// payload and schema/ts are not carried on the wire"). interaction-schema.md §1 carves out
// exactly one exception and no more: `item`, populated only when type is "interaction".

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
)

const wireItem = `{"v":1,"item_id":"01JBQ4Z0X9M6T7NPKV2RQF8SJD","ts":"2026-08-07T10:00:00Z","kind":"tool_run","tool":"Read"}`

func TestWireJournalRecordCarriesTheInteractionItem(t *testing.T) {
	got := toWireJournalRecord(journal.Record{
		Cursor:    11,
		SessionID: "m/s1",
		Type:      journal.TypeInteraction,
		Payload:   json.RawMessage(wireItem),
	})

	if string(got.Item) != wireItem {
		t.Errorf("Item = %s; want the item object verbatim -- the phone has no other source for it", got.Item)
	}
	// Non-vacuity: the hops that already worked must still work.
	if got.Cursor != 11 || got.SessionID != "m/s1" || got.Type != string(journal.TypeInteraction) {
		t.Errorf("conversion = %+v; want cursor 11, session m/s1, type interaction", got)
	}
}

// TestWireJournalRecordCarriesNoOtherPayload is the seam guardrail. `presence` already
// carries a payload (the online flag) and it is daemon-internal: a conversion that simply
// copied Payload across for every type would leak it and would still pass the test above.
func TestWireJournalRecordCarriesNoOtherPayload(t *testing.T) {
	got := toWireJournalRecord(journal.Record{
		Cursor:  12,
		Type:    journal.TypePresence,
		Payload: json.RawMessage(`{"online":true}`),
	})
	if got.Item != nil {
		t.Errorf("Item = %s on a %s record; §1 populates it ONLY when type is interaction", got.Item, journal.TypePresence)
	}
}
