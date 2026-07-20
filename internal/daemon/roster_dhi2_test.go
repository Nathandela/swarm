package daemon

// FAILING-FIRST (RED) test for finding DHI-2 (remote Phase-1 review). The atomic
// snapshot half of R-JRN.4 — the Resume.Roster — is never populated by the daemon,
// so a fresh phone (from=0) or a FullResync has NOTHING to resync FROM: it can only
// see the incremental event stream, never the live set as-of the read cursor. Worse,
// a reconcile-reconnected Running session (adopted via putMem, persisted=true, with
// NO `launched` journal record) is INVISIBLE in the event stream, so the phone cannot
// enumerate it by any path.
//
// WHY THIS FAILS TODAY: (*Daemon).JournalReadFrom (daemon/journal.go) forwards
// straight to journal.ReadFrom and leaves res.Roster empty; there is no
// journal.TypeRoster constant. This file references journal.TypeRoster (undefined),
// so the daemon package fails to COMPILE — a clean, undefined-only RED.
//
// CONTRACT THIS TEST PINS (green only once implemented):
//   - journal.TypeRoster RecordType constant ("roster"), synthetic / snapshot-only.
//   - (*Daemon).JournalReadFrom populates res.Roster with ONE journal.Record per
//     PERSISTED, non-tombstoned live session: SessionID = the session id,
//     Type = journal.TypeRoster, Group = status.Derive(m.Status). A putMem-adopted
//     reconnected Running session (persisted, no launched record) MUST appear; an
//     unpersisted launch reservation MUST NOT (no phantoms). The roster is a snapshot
//     consistent with res.Cursor.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// TestDaemon_JournalReadFrom_PopulatesRosterSnapshot_DHI2 asserts JournalReadFrom
// returns a roster snapshot over the persisted live sessions — including a reconcile-
// reconnected Running session adopted via putMem that has NO journal event of its own
// — and excludes an unpersisted launch reservation.
func TestDaemon_JournalReadFrom_PopulatesRosterSnapshot_DHI2(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	// (1) The KEY sub-finding: a reconcile-reconnected RUNNING session, adopted into
	// the registry via putMem (persisted=true) with NO `launched` journal record. It is
	// invisible in the event stream, so the roster is the ONLY path by which the phone
	// can enumerate it. Idle + permission derives to NeedsInput (a distinctive group).
	reconnected := persist.Meta{
		ID:           "reconnected-running",
		AgentType:    "fake",
		Cwd:          "/tmp",
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		Status:       status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission},
	}
	d.putMem(reconnected)

	// (2) A second persisted session in a DIFFERENT derived group (exited -> Completed),
	// also adopted memory-only (no journal event), so it too is roster-only.
	adoptedExited := persist.Meta{
		ID:           "adopted-exited",
		AgentType:    "fake",
		Cwd:          "/tmp",
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		Status:       status.Status{Process: status.ProcessExited, Turn: status.TurnIdle, Interaction: status.InteractionNone},
	}
	d.putMem(adoptedExited)

	// (3) An UNPERSISTED launch reservation: it occupies a registry slot before its
	// first saveMeta commits (persisted=false). It MUST NOT appear in the roster — a
	// reservation is not yet a real session (no phantoms).
	d.mu.Lock()
	d.sessions["reserved-phantom"] = &session{
		meta: persist.Meta{
			ID:     "reserved-phantom",
			Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
		},
		stop:      make(chan struct{}),
		persisted: false,
	}
	d.mu.Unlock()

	res, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom(0): %v", err)
	}

	// The reconnected session is INVISIBLE in the event stream (putMem appends no
	// journal record) — this is exactly why the roster is required.
	for _, ev := range res.Events {
		if ev.SessionID == reconnected.ID {
			t.Fatalf("reconnected session %s appeared in the event stream; the DHI-2 premise is that it is event-invisible and reachable only via the roster", reconnected.ID)
		}
	}

	roster := map[string]journal.Record{}
	for _, r := range res.Roster {
		roster[r.SessionID] = r
	}

	// Every roster record is TypeRoster (synthetic snapshot type) and its Cursor does
	// not exceed the snapshot cursor (snapshot as-of res.Cursor).
	for id, r := range roster {
		if r.Type != journal.TypeRoster {
			t.Fatalf("roster record %s Type = %q; want journal.TypeRoster", id, r.Type)
		}
		if r.Cursor > res.Cursor {
			t.Fatalf("roster record %s cursor %d exceeds snapshot cursor %d (roster must be consistent with res.Cursor)", id, r.Cursor, res.Cursor)
		}
	}

	// The reconnected Running session MUST be in the roster, with its server-derived
	// Group (NeedsInput).
	rr, ok := roster[reconnected.ID]
	if !ok {
		t.Fatalf("reconnected Running session %s missing from res.Roster; a reconcile-adopted running session must be enumerable via the roster (DHI-2)", reconnected.ID)
	}
	if rr.Group != status.Derive(reconnected.Status) {
		t.Fatalf("roster record %s Group = %q; want %q (server-derived)", reconnected.ID, rr.Group, status.Derive(reconnected.Status))
	}

	// The adopted exited session MUST be in the roster with Group = Completed.
	er, ok := roster[adoptedExited.ID]
	if !ok {
		t.Fatalf("persisted session %s missing from res.Roster", adoptedExited.ID)
	}
	if er.Group != status.Derive(adoptedExited.Status) {
		t.Fatalf("roster record %s Group = %q; want %q (server-derived)", adoptedExited.ID, er.Group, status.Derive(adoptedExited.Status))
	}

	// The unpersisted reservation MUST NOT appear (no phantoms).
	if _, ok := roster["reserved-phantom"]; ok {
		t.Fatalf("unpersisted launch reservation leaked into res.Roster; only persisted sessions belong in the snapshot")
	}
}
