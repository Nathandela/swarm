package daemon

// FAILING-FIRST (TDD RED, GG-5) tests for ADR-017 T2 rule 2 / playbook 6.1: the
// daemon-authored structured_gap capability-degrade event. An unrecoverable shim/daemon
// spool or cursor gap "emits an exact structured_gap boundary, disables structured_chat
// for that session instance, and forbids a fabricated completion" (playbook 6.1). This
// slice defines the event SHAPE and the emission SEAM; the emission itself is a STUB
// that returns ErrStructuredGapUnimplemented until the spool-boundary detection
// (ADR-010's spool) lands -- a stub that appends nothing is the fabricated-completion
// rule applied to the seam itself: better silence than a false event.
//
// CARRIER: StructuredGapEvent rides the EXISTING journal.Record family under a new
// journal.TypeStructuredGap discriminator, exactly mirroring how InteractionItem rides
// journal.TypeInteraction (internal/daemon/interaction.go) -- no new mailbox frame kind
// (IS-LAYER-1). Session id and cursor are deliberately absent from the event body for
// the same reason InteractionItem omits them: the enclosing journal.Record already
// carries both.
//
// THE SEAMS these tests pin (undefined symbols -> compile-fail RED):
//
//	journal.TypeStructuredGap journal.RecordType = "structured_gap"
//	type StructuredGapEvent struct{ TS time.Time; Reason string }
//	var ErrStructuredGapUnimplemented error
//	func (*Daemon) EmitStructuredGap(sessionID, reason string) error

import (
	"encoding/json"
	"errors"
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

// TestEmitStructuredGap_IsAStubThatAppendsNothing: emission is unimplemented (the
// spool-boundary detection is a later slice's work), so the seam returns
// ErrStructuredGapUnimplemented and -- because a fabricated completion is forbidden --
// the journal is untouched rather than gaining a half-true event.
func TestEmitStructuredGap_IsAStubThatAppendsNothing(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	before, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom before: %v", err)
	}

	err = d.EmitStructuredGap("s1", "spool cursor gap")
	if !errors.Is(err, ErrStructuredGapUnimplemented) {
		t.Fatalf("EmitStructuredGap error = %v; want ErrStructuredGapUnimplemented", err)
	}

	after, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom after: %v", err)
	}
	if len(after.Events) != len(before.Events) {
		t.Fatalf("journal grew by %d records on an unimplemented emission; want 0 (a stub must never fabricate a completion)", len(after.Events)-len(before.Events))
	}
}
