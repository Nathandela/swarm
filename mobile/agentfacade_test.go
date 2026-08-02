package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for the FACADE half of the agent seam. agentseam_test.go
// pins that the upstream field is missing and stops there, deliberately, because it is a
// reflection guard. This file pins the one line the facade gains once the seam exists:
// session() must carry the cached agent across VERBATIM, the way it already carries Group,
// and must not derive one when the cache has none.
//
// Title is the counter-example that makes the point worth a test: it is DERIVED here (the
// session id's local part). Agent must not be. Nothing on the phone knows which agent a
// session runs -- only the wire does -- so anything the facade computed would be a guess
// wearing a fact's clothes.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/status"
)

func TestSessionCarriesAgentVerbatim(t *testing.T) {
	var a App
	s := a.session(phonecore.CachedSession{
		SessionID: "machine/api-refactor",
		Group:     status.Group("working"),
		Agent:     "claude",
		Present:   true,
	})
	if s.Agent != "claude" {
		t.Errorf("Session.Agent = %q; want claude carried verbatim from phonecore.CachedSession, "+
			"the way Group is", s.Agent)
	}
	if s.Group != "working" {
		t.Errorf("Session.Group = %q; want working (the existing verbatim hop must be unchanged)", s.Group)
	}
}

func TestSessionDerivesNoAgent(t *testing.T) {
	var a App
	s := a.session(phonecore.CachedSession{SessionID: "machine/api-refactor", Present: true})
	if s.Agent != "" {
		t.Errorf("Session.Agent = %q for a cached session that carries none; want the empty string. "+
			"Title is derived from the id and Agent must not be -- the phone has no way to know the "+
			"agent except from the wire", s.Agent)
	}
}
