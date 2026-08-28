package skeleton

// FAILING-FIRST (TDD RED, GG-5) for phone-refit-playbook W7.1 / W7.4: the daemon's own record
// stamp and the session's last-activity stamp cross the wire, carried verbatim the way Name and
// Agent already are (namewire_test.go), and a zero stamp crosses as zero rather than as an epoch.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/status"
)

func TestWireJournalRecordCarriesTS(t *testing.T) {
	ts := time.Date(2026, 8, 28, 9, 38, 0, 0, time.UTC)
	got := toWireJournalRecord(journal.Record{
		Cursor:    7,
		TS:        ts,
		SessionID: "m/s1",
		Type:      journal.TypeGroupTransition,
		Group:     status.Group("working"),
	})
	if !got.TS.Equal(ts) {
		t.Errorf("TS = %v; want %v carried verbatim -- the daemon stamps every appended record and the phone has no other clock for it (W7.4)", got.TS, ts)
	}
}

func TestWireJournalRecordCarriesStateSince(t *testing.T) {
	last := time.Date(2026, 8, 28, 9, 34, 0, 0, time.UTC)
	got := toWireJournalRecord(journal.Record{
		SessionID:    "m/s1",
		Type:         journal.TypeRoster,
		Group:        status.Group("working"),
		StateSince: last,
	})
	if !got.StateSince.Equal(last) {
		t.Errorf("StateSince = %v; want %v carried verbatim -- persist.Meta.StateSince is the machine's stamp and the phone has no other source for it (W7.1)", got.StateSince, last)
	}
}

func TestWireJournalRecordInventsNoStamps(t *testing.T) {
	got := toWireJournalRecord(journal.Record{Cursor: 8, SessionID: "m/s2", Type: journal.TypeExited})
	if !got.TS.IsZero() {
		t.Errorf("TS = %v for a record carrying none; want the zero time", got.TS)
	}
	if !got.StateSince.IsZero() {
		t.Errorf("StateSince = %v for a record carrying none; want the zero time", got.StateSince)
	}
}
