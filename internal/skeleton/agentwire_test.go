package skeleton

// toWireJournalRecord is the ONE place a session's agent identity can cross from the daemon
// to the phone (ADR-007 B139). Everything downstream -- schema.JournalRecord,
// phonecore.CachedSession, swarmmobile.Session -- has an Agent field that this conversion is
// the sole producer of, so a dropped copy here empties the whole chain while every type-level
// guard in it stays green. That is the defect class android/gate/boundverbledger_test.go
// catalogues: a symbol that exists and is traced, with no path that gives it a value.
//
// This file started as a deliberate standing red, when journal.Record had no Agent and the two
// daemon constructors could not populate one. Both now do (internal/daemon/agentrecord_test.go
// pins the roster snapshot and all four journalworthy branches); what remains worth checking
// is the hop itself.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/status"
)

func TestWireJournalRecordCarriesAgent(t *testing.T) {
	// A group_transition, not a roster record: the roster is the other constructor and it
	// leaves Cursor deliberately unset, so a roster fixture carrying one would describe a
	// record production never emits (s10_rosterfixture_test.go fences exactly that).
	got := toWireJournalRecord(journal.Record{
		Cursor:    7,
		SessionID: "m/s1",
		Type:      journal.TypeGroupTransition,
		Group:     status.Group("working"),
		Agent:     "claude",
	})

	if got.Agent != "claude" {
		t.Errorf("Agent = %q; want claude carried verbatim -- the phone has no other source for it", got.Agent)
	}
	// Non-vacuity: the hops that already worked must still work, so a conversion gutted down
	// to the agent alone cannot pass this.
	if got.Cursor != 7 || got.SessionID != "m/s1" || got.Type != string(journal.TypeGroupTransition) || got.Group != status.Group("working") {
		t.Errorf("conversion = %+v; want cursor 7, session m/s1, type group_transition, group working", got)
	}
}

// TestWireJournalRecordInventsNoAgent is the seam guardrail: a daemon record with no agent
// converts to a wire record with no agent. Never default one at a boundary.
func TestWireJournalRecordInventsNoAgent(t *testing.T) {
	got := toWireJournalRecord(journal.Record{Cursor: 8, SessionID: "m/s2", Type: journal.TypeExited})
	if got.Agent != "" {
		t.Errorf("Agent = %q for a record carrying none; want the empty string", got.Agent)
	}
}
