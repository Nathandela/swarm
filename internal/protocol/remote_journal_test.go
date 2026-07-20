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

	// Wait until the healthy subscriber has received MORE than eventQueueCap frames, in
	// strictly increasing cursor order, before checking eviction. This is the key
	// synchronization: distributeJournal delivers each record to BOTH subscribers in the
	// same pass, so once the healthy subscriber has drained > eventQueueCap records, the
	// fan-out has attempted at least that many deliveries to the wedged subscriber's
	// queue too — which, with the wedged writer blocked on its bounded socket buffer,
	// overflowed at eventQueueCap and triggered eviction. Gating on a small count (e.g.
	// 20) instead raced eventuallyClosed's draining reads against a not-yet-overflowed
	// wedged queue, which un-wedged it under -race (the fan-out is slower there, so the
	// queue had not yet filled when we started reading).
	const wantLive = eventQueueCap + 64
	// Generous deadline: under `go test ./...` full-package parallelism on a loaded
	// machine the fan-out goroutine can be CPU-starved (the live subscriber receives
	// frames slowly). The bound tolerates that without weakening the assertion. NOTE:
	// this eviction property is inherently CPU-scheduling sensitive — the pre-existing
	// TestFanout_WedgedSubscriberDisconnectedWithinBound has the same sensitivity and
	// also fails under severe parallel starvation; both are reliable in isolation and
	// on an unloaded CI box. See docs/verification/remote-phase1-daemon-evidence.md.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n, regressed := liveGot, orderRegressed
		mu.Unlock()
		if regressed {
			t.Fatal("journal_event cursor order regressed on the healthy subscriber")
		}
		if n >= wantLive {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	n, regressed := liveGot, orderRegressed
	mu.Unlock()
	if regressed {
		t.Fatal("journal_event cursor order regressed on the healthy subscriber")
	}
	if n < wantLive {
		t.Fatalf("healthy subscriber received only %d ordered journal_event frames; want >= %d (a continuous stream)", n, wantLive)
	}

	// The wedged subscriber is disconnected within a bound (its queue overflowed above).
	evicted := wedged.eventuallyClosed(3 * time.Second)
	close(stopFlood)
	<-floodDone
	if !evicted {
		t.Fatalf("wedged journal subscriber not evicted within bound (S9/P-3)")
	}
}
