package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5). The Substrate session row has an agent slot
// (.prow .ag: mono, 10sp, --p-ink3, "claude" / "codex"), and swarmmobile.Session has
// nowhere to put it: it carries ID/Title/Group/Need/Present (mobile/types.go) and Title
// is derived from the id's local part, not the agent. This test traces WHY, the way
// state_seam_test.go traces PB-STATE-1's three missing coordinates: by reflecting on the
// upstream types the facade would have to read from, and failing loudly while they lack
// the field, instead of quietly wiring in a guess.
//
// The trace, wire-in to wire-out:
//
//  1. internal/journal.Record (the daemon's OWN durable journal, journal.go) is
//     SchemaVersion/Cursor/TS/SessionID/Type/Group/Payload. No Agent.
//  2. internal/daemon/journal.go builds every Record the phone can ever see:
//     journalRecordFor (launch/exit/lost/group_transition) and rosterSnapshotLocked (the
//     roster snapshot) both construct journal.Record literals that name SessionID/Type/
//     Group only, even though the daemon already knows the agent right there --
//     persist.Meta carries AgentType (persist.go) -- it is simply never read into the
//     record.
//  3. internal/skeleton/api.go's toWireJournalRecord converts daemon Record to the wire
//     schema.JournalRecord and says so in its own doc comment: "only the fields the phone
//     needs" -- Cursor/SessionID/Type/Group. Agent is excluded by name, not by oversight.
//  4. internal/protocol/schema.JournalRecord (remote.go), the actual bytes the relay
//     carries to the phone, is therefore Cursor/SessionID/Type/Group too.
//  5. internal/phonecore.CachedSession (journal.go), the phone's merged session model
//     that mobile/app.go's session() reads, is SessionID/Group/Present -- folded straight
//     from step 4, so it inherits the gap.
//
// Every SessionID row the phone ever renders comes from this chain. schema.SessionView
// DOES carry Agent (schema.go:117), but that type backs the LOCAL `swarm status` general
// view (V-4) -- phonecore never imports it, and grep over internal/phonecore and mobile
// confirms zero references. The phone-reachable value is LaunchSpec.Agent
// (mobile/types.go:252), which is OUTBOUND (what to launch), not a running session's
// identity.
//
// Closing this is a PROTOCOL change: internal/journal.Record needs the field, both
// daemon-side constructors need to populate it from persist.Meta.AgentType, the wire
// schema needs it, and phonecore.CachedSession needs to carry it forward -- four
// packages under internal/, every one of them off limits to this facade slice, and the
// kind of wire-shape change that gets its own ADR rather than riding in under a mobile
// field addition. So this test does not add Session.Agent. It fails until the seam
// above exists, at which point mobile/app.go's session() gains one line (Agent:
// string(cs.Agent), matching how Group already crosses verbatim) and this test starts
// asserting the field it currently laments the absence of.

import (
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestSessionAgentHasNoWireSource(t *testing.T) {
	// Non-vacuity: the fields these two types DO carry must still be there, so a renamed
	// schema does not make this test silently pass by measuring nothing (state_seam_test.go
	// sets the same guard for phonecore.State).
	jr := reflect.TypeOf(schema.JournalRecord{})
	for _, present := range []string{"Cursor", "SessionID", "Type", "Group"} {
		if _, ok := jr.FieldByName(present); !ok {
			t.Fatalf("schema.JournalRecord lost field %q; this guard is measuring the wrong schema", present)
		}
	}
	cs := reflect.TypeOf(phonecore.CachedSession{})
	for _, present := range []string{"SessionID", "Group", "Present"} {
		if _, ok := cs.FieldByName(present); !ok {
			t.Fatalf("phonecore.CachedSession lost field %q; this guard is measuring the wrong schema", present)
		}
	}

	if _, ok := jr.FieldByName("Agent"); !ok {
		t.Errorf("schema.JournalRecord carries no Agent: the wire type the phone's journal stream " +
			"decodes (remote.go) has nowhere to put a session's agent identity. " +
			"internal/skeleton/api.go's toWireJournalRecord converts only Cursor/SessionID/Type/Group " +
			"-- by its own comment, 'only the fields the phone needs' -- so even though the daemon " +
			"knows the agent locally (persist.Meta.AgentType), nothing copies it onto the record " +
			"that reaches the relay")
	}
	if _, ok := cs.FieldByName("Agent"); !ok {
		t.Errorf("phonecore.CachedSession carries no Agent: the phone's merged session model " +
			"(journal.go) is folded from schema.JournalRecord and inherits its gap, so " +
			"mobile/app.go's session() has no verbatim-from-the-wire value to assign a " +
			"swarmmobile.Session.Agent field the way it already does for Group")
	}
}
