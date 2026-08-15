package phonecore

// Regression tests for reviewer findings on the machine-manager slice:
//
//   - BLOCKING: SingleMachineManager's relay goroutine had no shutdown: NewSingleMachineManager
//     started it unconditionally, m.events was never closed, and relay blocked forever ranging
//     over a channel nothing closed -- every constructed-and-discarded manager leaked one
//     goroutine. Close (added alongside this test) fixes it.
//   - LOW: Close stopped the relay goroutine but never closed m.events itself, so a
//     range-style consumer of Events() blocked forever after Close returned. relay now
//     `defer close(m.events)`s on every exit path.
//   - BLOCKING: SingleMachineAdapter.Stop returned success while the manager kept relaying
//     that adapter's events onto the aggregate stream unchanged -- a fail-open Stop. Stop now
//     tells relay to drop (not forward) that adapter's events from that point on.
//   - BLOCKING (residual): the above fix only covered events not yet dequeued from the
//     adapter's channel; an event already dequeued and parked in relay's inner send (nobody
//     reading manager.Events()) still crossed the aggregate stream after Stop returned, because
//     the outer stopped() check runs only at loop re-entry. SingleMachineAdapter now exposes a
//     stopSignal() channel, closed by Stop, that relay's inner select reads as a third case so
//     a parked send is abandoned instead of delivered late.
//
// Kept in a separate file from r1_machinemanager_test.go, which is frozen as this slice's
// original TDD RED evidence (docs/verification/r1-red/machine-manager-red.txt) -- none of
// these behaviors existed when that RED was captured; their own RED evidence is the addendum
// appended to that same file.

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSingleMachineManager_CloseStopsRelayGoroutine reproduces the reviewer's leak probe
// inline: constructing and closing N managers must not leave N relay goroutines parked.
func TestSingleMachineManager_CloseStopsRelayGoroutine(t *testing.T) {
	const n = 20

	for i := 0; i < n; i++ {
		adapter := NewSingleMachineAdapter("m1", newMachineTestCore(t, "m1"), make(chan Event))
		manager := NewSingleMachineManager("My Laptop", adapter)
		if err := manager.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := manager.Close(); err != nil {
			t.Fatalf("a second Close must be idempotent, got: %v", err)
		}
	}

	// Count only relay stacks, not the process-wide total: other tests' or Core's
	// goroutines coming and going must not flip this assertion (reviewer LOW).
	relayCount := func() int {
		buf := make([]byte, 1<<20)
		stacks := string(buf[:runtime.Stack(buf, true)])
		return strings.Count(stacks, "(*SingleMachineManager).relay")
	}
	deadline := time.Now().Add(2 * time.Second)
	var after int
	for {
		runtime.GC()
		after = relayCount()
		if after == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if after > 0 {
		t.Fatalf("%d relay goroutines still parked after closing %d managers; want 0 (relay leaked)", after, n)
	}
}

// TestSingleMachineManager_CloseClosesAggregateStream is the LOW-severity fix: relay must
// close m.events on every exit path, not just m.done, or a range-style consumer of Events()
// blocks forever after Close returns.
func TestSingleMachineManager_CloseClosesAggregateStream(t *testing.T) {
	adapter := NewSingleMachineAdapter("m1", newMachineTestCore(t, "m1"), make(chan Event))
	manager := NewSingleMachineManager("My Laptop", adapter)

	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	done := make(chan struct{})
	go func() {
		for range manager.Events() {
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("ranging over Events() after Close never terminated: m.events was not closed")
	}
}

// TestSingleMachineAdapter_StopAbandonsEventParkedInRelay reproduces the reviewer's residual
// fail-open window: TestSingleMachineAdapter_StopHaltsAggregateRelay always drains the
// aggregate stream BEFORE calling Stop, so relay's outer stopped() check is all that test
// ever exercises. Here nobody reads manager.Events(), so relay dequeues the event and parks
// in its inner send -- past the outer stopped() check entirely -- before Stop is ever called.
// Stop must still make relay abandon that already-in-flight event.
func TestSingleMachineAdapter_StopAbandonsEventParkedInRelay(t *testing.T) {
	events := make(chan Event, 1)
	adapter := NewSingleMachineAdapter("m1", newMachineTestCore(t, "m1"), events)
	manager := NewSingleMachineManager("My Laptop", adapter)
	t.Cleanup(func() { _ = manager.Close() })

	events <- Event{Kind: "in-flight"}
	// Give relay time to dequeue the event and park trying to forward it: nobody is reading
	// manager.Events(), so relay is now blocked in the inner select with the event already
	// off the adapter's channel -- invisible to a stopped() check made only at loop entry.
	time.Sleep(200 * time.Millisecond)

	if err := adapter.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case got := <-manager.Events():
		t.Fatalf("event parked in relay before Stop still crossed the aggregate stream after Stop returned: %+v", got)
	case <-time.After(300 * time.Millisecond):
		// expected: Stop abandons the parked event.
	}
}

// TestSingleMachineAdapter_RedundantStartDoesNotOrphanTheStopSignal pins the Start()-reset
// hazard: relay parks in its inner send waiting on stop channel C1; a redundant Start on the
// already-running adapter must NOT retire C1, or the following Stop closes a fresh C2 nobody
// waits on and the parked event is delivered after Stop returned.
func TestSingleMachineAdapter_RedundantStartDoesNotOrphanTheStopSignal(t *testing.T) {
	events := make(chan Event, 1)
	adapter := NewSingleMachineAdapter("m1", newMachineTestCore(t, "m1"), events)
	manager := NewSingleMachineManager("My Laptop", adapter)
	t.Cleanup(func() { _ = manager.Close() })

	events <- Event{Kind: "in-flight"}
	// Let relay dequeue and park in the inner send (nobody reads manager.Events()), holding
	// the stop channel it read at send time.
	time.Sleep(200 * time.Millisecond)

	if err := adapter.Start(); err != nil { // redundant: adapter is already running
		t.Fatalf("Start: %v", err)
	}
	if err := adapter.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case got := <-manager.Events():
		t.Fatalf("parked event crossed the aggregate stream after Stop returned; the redundant Start orphaned the stop signal: %+v", got)
	case <-time.After(300 * time.Millisecond):
		// expected: the parked send still waits on the channel Stop closed.
	}
}

