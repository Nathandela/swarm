package daemon

// FAILING-FIRST (TDD RED, GG-5) for the two PRODUCER sites of the session-NAME seam
// (agents-tracker-ksvb.1).
//
// agentrecord_test.go states the reason this file exists and it is unchanged: the type change
// alone would LOOK like the fix, because every downstream guard flips green off `journal.Record`
// merely GAINING the field, whether or not either constructor here ever writes to it. So the
// record CONSTRUCTORS get their own assertions.
//
// Both sites already hold the value: persist.Meta.Name -- the user-provided session label -- is
// a field of the very variable each one reads its SessionID out of. Nothing derives, looks up
// or defaults it. An EMPTY name is a session the user never labelled, and it must arrive on the
// phone as empty rather than as a substitute, because the phone's fallback (the id's local
// part) is the only honest thing left to render (ADR-007 B135).

import (
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// namedMeta is metaWith plus the user label. It is a separate helper rather than a widened
// metaWith so agentrecord_test.go's fixtures keep asserting an UNNAMED session, which is the
// state every pre-v0.4.0 session is in.
func namedMeta(id, agent, name string, p status.Process, in status.Interaction) persist.Meta {
	m := metaWith(id, agent, p, in)
	m.Name = name
	return m
}

// TestJournalRecordForCarriesName covers ALL FOUR journalworthy branches, for
// TestJournalRecordForCarriesAgent's reason: one branch would be enough to make the standing
// reds green while three others silently dropped the name.
func TestJournalRecordForCarriesName(t *testing.T) {
	running := namedMeta("s1", "claude", "api refactor", status.ProcessRunning, status.InteractionNone)

	cases := []struct {
		name       string
		prev       persist.Meta
		prevExists bool
		next       persist.Meta
		wantType   journal.RecordType
	}{
		{"launched", persist.Meta{}, false, running, journal.TypeLaunched},
		{"exited", running, true, namedMeta("s1", "claude", "api refactor", status.ProcessExited, status.InteractionNone), journal.TypeExited},
		{"lost", running, true, namedMeta("s1", "claude", "api refactor", status.ProcessLost, status.InteractionNone), journal.TypeLost},
		{"group_transition", running, true, namedMeta("s1", "claude", "api refactor", status.ProcessRunning, status.InteractionPermission), journal.TypeGroupTransition},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, ok := journalRecordFor(tc.prev, tc.prevExists, tc.next)
			if !ok {
				t.Fatalf("journalRecordFor reported not journalworthy; this case is meant to produce a %s record", tc.wantType)
			}
			if rec.Type != tc.wantType {
				t.Fatalf("record type = %q; want %q (the fixture drifted, so the assertion below would measure the wrong branch)", rec.Type, tc.wantType)
			}
			if rec.Name != "api refactor" {
				t.Errorf("%s record carries Name %q; want \"api refactor\", read straight off next.Name -- "+
					"the daemon holds the user's label at this exact line and the phone has no other source for it", tc.wantType, rec.Name)
			}
		})
	}
}

// TestJournalRecordForInventsNoName is the seam guardrail: a meta with no label produces a
// record with no name. Never default, never substitute the agent type or the id -- the phone
// decides its own fallback and cannot tell a fabricated name from a real one.
func TestJournalRecordForInventsNoName(t *testing.T) {
	next := namedMeta("s2", "claude", "", status.ProcessRunning, status.InteractionNone)
	rec, ok := journalRecordFor(persist.Meta{}, false, next)
	if !ok {
		t.Fatalf("journalRecordFor(launched) reported not journalworthy")
	}
	if rec.Name != "" {
		t.Errorf("record carries Name %q for a meta with no label; want the empty string -- "+
			"an invented name reads as the user's own on the phone", rec.Name)
	}
}

// TestRosterSnapshotCarriesName covers the OTHER constructor. The roster is the only path by
// which a phone enumerates a reconcile-adopted session, so a roster record that drops the name
// leaves those sessions showing a raw id forever.
func TestRosterSnapshotCarriesName(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	d.putMem(namedMeta("named-session", "claude", "api refactor", status.ProcessRunning, status.InteractionNone))
	d.putMem(namedMeta("other-session", "codex", "docs pass", status.ProcessRunning, status.InteractionNone))
	d.putMem(namedMeta("unnamed-session", "claude", "", status.ProcessRunning, status.InteractionNone))

	res, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}

	got := map[string]string{}
	for _, r := range res.Roster {
		if r.Type != journal.TypeRoster {
			t.Fatalf("roster carries a %q record; want only roster records", r.Type)
		}
		got[r.SessionID] = r.Name
	}

	for id, want := range map[string]string{
		"named-session":   "api refactor",
		"other-session":   "docs pass",
		"unnamed-session": "",
	} {
		name, ok := got[id]
		if !ok {
			t.Fatalf("session %q missing from the roster snapshot; the fixture is not measuring what it thinks", id)
		}
		if name != want {
			t.Errorf("roster record for %q carries Name %q; want %q verbatim from persist.Meta.Name", id, name, want)
		}
	}
}
