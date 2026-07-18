package journal

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// R-JRN.3 (amended D.0-A7): flap debounce lives at the DELIVERY layer, NEVER in the
// durable journal. The durable journal records every distinct net transition; a
// mock-clock debouncer on the push/delivery layer collapses a flap that settles
// within its window and never delays a terminal lifecycle record.
//
// FROZEN API these tests expect (the delivery-layer seam; its ultimate home may be
// the gateway's coalescer, R-GW.4 — see the test-writer report):
//
//	type Debouncer struct{ ... }
//	func NewDebouncer(window time.Duration, clock func() time.Time) *Debouncer
//	func (d *Debouncer) Offer(r Record)              // enqueue at clock-now
//	func (d *Debouncer) Drain(now time.Time) []Record // records DUE for delivery as of now

// TestJournal_DebounceCollapsesFlap asserts BOTH halves of A7:
//  1. the durable journal records every net transition with NO collapsing;
//  2. a delivery-layer Debouncer collapses a within-window flap to the settled net
//     state and never delays a terminal lifecycle record.
func TestJournal_DebounceCollapsesFlap(t *testing.T) {
	// --- durable side: the journal records EVERY distinct net transition ---
	j := openJournal(t, jdir(t))
	flaps := []status.Group{
		status.GroupWorking,
		status.GroupNeedsInput,
		status.GroupWorking,
		status.GroupNeedsInput,
	}
	for _, g := range flaps {
		mustAppend(t, j, Record{SessionID: "s", Type: TypeGroupTransition, Group: g})
	}
	res, err := j.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if len(res.Events) != len(flaps) {
		t.Fatalf("durable journal collapsed flaps: %d records for %d transitions; the durable log must record every net transition", len(res.Events), len(flaps))
	}

	// --- delivery side: a mock-clock debouncer collapses the within-window flap ---
	base := time.Unix(1700000000, 0).UTC()
	now := base
	clock := func() time.Time { return now }
	const window = time.Second
	deb := NewDebouncer(window, clock)

	// A session flaps needs_input -> working -> needs_input, all inside the window.
	now = base
	deb.Offer(Record{SessionID: "s", Type: TypeGroupTransition, Group: status.GroupNeedsInput})
	now = base.Add(100 * time.Millisecond)
	deb.Offer(Record{SessionID: "s", Type: TypeGroupTransition, Group: status.GroupWorking})
	now = base.Add(200 * time.Millisecond)
	deb.Offer(Record{SessionID: "s", Type: TypeGroupTransition, Group: status.GroupNeedsInput})

	// Before the window elapses nothing is due.
	if due := deb.Drain(base.Add(300 * time.Millisecond)); len(due) != 0 {
		t.Fatalf("debouncer delivered %d records before the window elapsed; want 0", len(due))
	}
	// After the window: the flap collapses to the single settled net state.
	due := deb.Drain(base.Add(window + time.Millisecond))
	if len(due) != 1 {
		t.Fatalf("debouncer delivered %d records for a settled flap; want 1 net transition", len(due))
	}
	if due[0].Group != status.GroupNeedsInput {
		t.Fatalf("settled net group = %q; want %q (the last state within the window)", due[0].Group, status.GroupNeedsInput)
	}

	// A terminal lifecycle record is NEVER delayed: it is due immediately, even
	// inside a fresh window.
	now = base.Add(2 * time.Second)
	deb.Offer(Record{SessionID: "s", Type: TypeExited})
	immediate := deb.Drain(now)
	sawExit := false
	for _, r := range immediate {
		if r.Type == TypeExited {
			sawExit = true
		}
	}
	if !sawExit {
		t.Fatalf("terminal 'exited' record was delayed by the debouncer; a terminal lifecycle record must never be debounced")
	}
}
