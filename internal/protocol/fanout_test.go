package protocol

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
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

// TestFanout_WedgedSubscriberDisconnectedWithinBound asserts S9: a subscriber
// that stops reading is disconnected within a bound, and a healthy subscriber
// keeps receiving events — the wedged one never blocks the event loop.
func TestFanout_WedgedSubscriberDisconnectedWithinBound(t *testing.T) {
	stub := newStubDaemon()
	stub.setMetas(statusMeta("sess1", status.TurnActive, status.InteractionNone))
	sock := serveStub(t, stub)

	// Wedged subscriber: subscribes but never reads its socket. We reach past the
	// Client API to a raw conn so nothing drains it.
	wedged := rawDial(t, sock)
	wep := wedged.hello(Version, []string{"subscribe"})
	wedged.writeControl(Control{Op: OpSubscribe, EndpointID: wep.EndpointID})
	// Deliberately never read from `wedged` again.

	// Healthy subscriber via the Client API, drained continuously so it never
	// becomes wedged itself and we can prove it keeps receiving.
	healthy := dialClient(t, sock, []string{"subscribe"})
	ch, err := healthy.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe healthy: %v", err)
	}
	var healthyGot int32
	stopDrain := make(chan struct{})
	go func() {
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					return
				}
				atomic.AddInt32(&healthyGot, 1)
			case <-stopDrain:
				return
			}
		}
	}()

	// Flood status changes well past any reasonable per-client queue bound. The
	// push side must never block (the daemon's event loop stays live).
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ {
			stub.pushStatus(statusMeta("sess1", status.TurnActive, status.InteractionNone))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(oneSecond):
		t.Fatalf("publishing blocked while a subscriber was wedged — violates S9 (event loop must not block)")
	}

	// The healthy subscriber received events throughout the flood.
	deadline := time.Now().Add(oneSecond)
	for time.Now().Before(deadline) && atomic.LoadInt32(&healthyGot) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	close(stopDrain)
	if atomic.LoadInt32(&healthyGot) == 0 {
		t.Fatalf("healthy subscriber starved by a wedged peer — violates S9")
	}

	// The wedged connection is disconnected within a bound: its socket read
	// eventually returns an error (server closed it).
	if !wedged.eventuallyClosed(2 * time.Second) {
		t.Fatalf("wedged subscriber not disconnected within bound — violates S9/P-3")
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
