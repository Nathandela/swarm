package protocol

// FAILING-FIRST (RED) test for finding DHI-2 (remote Phase-1 review), protocol tier.
// journal_read's reply must carry the ROSTER snapshot (the atomic-snapshot half of
// R-JRN.4) so a fresh phone (from=0) or a FullResync can resync the live set, not just
// the incremental event tail. handleJournalRead (server.go) threads res.Events onto
// Control.Journal + res.FullResync onto Control.FullResync, but drops res.Roster.
//
// WHY THIS FAILS TODAY: protocol.JournalResume has no Roster field and Control has no
// Roster field, so `JournalResume{Roster: ...}` and `got.Roster` are undefined and the
// protocol package fails to COMPILE — an undefined-only RED.
//
// CONTRACT PINNED (green only once implemented):
//   - protocol.JournalResume gains `Roster []JournalRecord`.
//   - protocol.Control gains `Roster []JournalRecord` with json tag `roster,omitempty`.
//   - handleJournalRead copies res.Roster onto the reply Control's Roster.
//
// This uses a NEW roster-carrying fake JournalBackend (the existing journalStub in
// remote_journal_test.go is NOT modified).

import (
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

// rosterJournalStub is a journal-capable DaemonAPI (via the embedded stubDaemon) whose
// JournalReadFrom returns a JournalResume carrying a roster snapshot. It is distinct
// from journalStub so the frozen existing test file is untouched.
type rosterJournalStub struct {
	*stubDaemon
	resume JournalResume
}

func (j *rosterJournalStub) JournalReadFrom(from uint64) (JournalResume, error) {
	return j.resume, nil
}

func (j *rosterJournalStub) JournalSubscribe() (<-chan JournalRecord, func()) {
	return make(chan JournalRecord), func() {}
}

var (
	_ DaemonAPI      = (*rosterJournalStub)(nil)
	_ JournalBackend = (*rosterJournalStub)(nil)
)

// TestProtocol_JournalReadReplyCarriesRoster_DHI2 asserts the journal_read reply
// Control carries the backend's roster snapshot, with each record's type and
// server-derived group preserved on the wire.
func TestProtocol_JournalReadReplyCarriesRoster_DHI2(t *testing.T) {
	js := &rosterJournalStub{
		stubDaemon: newStubDaemon(),
		resume: JournalResume{
			Cursor: 9,
			Roster: []JournalRecord{
				{Cursor: 9, SessionID: "live-working", Type: "roster", Group: status.GroupWorking},
				{Cursor: 9, SessionID: "live-needs", Type: "roster", Group: status.GroupNeedsInput},
			},
			Events: []JournalRecord{
				{Cursor: 9, SessionID: "live-working", Type: "group_transition", Group: status.GroupWorking},
			},
		},
	}

	sock := tmpSock(t)
	srv, err := Serve(js, sock)
	if err != nil {
		t.Fatalf("Serve(roster journal): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal})

	rc.writeControl(Control{Op: OpJournalRead, EndpointID: rep.EndpointID, Cursor: 0})
	got := rc.readControl()
	if got.Op != OpJournalRead {
		t.Fatalf("journal_read reply op = %q; want %q", got.Op, OpJournalRead)
	}

	if len(got.Roster) != 2 {
		t.Fatalf("journal_read reply carried %d roster records; want 2 (DHI-2: handleJournalRead must thread res.Roster onto the reply Control)", len(got.Roster))
	}

	byID := map[string]JournalRecord{}
	for _, r := range got.Roster {
		byID[r.SessionID] = r
		if r.Type != "roster" {
			t.Fatalf("roster record %s Type = %q; want %q", r.SessionID, r.Type, "roster")
		}
	}
	if g := byID["live-working"].Group; g != status.GroupWorking {
		t.Fatalf("roster record live-working Group = %q; want %q", g, status.GroupWorking)
	}
	if g := byID["live-needs"].Group; g != status.GroupNeedsInput {
		t.Fatalf("roster record live-needs Group = %q; want %q", g, status.GroupNeedsInput)
	}
}
