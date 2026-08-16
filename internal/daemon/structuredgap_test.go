package daemon

// Tests for ADR-017 T2 rule 2 / playbook 6.1: the daemon-authored structured_gap
// capability-degrade event. An unrecoverable shim/daemon spool or cursor gap "emits an
// exact structured_gap boundary, disables structured_chat for that session instance,
// and forbids a fabricated completion" (playbook 6.1).
//
// CARRIER: StructuredGapEvent rides the EXISTING journal.Record family under a new
// journal.TypeStructuredGap discriminator, exactly mirroring how InteractionItem rides
// journal.TypeInteraction (internal/daemon/interaction.go) -- no new mailbox frame kind
// (IS-LAYER-1). Session id and cursor are deliberately absent from the event body for
// the same reason InteractionItem omits them: the enclosing journal.Record already
// carries both.
//
// R6 (bd agents-tracker-hggx.7) landed real emission (r6_structuredgap_test.go);
// the stub-emission pin this file used to carry (TestEmitStructuredGap_
// IsAStubThatAppendsNothing) is retired along with the stub itself.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
)

// TestStructuredGapEvent_WireShape pins the event body's shape: no session_id, no
// cursor (InteractionItem's own convention -- the enclosing journal.Record carries
// both already).
func TestStructuredGapEvent_WireShape(t *testing.T) {
	ev := StructuredGapEvent{TS: time.UnixMilli(1700000000000).UTC(), Reason: "spool cursor gap"}
	got, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal StructuredGapEvent: %v", err)
	}
	const want = `{"ts":"2023-11-14T22:13:20Z","reason":"spool cursor gap"}`
	if string(got) != want {
		t.Fatalf("StructuredGapEvent wire shape =\n  %s\nwant\n  %s", got, want)
	}
}

// TestJournalTypeStructuredGap_IsADistinctWireDiscriminator pins the RecordType string
// itself, mirroring journal.go's other closed-vocabulary Type constants.
func TestJournalTypeStructuredGap_IsADistinctWireDiscriminator(t *testing.T) {
	if journal.TypeStructuredGap != "structured_gap" {
		t.Fatalf("journal.TypeStructuredGap = %q; want %q", journal.TypeStructuredGap, "structured_gap")
	}
}
