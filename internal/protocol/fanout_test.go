package protocol

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

// E6.5 — event fan-out with bounded per-client queues (S9, L1, P-3). A status
// change reaches a live subscriber within 1 s (L1); a wedged subscriber (one
// that stops reading its socket) is disconnected within a bound and never blocks
// the daemon's event loop, persistence, or other subscribers (S9).

// statusMeta builds a running session meta whose status derives to the given
// group inputs.
func statusMeta(id string, turn status.Turn, inter status.Interaction) persist.Meta {
	return persist.Meta{
		ID:        id,
		AgentType: "claude",
		Cwd:       "/tmp",
		Status:    status.Status{Process: status.ProcessRunning, Turn: turn, Interaction: inter},
	}
}

// TestFanout_StatusChangeReachesLiveSubscriberWithin1s asserts L1: a published
// status change is delivered to a subscribed client within one second, carrying
// the daemon-computed group.
func TestFanout_StatusChangeReachesLiveSubscriberWithin1s(t *testing.T) {
	stub := newStubDaemon()
	stub.setMetas(statusMeta("sess1", status.TurnActive, status.InteractionNone))
	sock := serveStub(t, stub)

	c := dialClient(t, sock, []string{"subscribe"})
	ch, err := c.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// A session transitions to needs-input.
	stub.pushStatus(statusMeta("sess1", status.TurnIdle, status.InteractionPermission))

	ev, ok := recvEvent(t, ch, oneSecond)
	if !ok {
		t.Fatalf("status change not delivered to subscriber within %s (violates L1)", oneSecond)
	}
	if ev.Session.Group != status.GroupNeedsInput {
		t.Errorf("event group = %q, want %q (server must compute the group)", ev.Session.Group, status.GroupNeedsInput)
	}
	if ev.Session.EndpointID != c.EndpointID() {
		t.Errorf("event endpoint id = %q, want the subscriber's %q", ev.Session.EndpointID, c.EndpointID())
	}
}

