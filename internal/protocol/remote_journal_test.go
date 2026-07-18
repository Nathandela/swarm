package protocol

// FAILING-FIRST protocol tests for the journal ops (plan R-PROT.3, ADR-007 D6).
// journal_subscribe streams ordered journal_event frames reusing the existing
// bounded-queue evict-the-wedged-subscriber discipline (S9/L1); journal_read
// returns a snapshot+range from a cursor (atomic per R-JRN.4). RED is
// undefined-only.
//
// FROZEN API these tests expect:
//
//	// The Server enables journal ops when its DaemonAPI ALSO implements JournalBackend
//	// (optional-interface type assertion, matching the existing stopEvents() seam) and
//	// the `journal` capability was negotiated.
//	type JournalRecord struct {
//	    Cursor    uint64       `json:"cursor"`
//	    SessionID string       `json:"session_id"`
//	    Type      string       `json:"type"`
//	    Group     status.Group `json:"group,omitempty"`
//	}
//	type JournalResume struct { Cursor uint64; Events []JournalRecord; FullResync bool }
//	type JournalBackend interface {
//	    JournalReadFrom(from uint64) (JournalResume, error)
//	    JournalSubscribe() (<-chan JournalRecord, func()) // single source; Server fans out (S9)
//	}
//	// Additive omitempty Control carriers for journal payloads.
//	//   Journal    []JournalRecord `json:"journal,omitempty"`
//	//   FullResync bool            `json:"full_resync,omitempty"`

import (
	"sync"
	"testing"
	"time"
)

// journalStub is a DaemonAPI (via the embedded stubDaemon) that ALSO implements the
// expected JournalBackend, so the Server exposes journal ops over it.
type journalStub struct {
	*stubDaemon
	mu     sync.Mutex
	resume JournalResume
	source chan JournalRecord
}

func newJournalStub() *journalStub {
	return &journalStub{stubDaemon: newStubDaemon(), source: make(chan JournalRecord, 4096)}
}

func (j *journalStub) JournalReadFrom(from uint64) (JournalResume, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []JournalRecord
	for _, e := range j.resume.Events {
		if e.Cursor > from {
			out = append(out, e)
		}
	}
	return JournalResume{Cursor: j.resume.Cursor, Events: out, FullResync: j.resume.FullResync}, nil
}

func (j *journalStub) JournalSubscribe() (<-chan JournalRecord, func()) {
	return j.source, func() {}
}

func (j *journalStub) seed(recs ...JournalRecord) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.resume.Events = recs
	if n := len(recs); n > 0 {
		j.resume.Cursor = recs[n-1].Cursor
	}
}

// Compile-time proof the stub satisfies both surfaces (undefined until implemented).
var (
	_ DaemonAPI      = (*journalStub)(nil)
	_ JournalBackend = (*journalStub)(nil)
)

// serveJournal stands up a Server over a journal-capable DaemonAPI. The Server is
// expected to expose journal ops when its backend also implements JournalBackend
// (optional-interface assertion) — so passing js (which satisfies both) through the
// existing Serve entry point is enough.
func serveJournal(t *testing.T, js *journalStub) string {
	t.Helper()
	sock := tmpSock(t)
	srv, err := Serve(js, sock)
	if err != nil {
		t.Fatalf("Serve(journal): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// TestProtocol_JournalReadFromCursor (R-PROT.3): journal_read(from_cursor) returns
// every record with cursor > from, in order, with the boundary cursor.
func TestProtocol_JournalReadFromCursor(t *testing.T) {
	js := newJournalStub()
	js.seed(
		JournalRecord{Cursor: 1, SessionID: "s1", Type: "launched"},
		JournalRecord{Cursor: 2, SessionID: "s1", Type: "group_transition"},
		JournalRecord{Cursor: 3, SessionID: "s2", Type: "launched"},
		JournalRecord{Cursor: 4, SessionID: "s1", Type: "exited"},
		JournalRecord{Cursor: 5, SessionID: "s2", Type: "group_transition"},
	)
	sock := serveJournal(t, js)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal})

	rc.writeControl(Control{Op: OpJournalRead, EndpointID: rep.EndpointID, Cursor: 2})
	got := rc.readControl()
	if got.Op != OpJournalRead {
		t.Fatalf("journal_read reply op = %q; want %q", got.Op, OpJournalRead)
	}
	if got.Cursor != 5 {
		t.Fatalf("journal_read boundary cursor = %d; want 5", got.Cursor)
	}
	wantCursors := []uint64{3, 4, 5}
	if len(got.Journal) != len(wantCursors) {
		t.Fatalf("journal_read returned %d records; want %d (cursor>2)", len(got.Journal), len(wantCursors))
	}
	for i, rec := range got.Journal {
		if rec.Cursor != wantCursors[i] {
			t.Fatalf("journal_read record %d cursor = %d; want %d (ordered, cursor>from)", i, rec.Cursor, wantCursors[i])
		}
	}
}

// TestProtocol_JournalSubscribeOrderedAndEvictsWedged (R-PROT.3): journal_subscribe
// streams journal_event frames in cursor order; a wedged subscriber (never reading)
// is disconnected within a bound, never blocking the fan-out (S9/L1 discipline).
func TestProtocol_JournalSubscribeOrderedAndEvictsWedged(t *testing.T) {
	js := newJournalStub()
	sock := serveJournal(t, js)

	// A wedged subscriber that never drains its socket.
	wedged := rawDial(t, sock)
	wep := wedged.hello(Version, []string{CapJournal})
	wedged.writeControl(Control{Op: OpJournalSubscribe, EndpointID: wep.EndpointID})
	_ = wedged.readControl() // the subscribe OK; then never read again

	// A healthy subscriber that drains and checks ordering.
	live := rawDial(t, sock)
	lep := live.hello(Version, []string{CapJournal})
	live.writeControl(Control{Op: OpJournalSubscribe, EndpointID: lep.EndpointID})
	_ = live.readControl() // subscribe OK

	// Flood the single journal source: ordered cursors, more than one bounded queue
	// holds, so the wedged subscriber overflows and is evicted.
	go func() {
		for i := uint64(1); i <= uint64(eventQueueCap+200); i++ {
			js.source <- JournalRecord{Cursor: i, SessionID: "s1", Type: "group_transition"}
		}
	}()

	// The healthy subscriber sees journal_event frames in strictly increasing cursor
	// order.
	var last uint64
	for n := 0; n < 20; n++ {
		ev := live.readControl()
		if ev.Op != OpJournalEvent {
			t.Fatalf("live subscriber got op %q; want %q", ev.Op, OpJournalEvent)
		}
		if ev.Cursor <= last {
			t.Fatalf("journal_event out of order: cursor %d after %d", ev.Cursor, last)
		}
		last = ev.Cursor
	}

	// The wedged subscriber is disconnected within a bound.
	if !wedged.eventuallyClosed(2 * time.Second) {
		t.Fatalf("wedged journal subscriber not evicted within bound (S9/P-3)")
	}
}
