package daemon

// FAILING-FIRST (TDD RED, GG-5) for phone-refit-playbook W7.1 (as ruled at review): the roster
// snapshot and the four journalworthy transitions carry the MACHINE's stamp of when the session
// entered its current state -- persist.Meta.EffectiveGroupEnteredAt(), never LastActivity, which
// is written only at launch and exit and would age twins launched together identically -- at
// exactly the sites Agent and Name are set (agentrecord_test.go). A meta with no stamp at all
// stays zero.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// metaInStateSince: entered its current state at `since`, with a DIFFERENT LastActivity so the
// two sources cannot be confused.
func metaInStateSince(id string, p status.Process, in status.Interaction, since time.Time) persist.Meta {
	m := metaWith(id, "claude", p, in)
	m.GroupEnteredAt = since
	m.LastActivity = since.Add(-time.Hour)
	return m
}

func TestJournalRecordForCarriesStateSince(t *testing.T) {
	since := time.Date(2026, 8, 28, 9, 34, 0, 0, time.UTC)
	running := metaInStateSince("s1", status.ProcessRunning, status.InteractionNone, since.Add(-time.Minute))

	cases := []struct {
		name       string
		prev       persist.Meta
		prevExists bool
		next       persist.Meta
		wantType   journal.RecordType
	}{
		{"launched", persist.Meta{}, false, metaInStateSince("s1", status.ProcessRunning, status.InteractionNone, since), journal.TypeLaunched},
		{"exited", running, true, metaInStateSince("s1", status.ProcessExited, status.InteractionNone, since), journal.TypeExited},
		{"lost", running, true, metaInStateSince("s1", status.ProcessLost, status.InteractionNone, since), journal.TypeLost},
		{"group_transition", running, true, metaInStateSince("s1", status.ProcessRunning, status.InteractionPermission, since), journal.TypeGroupTransition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, ok := journalRecordFor(tc.prev, tc.prevExists, tc.next)
			if !ok || rec.Type != tc.wantType {
				t.Fatalf("journalRecordFor = %+v ok=%v; want a %s record", rec, ok, tc.wantType)
			}
			if !rec.StateSince.Equal(since) {
				t.Errorf("%s record carries StateSince %v; want %v, next.EffectiveGroupEnteredAt() -- the age is time in the current state, not time since launch (W7.1 ruling)", tc.wantType, rec.StateSince, since)
			}
		})
	}
}

func TestJournalRecordForStateSinceFallsBackAsTheMetaDoes(t *testing.T) {
	// A record written before GroupEnteredAt existed: EffectiveGroupEnteredAt's own fallback
	// (LastActivity, then CreatedAt) is what crosses, not a zero.
	last := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	next := metaWith("s2", "claude", status.ProcessRunning, status.InteractionNone)
	next.GroupEnteredAt = time.Time{}
	next.LastActivity = last
	rec, ok := journalRecordFor(persist.Meta{}, false, next)
	if !ok {
		t.Fatalf("journalRecordFor(launched) reported not journalworthy")
	}
	if !rec.StateSince.Equal(last) {
		t.Errorf("StateSince = %v; want %v, the meta's own fallback for a pre-GroupEnteredAt record", rec.StateSince, last)
	}
}

func TestRosterSnapshotCarriesStateSince(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	since := time.Date(2026, 8, 28, 9, 34, 0, 0, time.UTC)

	d.putMem(metaInStateSince("stamped", status.ProcessRunning, status.InteractionNone, since))
	unstamped := metaWith("unstamped", "claude", status.ProcessRunning, status.InteractionNone)
	unstamped.GroupEnteredAt, unstamped.LastActivity, unstamped.CreatedAt = time.Time{}, time.Time{}, time.Time{}
	d.putMem(unstamped)

	res, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	got := map[string]time.Time{}
	for _, r := range res.Roster {
		got[r.SessionID] = r.StateSince
	}
	if !got["stamped"].Equal(since) {
		t.Errorf("roster record for stamped carries StateSince %v; want %v -- the roster is the only path by which a reconnected session reaches the phone, so it must carry the stamp (W7.1)", got["stamped"], since)
	}
	if !got["unstamped"].IsZero() {
		t.Errorf("roster record for unstamped carries StateSince %v; want the zero time, never an invented one", got["unstamped"])
	}
}
