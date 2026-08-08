package phonecore

// FAILING-FIRST (TDD RED, GG-5) for the FOLD half of the session-NAME seam
// (agents-tracker-ksvb.1).
//
// The rule is Group's and Agent's rule, deliberately not a new one: a non-empty value on a
// record updates the cache VERBATIM, and an EMPTY value updates nothing. The roster carries the
// name and a bare event may not, so an unguarded assignment would erase a known name every time
// a session merely changed state -- and the phone would fall back to the raw id for a session
// the user HAS labelled, which is the exact complaint this bead exists for.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func TestSessionCacheFoldsNameVerbatim(t *testing.T) {
	c := NewSessionCache()

	// Cursor stays unset, as it is on the real wire for a roster record.
	c.Apply(schema.JournalRecord{SessionID: "m/s1", Type: "roster", Group: status.Group("working"), Agent: "claude", Name: "api refactor"})
	c.Apply(schema.JournalRecord{SessionID: "m/s2", Type: "roster", Group: status.Group("idle"), Agent: "codex", Name: "docs pass"})

	if cs, ok := c.Get("m/s1"); !ok || cs.Name != "api refactor" {
		t.Fatalf("s1 = %+v ok=%v; want Name \"api refactor\" verbatim from the roster", cs, ok)
	}
	if cs, ok := c.Get("m/s2"); !ok || cs.Name != "docs pass" {
		t.Fatalf("s2 = %+v ok=%v; want Name \"docs pass\" verbatim from the roster", cs, ok)
	}

	// A later event that carries NO name must not blank the one already known, exactly as a
	// nameless event does not blank Group or Agent.
	c.Apply(schema.JournalRecord{Cursor: 12, SessionID: "m/s1", Type: "group_transition", Group: status.Group("needs_input")})
	cs, ok := c.Get("m/s1")
	if !ok {
		t.Fatalf("s1 vanished after a group_transition")
	}
	if cs.Name != "api refactor" {
		t.Errorf("s1 Name = %q after a nameless group_transition; want \"api refactor\" preserved -- an "+
			"empty name on a record means the record does not carry one, not that the session lost its name", cs.Name)
	}
	if cs.Group != status.Group("needs_input") {
		t.Errorf("s1 Group = %q; want needs_input (the guard must not have broken Group's own fold)", cs.Group)
	}
	if cs.Agent != "claude" {
		t.Errorf("s1 Agent = %q; want claude preserved (the guard must not have broken Agent's own fold)", cs.Agent)
	}
}

func TestSessionCacheInventsNoName(t *testing.T) {
	c := NewSessionCache()
	c.Apply(schema.JournalRecord{SessionID: "m/s3", Type: "roster", Group: status.Group("working"), Agent: "claude"})

	cs, ok := c.Get("m/s3")
	if !ok {
		t.Fatalf("s3 missing from the cache")
	}
	if cs.Name != "" {
		t.Errorf("s3 Name = %q from a record that carried none; want the empty string. A session "+
			"the user never labelled HAS no name, and a defaulted one would read as the user's own", cs.Name)
	}
}
