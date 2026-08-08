package skeleton

// toWireJournalRecord is the ONE place a session's user-given NAME can cross from the daemon to
// the phone, exactly as agentwire_test.go says of the agent identity. Everything downstream --
// schema.JournalRecord, phonecore.CachedSession, swarmmobile.Session.Title -- is fed by this
// conversion alone, so a dropped copy here empties the whole chain while every type-level guard
// in it stays green.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/status"
)

func TestWireJournalRecordCarriesName(t *testing.T) {
	// A group_transition, not a roster record, for agentwire_test.go's reason: the roster
	// leaves Cursor deliberately unset, so a roster fixture carrying one would describe a
	// record production never emits.
	got := toWireJournalRecord(journal.Record{
		Cursor:    7,
		SessionID: "m/s1",
		Type:      journal.TypeGroupTransition,
		Group:     status.Group("working"),
		Agent:     "claude",
		Name:      "api refactor",
	})

	if got.Name != "api refactor" {
		t.Errorf("Name = %q; want \"api refactor\" carried verbatim -- the phone has no other source for it", got.Name)
	}
	// Non-vacuity: the hops that already worked must still work, so a conversion gutted down
	// to the name alone cannot pass this.
	if got.Cursor != 7 || got.SessionID != "m/s1" || got.Type != string(journal.TypeGroupTransition) || got.Group != status.Group("working") || got.Agent != "claude" {
		t.Errorf("conversion = %+v; want cursor 7, session m/s1, type group_transition, group working, agent claude", got)
	}
}

// TestWireJournalRecordInventsNoName is the seam guardrail: a daemon record with no name
// converts to a wire record with no name. Never default one at a boundary.
func TestWireJournalRecordInventsNoName(t *testing.T) {
	got := toWireJournalRecord(journal.Record{Cursor: 8, SessionID: "m/s2", Type: journal.TypeExited})
	if got.Name != "" {
		t.Errorf("Name = %q for a record carrying none; want the empty string", got.Name)
	}
}