// TestSingleMachineAdapter_StopHaltsAggregateRelay is the fail-closed fix: once Stop returns,
// no further events from that adapter reach the manager's aggregate stream, and a later Start
// resumes forwarding.
func TestSingleMachineAdapter_StopHaltsAggregateRelay(t *testing.T) {
	events := make(chan Event, 4)
	adapter := NewSingleMachineAdapter("m1", newMachineTestCore(t, "m1"), events)
	manager := NewSingleMachineManager("My Laptop", adapter)
	t.Cleanup(func() { _ = manager.Close() })

	events <- Event{Kind: "before-stop"}
	select {
	case got := <-manager.Events():
		if got.Kind != "before-stop" {
			t.Fatalf("got Kind %q; want %q", got.Kind, "before-stop")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("event sent before Stop never reached the aggregate stream")
	}

	if err := adapter.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	events <- Event{Kind: "after-stop"}
	select {
	case got := <-manager.Events():
		t.Fatalf("event sent after Stop reached the aggregate stream unchanged: %+v", got)
	case <-time.After(200 * time.Millisecond):
		// expected: Stop means this event never arrives.
	}

	if err := adapter.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events <- Event{Kind: "after-restart"}
	select {
	case got := <-manager.Events():
		if got.Kind != "after-restart" {
			t.Fatalf("got Kind %q; want %q", got.Kind, "after-restart")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("event sent after a post-Stop Start never reached the aggregate stream")
	}
}
