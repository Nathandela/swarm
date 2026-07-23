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

	"github.com/Nathandela/swarm/internal/wire"
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
func serveJournal(t *testing.T, js *journalStub) (string, *Server) {
	t.Helper()
	sock := tmpSock(t)
	srv, err := Serve(js, sock)
	if err != nil {
		t.Fatalf("Serve(journal): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock, srv
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
	sock, _ := serveJournal(t, js)
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
	sock, srv := serveJournal(t, js)

	// A wedged subscriber that never drains its socket. Bound its kernel RECEIVE
	// buffer so a modest flood of the small journal frames overflows it and blocks the
	// server's writer promptly — otherwise, on a default (large, OS-autotuned) recv
	// buffer, thousands of tiny journal_event frames are absorbed by the kernel before
	// the writer ever blocks, so the queue never overflows and eviction cannot be
	// observed within a bounded flood (especially under -race, where the fan-out is
	// throttled). This models a real subscriber with a bounded socket buffer; the
	// server side cannot control a remote peer's recv buffer, so the eviction
	// discipline it DOES own (bounded queue + evict-on-overflow) is what is under test.
	wedged := rawDial(t, sock)
	if uc, ok := wedged.conn.(interface{ SetReadBuffer(int) error }); ok {
		if err := uc.SetReadBuffer(4 << 10); err != nil {
			t.Fatalf("bound wedged recv buffer: %v", err)
		}
	}
	wep := wedged.hello(Version, []string{CapJournal})
	wedged.writeControl(Control{Op: OpJournalSubscribe, EndpointID: wep.EndpointID})
	_ = wedged.readControl() // the subscribe OK; then never read again

	// A healthy subscriber, drained CONTINUOUSLY in the background — exactly as
	// TestFanout_WedgedSubscriberDisconnectedWithinBound drains its healthy peer. This
	// is what keeps it from becoming wedged itself: reading a FIXED prefix and then
	// stopping (as an earlier version did) leaves the "healthy" subscriber wedged too,
	// so under load eviction becomes a race between the two subscribers, and the
	// wedged one is not reliably the one evicted. The drainer verifies strictly
	// increasing cursor order on every frame; a gap is allowed (a momentarily-behind
	// subscriber may drop a record, resynced via journal_read, R-JRN.6), a regression
	// is not.
	live := rawDial(t, sock)
	lep := live.hello(Version, []string{CapJournal})
	live.writeControl(Control{Op: OpJournalSubscribe, EndpointID: lep.EndpointID})
	_ = live.readControl() // subscribe OK

	var mu sync.Mutex
	var liveGot int
	var orderRegressed bool
	// Blocking-read drainer under a single generous deadline (NOT a per-iteration
	// deadline + tight poll, which burns CPU on SetReadDeadline/syscalls and — under
	// -race on a loaded machine — starves the server's fan-out goroutine that must run
	// to evict the wedged subscriber). t.Cleanup closes live.conn, which unblocks and
	// ends this goroutine; no explicit join is needed.
	_ = live.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	go func() {
		var last uint64
		for {
			typ, payload, err := wire.ReadFrame(live.conn)
			if err != nil {
				return // deadline, conn closed at cleanup, or other error: stop
			}
			if typ != wire.TControl {
				continue
			}
			ev, derr := DecodeControl(payload)
			if derr != nil || ev.Op != OpJournalEvent {
				continue
			}
			mu.Lock()
			if ev.Cursor <= last {
				orderRegressed = true
			}
			last = ev.Cursor
			liveGot++
			mu.Unlock()
		}
	}()

	// Flood the single journal source CONTINUOUSLY until the test signals stop. The
	// wedged subscriber's queue must OVERFLOW (triggering eviction) before the
	// eventuallyClosed check below, whose reads would otherwise DRAIN and un-wedge it.
	// Nothing reads the wedged conn until then, and its kernel send buffer is bounded
	// (journalSndBuf) so its writer blocks after a few KB — so its bounded queue backs up
	// monotonically to overflow. A continuous flood (not a fixed burst) guarantees the
	// fan-out has enough records in flight to reach that overflow even when it is throttled
	// under -race.
	stopFlood := make(chan struct{})
	floodDone := make(chan struct{})
	go func() {
		defer close(floodDone)
		for i := uint64(1); ; i++ {
			select {
			case <-stopFlood:
				return
			case js.source <- JournalRecord{Cursor: i, SessionID: "s1", Type: "group_transition"}:
			}
		}
	}()

	// Observe eviction DIRECTLY at its source of truth: distributeJournal removes a
	// wedged subscriber from srv.jsubs (delete(s.jsubs, sc)) the moment its bounded
	// queue overflows. Under the continuous flood, the wedged subscriber's queue fills
	// monotonically (its writer is blocked on a bounded socket buffer, nothing drains
	// it), so eviction is INEVITABLE — the deadline below is a pure liveness net, not a
	// throughput/rate gate, so it does not false-fail under CPU pressure (the earlier
	// "receive >= eventQueueCap+64 frames within 15s wall-clock" barrier did). This test
	// is package protocol, so it reads srv.jsubs under srv.jsubMu directly.
	//
	// jsubs starts with two subscribers (wedged + healthy); eviction of the wedged one
	// drops it to one. Poll for n < 2.
	evictDeadline := time.Now().Add(30 * time.Second)
	evicted := false
	for time.Now().Before(evictDeadline) {
		mu.Lock()
		regressed := orderRegressed
		mu.Unlock()
		if regressed {
			t.Fatal("journal_event cursor order regressed on the healthy subscriber")
		}
		srv.jsubMu.Lock()
		n := len(srv.jsubs)
		srv.jsubMu.Unlock()
		if n < 2 {
			evicted = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !evicted {
		t.Fatal("wedged journal subscriber not evicted within bound (S9/P-3)")
	}

	// The healthy subscriber survived the eviction, stayed strictly ordered, and is the
	// sole remaining subscriber. No count-in-window: a floor of >= 2 frames only proves
	// the stream reached the drainer at all (order is the guarded property, not rate).
	mu.Lock()
	liveN, regressed := liveGot, orderRegressed
	mu.Unlock()
	if regressed {
		t.Fatal("journal_event cursor order regressed on the healthy subscriber")
	}
	if liveN < 2 {
		t.Fatalf("healthy subscriber received only %d ordered journal_event frames; want >= 2", liveN)
	}
	srv.jsubMu.Lock()
	remaining := len(srv.jsubs)
	srv.jsubMu.Unlock()
	if remaining != 1 {
		t.Fatalf("after eviction jsubs has %d subscribers; want 1 (only the wedged one evicted, healthy survives)", remaining)
	}

	// Confirm the wedged subscriber's connection is actually torn down (now safe/non-racy
	// since eviction was already observed above; these draining reads no longer race the
	// overflow).
	if !wedged.eventuallyClosed(3 * time.Second) {
		t.Fatalf("wedged journal subscriber connection not closed after eviction (S9/P-3)")
	}
	close(stopFlood)
	<-floodDone
}
