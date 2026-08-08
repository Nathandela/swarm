package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for the FACADE half of the session-NAME seam
// (agents-tracker-ksvb.1): the one decision mobile/app.go's session() makes about what a person
// reads at the top of every row.
//
// TITLE IS THE FIELD THAT CHANGES, AND NO NEW ONE IS ADDED. Session.Title is already "the
// display name", already derived, and already what every render site spends; a second field
// beside it would make every one of those sites choose, and the site that forgot would render
// the id while the name sat one field away. So the DERIVATION gains a preference and the
// surface does not move.
//
// THE FALLBACK IS TODAY'S, EXACTLY. A record carrying no name is an OLD DAEMON or an unlabelled
// session, and both must render precisely what they render now -- the id's local part, or the
// whole id when it names no machine. ADR-007 B135: never fabricate. TestSessionTitleFallsBack
// below is that compatibility test, driven through the same seam production uses rather than
// through a mock of it.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func TestSessionTitlePrefersTheName(t *testing.T) {
	var a App
	s := a.session(phonecore.CachedSession{
		SessionID: "ep-1a2b3c4d/kx7q2m4v9p1s6t8w",
		Name:      "api refactor",
		Group:     status.Group("working"),
		Agent:     "claude",
		Present:   true,
	})
	if s.Title != "api refactor" {
		t.Errorf("Session.Title = %q; want \"api refactor\" -- the user's own label, which the "+
			"daemon holds and the wire now carries, beats the 16-char random local part of the id", s.Title)
	}
	// The id itself is untouched: it is the identity every verb signs over, and the title is
	// display only.
	if s.ID != "ep-1a2b3c4d/kx7q2m4v9p1s6t8w" {
		t.Errorf("Session.ID = %q; want the namespaced id verbatim -- the name is display, not identity", s.ID)
	}
	if s.Agent != "claude" {
		t.Errorf("Session.Agent = %q; want claude (the existing verbatim hop must be unchanged)", s.Agent)
	}
}

// TestSessionTitleFallsBack is the OLD-DAEMON COMPATIBILITY case: a daemon that predates the
// field sends records with no name at all, and every one of them must render exactly as it does
// today. Both of today's arms are covered -- an id that names a machine, and one that does not.
func TestSessionTitleFallsBack(t *testing.T) {
	var a App

	namespaced := a.session(phonecore.CachedSession{SessionID: "ep-1a2b3c4d/kx7q2m4v9p1s6t8w", Present: true})
	if namespaced.Title != "kx7q2m4v9p1s6t8w" {
		t.Errorf("Session.Title = %q for a nameless session; want the id's local part, which is "+
			"exactly today's behaviour. A daemon predating the field sends no name, and inventing "+
			"one for it is ADR-007 B135's defect", namespaced.Title)
	}

	bare := a.session(phonecore.CachedSession{SessionID: "kx7q2m4v9p1s6t8w", Present: true})
	if bare.Title != "kx7q2m4v9p1s6t8w" {
		t.Errorf("Session.Title = %q for a nameless id that names no machine; want the whole id, "+
			"which is today's other arm", bare.Title)
	}
}

// TestOldDaemonRecordRendersAsToday drives the WHOLE phone-side chain from a wire record an old
// daemon really would send -- one whose JSON has no `name` key at all -- so the compatibility
// claim is made about the decode, the fold and the derivation together rather than about a
// hand-built cache entry.
func TestOldDaemonRecordRendersAsToday(t *testing.T) {
	cache := phonecore.NewSessionCache()
	// Exactly the fields a pre-Name daemon puts on a roster record.
	cache.Apply(schema.JournalRecord{
		SessionID: "ep-1a2b3c4d/kx7q2m4v9p1s6t8w",
		Type:      "roster",
		Group:     status.Group("working"),
		Agent:     "claude",
	})
	cs, ok := cache.Get("ep-1a2b3c4d/kx7q2m4v9p1s6t8w")
	if !ok {
		t.Fatalf("the roster record did not reach the cache; the fixture is measuring nothing")
	}
	if cs.Name != "" {
		t.Fatalf("CachedSession.Name = %q from a record with no name; the fold invented one", cs.Name)
	}

	var a App
	if got := a.session(cs).Title; got != "kx7q2m4v9p1s6t8w" {
		t.Errorf("Title = %q for an old daemon's record; want the id's local part -- the rendering "+
			"an old daemon produces must be byte-identical to the one it produces today", got)
	}
}