// serveStubServer stands up a Server over stub on a fresh socket, with cleanup, and
// returns BOTH the socket path and the *Server so a package-internal test can observe
// the subscriber map (srv.subs) directly at its source of truth — the load-independent
// signal for eviction (mirrors serveJournal in remote_journal_test.go).
func serveStubServer(t *testing.T, stub *stubDaemon) (string, *Server) {
	t.Helper()
	sock := tmpSock(t)
	srv, err := Serve(stub, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock, srv
}

// TestFanout_WedgedSubscriberDisconnectedWithinBound asserts S9: a subscriber that
// stops reading is disconnected within a bound, and a healthy subscriber keeps
// receiving events — the wedged one never blocks the event loop.
//
// LOAD-INDEPENDENT by construction (mirrors TestProtocol_JournalSubscribeOrderedAndEvictsWedged):
// the wedged subscriber's eviction is observed DIRECTLY at its source of truth — the server
// deletes it from s.subs under s.subMu in distribute, and this test is package protocol so it
// polls len(srv.subs) dropping 2 -> 1 — instead of inferring it from a wall-clock throughput
// race. The healthy subscriber is drained in LOCKSTEP with the flood (push a bounded batch, then
// drain it to completion before the next), so at most `batch` (< eventQueueCap) events are ever
// outstanding for it and its bounded fan-out queue can NEVER overflow regardless of CPU
// scheduling — it survives BY CONSTRUCTION, not by out-scheduling a continuous flood. The earlier
// version gated on WALL-CLOCK throughput (healthy sub had to receive >= 1 event within 1 s), which
// false-failed under full-parallel CPU contention where its delivery is merely delayed, not
// starved.
func TestFanout_WedgedSubscriberDisconnectedWithinBound(t *testing.T) {
	stub := newStubDaemon()
	stub.setMetas(statusMeta("sess1", status.TurnActive, status.InteractionNone))
	sock, srv := serveStubServer(t, stub)

	// Wedged subscriber: subscribes but never reads its socket again. Bound its kernel RECEIVE
	// buffer so a modest flood of the small event frames overflows it and blocks the server's
	// writer promptly — otherwise, on a default (large, OS-autotuned) recv buffer, the kernel
	// absorbs many tiny event frames before the writer ever blocks, so the bounded queue never
	// overflows and eviction cannot be observed within a bounded flood (especially under -race).
	// This models a real subscriber with a bounded socket buffer; the server owns the eviction
	// discipline (bounded queue + evict-on-overflow), which is what is under test. Mirrors the
	// journal sibling.
	wedged := rawDial(t, sock)
	if uc, ok := wedged.conn.(interface{ SetReadBuffer(int) error }); ok {
		if err := uc.SetReadBuffer(4 << 10); err != nil {
			t.Fatalf("bound wedged recv buffer: %v", err)
		}
	}
	wep := wedged.hello(Version, []string{CapSubscribe})
	wedged.writeControl(Control{Op: OpSubscribe, EndpointID: wep.EndpointID})
	_ = wedged.readControl() // the subscribe OK; then never read again

	// Healthy subscriber, drained in LOCKSTEP with the flood below (send a bounded batch, then
	// drain this subscriber to completion before sending the next). The lockstep is what makes its
	// survival LOAD-INDEPENDENT: because it is drained fully between batches, at most `batch`
	// (< eventQueueCap) events are ever outstanding for it, so its bounded fan-out queue can never
	// overflow no matter how starved its writer gets under CPU pressure — it survives BY
	// CONSTRUCTION, not by out-scheduling a continuous flood. A raw conn (not the Client API) so
	// the drain is an explicit, counted frame read.
	healthy := rawDial(t, sock)
	hep := healthy.hello(Version, []string{CapSubscribe})
	healthy.writeControl(Control{Op: OpSubscribe, EndpointID: hep.EndpointID})
	_ = healthy.readControl() // subscribe OK

	// drainHealthy blocks until it has read exactly n event frames from the healthy subscriber. A
	// read error here (the deadline) would mean the healthy subscriber did NOT receive an expected
	// event — i.e. its bounded queue overflowed and dropped one, OR the fan-out loop was blocked by
	// the wedged peer (violating S9) — both of which the lockstep loop makes impossible by
	// construction; the deadline is only a hang-guard. Each event must carry the daemon-computed
	// group (GroupWorking here), proving the group survives the fan-out. Non-event frames are
	// skipped and not counted.
	healthyGot := 0
	drainHealthy := func(n int) {
		t.Helper()
		_ = healthy.conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		for got := 0; got < n; {
			typ, payload, err := wire.ReadFrame(healthy.conn)
			if err != nil {
				t.Fatalf("healthy subscriber read failed after %d/%d frames of a batch (its bounded "+
					"queue must never overflow by construction, and a fan-out blocked by the wedged "+
					"peer would also stall here): %v", got, n, err)
			}
			if typ != wire.TControl {
				continue
			}
			ev, derr := DecodeControl(payload)
			if derr != nil || ev.Op != OpEvent {
				continue
			}
			if ev.Session == nil {
				t.Fatalf("healthy subscriber event carried no session view (S9/L1)")
			}
			if ev.Session.Group != status.GroupWorking {
				t.Fatalf("healthy subscriber event carried group %q; want %q (daemon-computed group must survive the fan-out)",
					ev.Session.Group, status.GroupWorking)
			}
			healthyGot++
			got++
		}
	}

	// LOCKSTEP flood/drain until the WEDGED subscriber is evicted (subs 2 -> 1). Each round pushes a
	// bounded batch of status changes into the single event source, then drains the healthy
	// subscriber fully — so the healthy subscriber never has more than `batch` events outstanding,
	// and with batch < eventQueueCap (256) its fan-out queue can NEVER overflow regardless of CPU
	// scheduling. That removes the load-sensitivity that made this test flaky under heavy load. The
	// wedged subscriber is never drained and its writer is blocked on a bounded kernel buffer, so
	// every batch accumulates monotonically in its 256-queue until it overflows and distribute
	// evicts it (delete(s.subs, sc)) — observed here at its source of truth by polling
	// len(srv.subs). Eviction is inevitable under a monotonically growing queue, so the wall-clock
	// deadline is a pure liveness net, not a throughput/rate gate, and CPU pressure cannot
	// false-fail it. This test is package protocol, so it reads srv.subs under srv.subMu directly.
	const batch = 64
	evictDeadline := time.Now().Add(30 * time.Second)
	evicted := false
	for time.Now().Before(evictDeadline) {
		for i := 0; i < batch; i++ {
			stub.pushStatus(statusMeta("sess1", status.TurnActive, status.InteractionNone))
		}
		drainHealthy(batch)
		srv.subMu.Lock()
		n := len(srv.subs)
		srv.subMu.Unlock()
		if n < 2 {
			evicted = true
			break
		}
	}
	if !evicted {
		t.Fatal("wedged fan-out subscriber not evicted within bound (S9/P-3)")
	}

	// The healthy subscriber survived the eviction and is the sole remaining subscriber. A floor of
	// >= 2 events only proves the stream reached the drain at all (survival is the guarded property,
	// not rate).
	if healthyGot < 2 {
		t.Fatalf("healthy subscriber received only %d events; want >= 2 (it must survive the wedged peer)", healthyGot)
	}
	srv.subMu.Lock()
	remaining := len(srv.subs)
	srv.subMu.Unlock()
	if remaining != 1 {
		t.Fatalf("after eviction subs has %d subscribers; want 1 (only the wedged one evicted, healthy survives)", remaining)
	}

	// Confirm the wedged subscriber's connection is actually torn down (safe/non-racy now that
	// eviction was already observed and the flood has stopped).
	if !wedged.eventuallyClosed(3 * time.Second) {
		t.Fatalf("wedged fan-out subscriber connection not closed after eviction (S9/P-3)")
	}
}

// TestFanout_EventLoopSurvivesSubscriberChurn asserts the fan-out keeps
// delivering after a subscriber disconnects mid-stream (no wedge leaks into the
// source drain).
func TestFanout_EventLoopSurvivesSubscriberChurn(t *testing.T) {
	stub := newStubDaemon()
	stub.setMetas(statusMeta("sess1", status.TurnActive, status.InteractionNone))
	sock := serveStub(t, stub)

	transient := dialClient(t, sock, []string{"subscribe"})
	if _, err := transient.Subscribe(); err != nil {
		t.Fatalf("Subscribe transient: %v", err)
	}
	survivor := dialClient(t, sock, []string{"subscribe"})
	sch, err := survivor.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe survivor: %v", err)
	}

	// The transient subscriber leaves.
	_ = transient.Close()

	stub.pushStatus(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	if _, ok := recvEvent(t, sch, oneSecond); !ok {
		t.Fatalf("survivor did not receive an event after a peer disconnected")
	}
}

// eventuallyClosed reports whether the raw connection's read side observes the
// server closing it within d.
func (r *rawConn) eventuallyClosed(d time.Duration) bool {
	_ = r.conn.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 1024)
	for {
		_, err := r.conn.Read(buf)
		if err != nil {
			// A timeout means the server did NOT close it; any other error
			// (EOF, reset) means it did.
			ne, ok := err.(interface{ Timeout() bool })
			if ok && ne.Timeout() {
				return false
			}
			return true
		}
	}
}
