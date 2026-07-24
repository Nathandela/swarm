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

	// A healthy subscriber, drained in LOCKSTEP with the flood below (send a bounded
	// batch, then drain this subscriber to completion before sending the next). The
	// lockstep is what makes its survival LOAD-INDEPENDENT: because it is drained fully
	// between batches, at most `batch` (< eventQueueCap) records are ever outstanding for
	// it, so its bounded fan-out queue can never overflow no matter how starved its writer
	// gets under CPU pressure — it survives BY CONSTRUCTION, not by out-scheduling a
	// continuous flood. The earlier continuous-drain version could lose that race under
	// heavy load: the healthy subscriber's own 256-queue overflowed and it evicted too,
	// dropping jsubs to 0 and failing the "only the wedged one evicted" assertion. The
	// drain verifies strictly increasing cursor order on every frame; a regression fails.
	live := rawDial(t, sock)
	lep := live.hello(Version, []string{CapJournal})
	live.writeControl(Control{Op: OpJournalSubscribe, EndpointID: lep.EndpointID})
	_ = live.readControl() // subscribe OK

	// drainHealthy blocks until it has read exactly n journal_event frames from the
	// healthy subscriber, asserting strictly increasing cursor order. A read error here
	// (the deadline) would mean the healthy subscriber did NOT receive an expected frame —
	// i.e. its bounded queue overflowed and dropped one — which the lockstep loop makes
	// impossible by construction; the deadline is only a hang-guard. Non-journal frames
	// are skipped and not counted.
	var last uint64
	liveGot := 0
	drainHealthy := func(n int) {
		t.Helper()
		_ = live.conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		for got := 0; got < n; {
			typ, payload, err := wire.ReadFrame(live.conn)
			if err != nil {
				t.Fatalf("healthy subscriber read failed after %d/%d frames of a batch "+
					"(its bounded queue must never overflow by construction): %v", got, n, err)
			}
			if typ != wire.TControl {
				continue
			}
			ev, derr := DecodeControl(payload)
			if derr != nil || ev.Op != OpJournalEvent {
				continue
			}
			if ev.Cursor <= last {
				t.Fatalf("journal_event cursor order regressed on the healthy subscriber: got %d after %d", ev.Cursor, last)
			}
			last = ev.Cursor
			liveGot++
			got++
		}
	}

	// LOCKSTEP flood/drain until the WEDGED subscriber is evicted (jsubs 2 -> 1). Each
	// round emits a bounded batch into the single journal source, then drains the healthy
	// subscriber fully — so the healthy subscriber never has more than `batch` records
	// outstanding, and with batch < eventQueueCap (256) its fan-out queue can NEVER
	// overflow regardless of CPU scheduling. That removes the load-sensitivity that made
	// this test flaky under heavy load. The wedged subscriber is never drained and its
	// writer is blocked on a bounded kernel buffer (journalSndBuf), so every batch
	// accumulates monotonically in its 256-queue until it overflows and distributeJournal
	// evicts it (delete(s.jsubs, sc)) — observed here at its source of truth by polling
	// len(srv.jsubs). Eviction is inevitable under a monotonically growing queue, so the
	// wall-clock deadline is a pure liveness net, not a throughput/rate gate, and CPU
	// pressure cannot false-fail it. This test is package protocol, so it reads srv.jsubs
	// under srv.jsubMu directly.
	const batch = 64
	next := uint64(1)
	evictDeadline := time.Now().Add(30 * time.Second)
	evicted := false
	for time.Now().Before(evictDeadline) {
		for i := 0; i < batch; i++ {
			js.source <- JournalRecord{Cursor: next, SessionID: "s1", Type: "group_transition"}
			next++
		}
		drainHealthy(batch)
		srv.jsubMu.Lock()
		n := len(srv.jsubs)
		srv.jsubMu.Unlock()
		if n < 2 {
			evicted = true
			break
		}
	}
	if !evicted {
		t.Fatal("wedged journal subscriber not evicted within bound (S9/P-3)")
	}

	// The healthy subscriber survived the eviction, stayed strictly ordered (asserted in
	// drainHealthy), and is the sole remaining subscriber. A floor of >= 2 frames only
	// proves the stream reached the drain at all (order is the guarded property, not rate).
	if liveGot < 2 {
		t.Fatalf("healthy subscriber received only %d ordered journal_event frames; want >= 2", liveGot)
	}
	srv.jsubMu.Lock()
	remaining := len(srv.jsubs)
	srv.jsubMu.Unlock()
	if remaining != 1 {
		t.Fatalf("after eviction jsubs has %d subscribers; want 1 (only the wedged one evicted, healthy survives)", remaining)
	}

	// Confirm the wedged subscriber's connection is actually torn down (safe/non-racy now
	// that eviction was already observed and the flood has stopped; these draining reads
	// no longer race the overflow).
	if !wedged.eventuallyClosed(3 * time.Second) {
		t.Fatalf("wedged journal subscriber connection not closed after eviction (S9/P-3)")
	}
}
