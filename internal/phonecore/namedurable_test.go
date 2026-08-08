package phonecore

// FAILING-FIRST (TDD RED, GG-5) for the DURABILITY of the two per-session fields the wire now
// carries -- CachedSession.Agent and CachedSession.Name (agents-tracker-ksvb.1).
//
// WHY THIS IS A TEST OF ITS OWN RATHER THAN A WIDER fullState(). `Sessions` is sealed inside
// content_kept, and the pinned per-version literals must go on restoring exactly the fullState()
// this build builds -- so putting a NEW sub-field into that fixture demands either splicing it
// into literals that never carried it, which falsifies the artifact that proves migration works,
// or weakening the guard. state_test.go's own paragraph records an implementer hitting that wall
// and correctly refusing both, and the idiom it prescribes is this file: pin the new sub-field's
// round trip separately. TestStateStore_PushPreferenceVersionSurvivesARestart is the precedent.
//
// AGENT IS ASSERTED BESIDE NAME EVEN THOUGH IT SHIPPED EARLIER, because it never got this test:
// it was added to CachedSession and left out of fullState() for the reason above, so nothing
// anywhere proved it survived a restart. A restore that dropped it would have been silent.

import (
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

func TestStateStore_SessionAgentAndNameSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	kek := &s14aSealer{kek: stateV4FixtureKEK}

	store, err := OpenStore(path, "m1", kek, kek)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	st := store.Load()
	st.Sessions = []CachedSession{
		{SessionID: "m1/s1", Group: status.Group("working"), Agent: "claude", Name: "api refactor", Present: true},
		// The UNLABELLED session, carried through the same write: its emptiness must survive
		// too, or a restore that defaulted a name would be invisible beside the one that works.
		{SessionID: "m1/s2", Group: status.Group("working"), Agent: "codex", Present: true},
	}
	if err := store.Save(st); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A REAL restart: a second store built from the file alone, so nothing in memory can supply
	// an answer.
	reopened, err := OpenStore(path, "m1", &s14aSealer{kek: stateV4FixtureKEK}, &s14aSealer{kek: stateV4FixtureKEK})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.Load().Sessions
	if len(got) != 2 {
		t.Fatalf("restored %d sessions; want 2 -- the fixture is not measuring what it thinks", len(got))
	}
	byID := map[string]CachedSession{}
	for _, cs := range got {
		byID[cs.SessionID] = cs
	}
	if cs := byID["m1/s1"]; cs.Agent != "claude" || cs.Name != "api refactor" {
		t.Errorf("m1/s1 restored as %+v; want Agent claude and Name \"api refactor\". A per-session "+
			"field the wire carries and the blob drops comes back as the raw id on the next Android "+
			"process death, which is routine", cs)
	}
	if cs := byID["m1/s2"]; cs.Agent != "codex" || cs.Name != "" {
		t.Errorf("m1/s2 restored as %+v; want Agent codex and an EMPTY Name -- a session the user "+
			"never labelled must come back unlabelled", cs)
	}
}
