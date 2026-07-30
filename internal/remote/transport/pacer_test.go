package transport_test

// THE RATE DrainPacer PRODUCES -- part of the gap ADR-007 B98 named and left open.
//
// DrainPacer is LIVE: internal/remotegw/command_loop.go:317 constructs it on the gateway's
// inbound hop. Nothing constructed it and asserted what it does. drainbudget_test.go pins the
// CONSTANTS it is built from; this pins the RATE it produces, which is the half that can
// regress without any number changing.
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
