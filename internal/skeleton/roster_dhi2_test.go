package skeleton

// FAILING-FIRST (RED) test for finding DHI-2 (remote Phase-1 review), skeleton tier.
// coreAPI.JournalReadFrom (skeleton/api.go) converts the daemon journal.Resume into
// the wire protocol.JournalResume, but today it maps ONLY res.Events -> out.Events; it
// drops the roster snapshot entirely. Once the daemon populates res.Roster (DHI-2),
// the adapter must also map res.Roster -> out.Roster (wire records: Cursor, SessionID,
// Type, Group) so a fresh phone can resync the live set over the assembled remote path.
//
// WHY THIS FAILS TODAY: protocol.JournalResume has no Roster field and journal has no
// TypeRoster constant, so `out.Roster` / `journal.TypeRoster` are undefined and the
// skeleton package fails to COMPILE — an undefined-only RED. Even once those exist,
// coreAPI.JournalReadFrom must be taught to fill out.Roster for this test to pass.
//
// This exercises the REAL production adapter (sk.api is the *coreAPI) over a real
// *daemon.Daemon, mirroring remote_journal_test.go's assembled-path style.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/protocol"
)

// TestCoreAPIJournalReadFrom_MapsRosterToWire_DHI2 launches a persisted, running
// session and asserts coreAPI.JournalReadFrom surfaces it in out.Roster as a wire
// record carrying the roster type and the server-derived group.
func TestCoreAPIJournalReadFrom_MapsRosterToWire_DHI2(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print HELLO\nidle 60s\n")

	// A launched session is persisted + running, so it belongs to the daemon roster
	// snapshot and must be mapped by coreAPI.JournalReadFrom into the wire out.Roster.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err := sk.api.JournalReadFrom(0)
		if err != nil {
			t.Fatalf("coreAPI.JournalReadFrom: %v", err)
		}
		for _, r := range out.Roster {
			if r.SessionID != m.ID {
				continue
			}
			// Wire record fields carried through from the daemon roster snapshot.
			if r.Type != string(journal.TypeRoster) {
				t.Fatalf("roster wire record Type = %q; want %q", r.Type, journal.TypeRoster)
			}
			if r.Group == "" {
				t.Fatalf("roster wire record for %s carried an empty Group; want the server-derived group", m.ID)
			}
			if r.Cursor > out.Cursor {
				t.Fatalf("roster wire record cursor %d exceeds snapshot cursor %d", r.Cursor, out.Cursor)
			}
			return // roster snapshot mapped end-to-end through the adapter
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("coreAPI.JournalReadFrom never mapped res.Roster into out.Roster for launched session %s (DHI-2: the adapter drops the roster snapshot)", m.ID)
}

// compile-time proof the real adapter still satisfies protocol.JournalBackend once the
// roster field is threaded (a guard against an accidental signature drift).
var _ protocol.JournalBackend = (*coreAPI)(nil)
