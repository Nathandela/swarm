package transport_test

// THE PACING DrainPacer AND AckBatcher PRODUCE -- the gap ADR-007 B98 named and left open.
//
// Both types are LIVE: internal/remotegw/command_loop.go:309,317 constructs them on the
// gateway's inbound hop. Until now nothing in the tree constructed either and asserted what it
// does. drainbudget_test.go pins the CONSTANTS they are built from; this pins the BEHAVIOUR,
// which is the half that can regress without any number changing.
//
// WHY THE ASSERTIONS ARE UPPER BOUNDS. §6.0 binds a ceiling ("<= 3 reads/s", "<= 1 ack/s"), not
// a cadence, and the pacer is deliberately adaptive -- it leaves the batching regime when
// spacing stops being productive. A test that pinned exact timings would fail on a loaded host
// and would forbid the adaptation the design is about. Every assertion here is therefore a
// bound with slack, in the direction the requirement cares about: too FAST is a defect, too
// slow is a scheduler.
//
// WHAT IS NOT COVERED. The regime machine's internal state is not asserted directly -- only its
// observable consequence, that sustained reads stay under the ceiling. Whether leaving the
// batching regime is the RIGHT adaptation is a design question §6.0 settles, not this file.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/transport"
)

// TestDrainPacer_SustainedReadsStayUnderTheCeiling is the property the relay quota depends on:
// however the regime flaps, the average over a run never exceeds §6.0's ceiling.
func TestDrainPacer_SustainedReadsStayUnderTheCeiling(t *testing.T) {
	p := transport.NewDrainPacer()
	ctx := context.Background()

	const reads = 12
	start := time.Now()
	for i := 0; i < reads; i++ {
		if err := p.Pace(ctx); err != nil {
			t.Fatalf("Pace: %v", err)
		}
		p.Observe(2) // a real backlog keeps the ceiling binding
	}
	elapsed := time.Since(start)

	// The first read is admitted immediately (a full window of tokens), so the ceiling governs
	// the reads AFTER it.
	rate := float64(reads-1) / elapsed.Seconds()
	if rate > float64(transport.MaxDrainReadsPerSec)*1.25 {
		t.Errorf("DrainPacer admitted %d reads in %v (%.2f/s); §6.0 ceilings the sustained rate at "+
			"%d/s. mailbox_read meters against the relay's OpsPerMin window shared with the live "+
			"tail, so a pacer that overruns spends the budget of the traffic it exists to receive",
			reads, elapsed, rate, transport.MaxDrainReadsPerSec)
	}
}

// TestDrainPacer_PaceHonoursCancellation. The pacer sleeps on the gateway's goroutine; one that
// ignored ctx would outlive the Service it belongs to and keep reading after teardown.
func TestDrainPacer_PaceHonoursCancellation(t *testing.T) {
	p := transport.NewDrainPacer()
	ctx := context.Background()
	// Spend the first, free read so the next one has to wait.
	if err := p.Pace(ctx); err != nil {
		t.Fatalf("Pace: %v", err)
	}
	p.Observe(2)

	cctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Pace(cctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Pace returned nil after its context was cancelled; a cancelled drain must not " +
				"admit the read it was waiting to admit")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Pace did not return within 2s of cancellation; the gateway's drain goroutine " +
			"cannot be torn down")
	}
}

// TestAckBatcher_CoalescesToTheHighestCursor is the quota half AND the latency half at once:
// many consumed items must produce at most one ack per tick, carrying the highest coordinate.
func TestAckBatcher_CoalescesToTheHighestCursor(t *testing.T) {
	var mu sync.Mutex
	var got []uint64
	b := transport.NewAckBatcher(func(_ context.Context, cursor uint64) error {
		mu.Lock()
		got = append(got, cursor)
		mu.Unlock()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	// A page of twenty items delivered inside one tick.
	for i := uint64(1); i <= 20; i++ {
		b.Record(i)
	}
	time.Sleep(1500 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("AckBatcher never acked; an ack that never happens leaves the relay holding every " +
			"item forever, which is the depth cap CR-4 refuses appends at")
	}
	if len(got) > 2 {
		t.Errorf("AckBatcher issued %d acks (%v) for twenty items inside ~1.5 ticks; §6.0 ceilings "+
			"acks at %d/s precisely because each one is a synchronous relay fsync",
			len(got), got, transport.MaxDrainAcksPerSec)
	}
	if got[0] != 20 {
		t.Errorf("the first ack carried cursor %d, want 20: an ack is monotonic and releases "+
			"everything up to it, so batching must ack the HIGHEST consumed coordinate, not the "+
			"first or the last recorded", got[0])
	}
}

// TestAckBatcher_RecordDoesNoIOOnTheDeliveryPath. Record runs between delivering an item and
// re-parking the wait; §6.0's 2026-07-25 amendment makes ack PLACEMENT a latency requirement,
// because one inline ack (p50 30.8 ms, max 129.2 ms) can eat most of the 150 ms input budget.
func TestAckBatcher_RecordDoesNoIOOnTheDeliveryPath(t *testing.T) {
	var calls int
	var mu sync.Mutex
	b := transport.NewAckBatcher(func(_ context.Context, _ uint64) error {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(200 * time.Millisecond) // a relay fsync
		return nil
	})

	start := time.Now()
	for i := uint64(1); i <= 100; i++ {
		b.Record(i)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("100 Record calls took %v; Record sits on the delivery path and must never block "+
			"or perform I/O", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("Record invoked the ack callback %d times with no Run goroutine; the ack must "+
			"happen OFF the delivery path, on the batcher's own tick", calls)
	}
}

// TestAckBatcher_AFailedAckIsRetriedNotLost. A dropped ack is safe for correctness (both hops
// advance a durable cursor first) but not for the relay's depth cap, so the coordinate has to
// survive a failure.
func TestAckBatcher_AFailedAckIsRetriedNotLost(t *testing.T) {
	var mu sync.Mutex
	var attempts []uint64
	fail := true
	b := transport.NewAckBatcher(func(_ context.Context, cursor uint64) error {
		mu.Lock()
		attempts = append(attempts, cursor)
		first := fail
		fail = false
		mu.Unlock()
		if first {
			return errors.New("relay refused the ack")
		}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	b.Record(7)

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(attempts)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(attempts) < 2 {
		t.Fatalf("a refused ack was attempted %d time(s) (%v); the coordinate must be kept for the "+
			"next tick, or a single transient refusal leaves the relay holding the mailbox until "+
			"the depth cap refuses appends", len(attempts), attempts)
	}
	if attempts[1] != 7 {
		t.Errorf("the retry acked cursor %d, want 7 -- the retained coordinate must be the one that "+
			"failed", attempts[1])
	}
}
