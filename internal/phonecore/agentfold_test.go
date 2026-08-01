package phonecore

// FAILING-FIRST (TDD RED, GG-5) for the FOLD half of the agent seam. mobile/agentseam_test.go
// pins that the FIELD is missing; this file pins how the field must BEHAVE once it exists,
// which a reflection guard cannot see.
//
// The rule is Group's rule, and it is deliberately not a new one: a non-empty value on a
// record updates the cache VERBATIM, and an EMPTY value updates nothing. Group's
// applyLocked guard (`if rec.Group != ""`) exists because most record types carry no group
// -- only roster snapshots and group_transition events do -- so folding an empty group from
// an `exited` event would blank a session's live group. Agent has exactly the same shape:
// the roster carries it, and a bare event may not, so an unguarded assignment would erase a
// known agent every time a session merely changed state.
//
// The second test is the seam guardrail from the other side: a record with no agent must
// leave the session with no agent. Never invent, never default -- an empty string that
// reads as a real agent is the defect class this project cares most about.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func TestSessionCacheFoldsAgentVerbatim(t *testing.T) {
	c := NewSessionCache()

	// A roster snapshot carries the agent; it lands verbatim. Cursor stays unset, as it is
	// on the real wire: a roster record is a set member keyed by SessionID, not a point in
	// the cursor-ordered stream (internal/daemon/journal.go), and a fixture that invents one
	// would test a record production never emits.
	c.Apply(schema.JournalRecord{SessionID: "m/s1", Type: "roster", Group: status.Group("working"), Agent: "claude"})
	c.Apply(schema.JournalRecord{SessionID: "m/s2", Type: "roster", Group: status.Group("idle"), Agent: "codex"})

	if cs, ok := c.Get("m/s1"); !ok || cs.Agent != "claude" {
		t.Fatalf("s1 = %+v ok=%v; want Agent claude verbatim from the roster", cs, ok)
	}
	if cs, ok := c.Get("m/s2"); !ok || cs.Agent != "codex" {
		t.Fatalf("s2 = %+v ok=%v; want Agent codex verbatim from the roster", cs, ok)
	}

	// A later event that carries NO agent must not blank the one already known, exactly as
	// an agentless event does not blank Group.
	c.Apply(schema.JournalRecord{Cursor: 12, SessionID: "m/s1", Type: "group_transition", Group: status.Group("needs_input")})
	cs, ok := c.Get("m/s1")
	if !ok {
		t.Fatalf("s1 vanished after a group_transition")
	}
	if cs.Agent != "claude" {
		t.Errorf("s1 Agent = %q after an agentless group_transition; want claude preserved -- an "+
			"empty agent on a record means the record does not carry one, not that the session lost its agent", cs.Agent)
	}
	if cs.Group != status.Group("needs_input") {
		t.Errorf("s1 Group = %q; want needs_input (the guard must not have broken Group's own fold)", cs.Group)
	}
}

func TestSessionCacheInventsNoAgent(t *testing.T) {
	c := NewSessionCache()
	c.Apply(schema.JournalRecord{SessionID: "m/s3", Type: "roster", Group: status.Group("working")})

	cs, ok := c.Get("m/s3")
	if !ok {
		t.Fatalf("s3 missing from the cache")
	}
	if cs.Agent != "" {
		t.Errorf("s3 Agent = %q from a record that carried none; want the empty string. A session "+
			"whose record has no agent HAS no agent, and a defaulted value would read as a real one", cs.Agent)
	}
}
