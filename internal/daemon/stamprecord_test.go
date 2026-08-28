package daemon

// FAILING-FIRST (TDD RED, GG-5) for phone-refit-playbook W7.1: the roster snapshot and the four
// journalworthy transitions carry persist.Meta.LastActivity -- the MACHINE's stamp -- at exactly
// the sites Agent and Name are set (agentrecord_test.go), and a zero stamp stays zero.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func metaActiveAt(id string, p status.Process, in status.Interaction, last time.Time) persist.Meta {
	m := metaWith(id, "claude", p, in)
	m.LastActivity = last
	return m
}

func TestJournalRecordForCarriesLastActivity(t *testing.T) {
	last := time.Date(2026, 8, 28, 9, 34, 0, 0, time.UTC)
	running := metaActiveAt("s1", status.ProcessRunning, status.InteractionNone, last.Add(-time.Minute))

	cases := []struct {
		name       string
		prev       persist.Meta
		prevExists bool
		next       persist.Meta
		wantType   journal.RecordType
	}{
		{"launched", persist.Meta{}, false, metaActiveAt("s1", status.ProcessRunning, status.InteractionNone, last), journal.TypeLaunched},
		{"exited", running, true, metaActiveAt("s1", status.ProcessExited, status.InteractionNone, last), journal.TypeExited},
		{"lost", running, true, metaActiveAt("s1", status.ProcessLost, status.InteractionNone, last), journal.TypeLost},
		{"group_transition", running, true, metaActiveAt("s1", status.ProcessRunning, status.InteractionPermission, last), journal.TypeGroupTransition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, ok := journalRecordFor(tc.prev, tc.prevExists, tc.next)
			if !ok || rec.Type != tc.wantType {
				t.Fatalf("journalRecordFor = %+v ok=%v; want a %s record", rec, ok, tc.wantType)
			}
			if !rec.LastActivity.Equal(last) {
				t.Errorf("%s record carries LastActivity %v; want %v read straight off next.LastActivity (W7.1)", tc.wantType, rec.LastActivity, last)
			}
		})
	}
}

func TestRosterSnapshotCarriesLastActivity(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	last := time.Date(2026, 8, 28, 9, 34, 0, 0, time.UTC)

	d.putMem(metaActiveAt("stamped", status.ProcessRunning, status.InteractionNone, last))
	d.putMem(metaActiveAt("unstamped", status.ProcessRunning, status.InteractionNone, time.Time{}))

	res, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	got := map[string]time.Time{}
	for _, r := range res.Roster {
		got[r.SessionID] = r.LastActivity
	}
	if !got["stamped"].Equal(last) {
		t.Errorf("roster record for stamped carries LastActivity %v; want %v -- the roster is the only path by which a reconnected session reaches the phone, so it must carry the stamp (W7.1)", got["stamped"], last)
	}
	if !got["unstamped"].IsZero() {
		t.Errorf("roster record for unstamped carries LastActivity %v; want the zero time, never an invented one", got["unstamped"])
	}
}
