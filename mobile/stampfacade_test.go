package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for phone-refit-playbook W7.1 / W7.4: the two additive facade
// fields. A zero stamp crosses as 0 -- Kotlin draws no age and no time for 0, and never the epoch.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func TestSessionCarriesLastActivityUnixMs(t *testing.T) {
	var a App
	last := time.Date(2026, 8, 28, 9, 34, 0, 0, time.UTC)

	s := a.session(phonecore.CachedSession{SessionID: "m/s1", Group: status.Group("working"), Present: true, LastActivity: last})
	if s.LastActivityUnixMs != last.UnixMilli() {
		t.Errorf("Session.LastActivityUnixMs = %d; want %d, the machine's stamp in the unit Kotlin ages from", s.LastActivityUnixMs, last.UnixMilli())
	}

	zero := a.session(phonecore.CachedSession{SessionID: "m/s2", Group: status.Group("working"), Present: true})
	if zero.LastActivityUnixMs != 0 {
		t.Errorf("Session.LastActivityUnixMs = %d for a session with no stamp; want 0 -- 0 is absent, and time.Time{}.UnixMilli() is a date in 1754 that would draw as a 272-year age", zero.LastActivityUnixMs)
	}
}

func TestJournalEntryCarriesTSUnixMs(t *testing.T) {
	a := transcriptApp(t)
	ts := time.Date(2026, 8, 28, 9, 38, 0, 0, time.UTC)

	a.onJournal(schema.JournalRecord{Cursor: 5, SessionID: "m/s1", Type: "launched", Group: status.Group("working"), TS: ts})
	a.onJournal(schema.JournalRecord{Cursor: 6, SessionID: "m/s1", Type: "group_transition", Group: status.Group("needs_input")})

	page, err := a.ReadJournal(0, 0)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	stamped, err := page.At(0)
	if err != nil {
		t.Fatal(err)
	}
	if stamped.TSUnixMs != ts.UnixMilli() {
		t.Errorf("JournalEntry.TSUnixMs = %d; want %d, the daemon's own record stamp (W7.4)", stamped.TSUnixMs, ts.UnixMilli())
	}
	unstamped, err := page.At(1)
	if err != nil {
		t.Fatal(err)
	}
	if unstamped.TSUnixMs != 0 {
		t.Errorf("JournalEntry.TSUnixMs = %d for a record with no stamp; want 0, never the epoch", unstamped.TSUnixMs)
	}
}
