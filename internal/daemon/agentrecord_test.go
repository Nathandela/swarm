package daemon

// FAILING-FIRST (TDD RED, GG-5) for the two PRODUCER sites of the session-agent seam.
//
// The type change alone is not the fix, and this file exists because it would LOOK like
// one: mobile/agentseam_test.go and internal/skeleton/agentwire_test.go both flip green off
// `journal.Record` merely GAINING an Agent field, whether or not either constructor here
// ever writes to it. That is the shape of the defect class android/gate/boundverbledger_test.go
// catalogues -- a symbol that exists, compiles and is traced, with no path that gives it a
// value. So the record CONSTRUCTORS get their own assertions.
//
// Both sites already hold the value: persist.Meta.AgentType is a field of the very variable
// each one reads its SessionID out of. Nothing derives, looks up or defaults it.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// metaWith is a persisted session meta in a given process/interaction state.
func metaWith(id, agent string, p status.Process, in status.Interaction) persist.Meta {
	return persist.Meta{
		ID:           id,
		AgentType:    agent,
		Cwd:          "/tmp",
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		Status:       status.Status{Process: p, Turn: status.TurnIdle, Interaction: in},
	}
}

// TestJournalRecordForCarriesAgent covers ALL FOUR journalworthy branches. One branch would
// be enough to make the standing reds green while three others silently dropped the agent,
// so each is asserted separately rather than via the first one that happens to fire.
func TestJournalRecordForCarriesAgent(t *testing.T) {
	running := metaWith("s1", "claude", status.ProcessRunning, status.InteractionNone)

	cases := []struct {
		name       string
		prev       persist.Meta
		prevExists bool
		next       persist.Meta
		wantType   journal.RecordType
	}{
		{"launched", persist.Meta{}, false, running, journal.TypeLaunched},
		{"exited", running, true, metaWith("s1", "claude", status.ProcessExited, status.InteractionNone), journal.TypeExited},
		{"lost", running, true, metaWith("s1", "claude", status.ProcessLost, status.InteractionNone), journal.TypeLost},
		{"group_transition", running, true, metaWith("s1", "claude", status.ProcessRunning, status.InteractionPermission), journal.TypeGroupTransition},
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
			if rec.Agent != "claude" {
				t.Errorf("%s record carries Agent %q; want claude, read straight off next.AgentType -- "+
					"the daemon holds the agent at this exact line and the phone has no other source for it", tc.wantType, rec.Agent)
			}
		})
	}
}

// TestJournalRecordForInventsNoAgent is the seam guardrail: a meta with no agent type
// produces a record with no agent. Never default, never substitute a plausible-looking one.
func TestJournalRecordForInventsNoAgent(t *testing.T) {
	next := metaWith("s2", "", status.ProcessRunning, status.InteractionNone)
	rec, ok := journalRecordFor(persist.Meta{}, false, next)
	if !ok {
		t.Fatalf("journalRecordFor(launched) reported not journalworthy")
	}
	if rec.Agent != "" {
		t.Errorf("record carries Agent %q for a meta with no AgentType; want the empty string -- "+
			"an invented agent reads as a real one on the phone", rec.Agent)
	}
}

// TestRosterSnapshotCarriesAgent covers the OTHER constructor. The roster is the only path
// by which a phone enumerates a reconcile-adopted session (roster_dhi2_test.go), so a roster
// record that drops the agent leaves those sessions unlabelled forever.
func TestRosterSnapshotCarriesAgent(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	d.putMem(metaWith("claude-session", "claude", status.ProcessRunning, status.InteractionNone))
	d.putMem(metaWith("codex-session", "codex", status.ProcessRunning, status.InteractionNone))
	d.putMem(metaWith("agentless-session", "", status.ProcessRunning, status.InteractionNone))

	res, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}

	got := map[string]string{}
	for _, r := range res.Roster {
		if r.Type != journal.TypeRoster {
			t.Fatalf("roster carries a %q record; want only roster records", r.Type)
		}
		got[r.SessionID] = r.Agent
	}

	for id, want := range map[string]string{
		"claude-session":    "claude",
		"codex-session":     "codex",
		"agentless-session": "",
	} {
		agent, ok := got[id]
		if !ok {
			t.Fatalf("session %q missing from the roster snapshot; the fixture is not measuring what it thinks", id)
		}
		if agent != want {
			t.Errorf("roster record for %q carries Agent %q; want %q verbatim from persist.Meta.AgentType", id, agent, want)
		}
	}
}
