package remotegw_test

// §6.0's inbound drain budget -- the ACK PLACEMENT and ACK RATE halves, owned by PB-NET-5(c)
// and PB-GW-7 -- asserted as BEHAVIOUR rather than as constants (ADR-007 B101's sibling shape:
// a live object with no fence at all).
//
// The requirement ids are named because section 6.0's budget table now cites this file as
// their fence, and internal/verify/phaseb_budget_test.go requires a cited fence to name the
// requirement it is cited for -- a citation that names nothing can point anywhere.
//
// WHAT WAS ALREADY COVERED AND WHY IT IS NOT THIS. transport/drainbudget_test.go pins
// MaxDrainReadsPerSec and MaxDrainAcksPerSec and the arithmetic against the relay's OpsPerMin
// window. Those are the NUMBERS the batcher is built from. Nothing asserted what the batcher
// DOES with them -- and the numbers being fenced is what made this easy to miss, because the
// file reads as coverage of the pacer and is coverage of its inputs.
//
// WHY THE FENCE LIVES HERE. internal/remotegw is the only production constructor of either
// type (command_loop.go:309,317). A fence placed with the consumer follows the requirement's
// live hop if the implementation is moved or renamed again, which this package's own history
// says is the likelier event; placed beside the implementation it would have to be rescued a
// third time. The SUBJECT is still AckBatcher -- this is not re-pointing the assertion at a
// more convenient object.
//
// EACH TEST BELOW IS ONE BEHAVIOUR THE PRODUCTION HOP DEPENDS ON, and none of them asserts a
// duration: they assert WHICH cursor was acked, HOW MANY acks happened, and WHETHER one
// happened at all. A loaded host makes a tick late, not wrong, so these do not degrade into
// the timing gates this suite already cannot keep green.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/transport"
)

// ackTick is the batcher's own flush period (MaxDrainAcksPerSec = 1). Tests wait for the
// code's timer rather than inventing one, and always with slack: every bound below is
// "eventually within N ticks", never "at tick N".
const ackTick = time.Second / transport.MaxDrainAcksPerSec

// recordingAck captures every ack the batcher performs, in order.
type recordingAck struct {
	mu      sync.Mutex
	cursors []uint64
	// failFirst makes the first ack fail, so the retry path is exercised.
	failFirst bool
	failed    bool
}

func (r *recordingAck) fn(_ context.Context, cursor uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cursors = append(r.cursors, cursor)
	if r.failFirst && !r.failed {
		r.failed = true
		return errors.New("relay ack refused")
	}
	return nil
}

func (r *recordingAck) all() []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint64(nil), r.cursors...)
}

func (r *recordingAck) count() int { return len(r.all()) }

// runBatcher starts one batcher's flush loop and joins it at test end.
func runBatcher(t *testing.T, rec *recordingAck) *transport.AckBatcher {
	t.Helper()
	b := transport.NewAckBatcher(rec.fn)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("AckBatcher.Run did not return when its context was cancelled; the drain " +
				"owns and JOINS this goroutine, so one that outlives its context is retained per " +
				"reconnect")
		}
	})
	return b
}

// awaitAcks waits until at least n acks have happened, or fails.
func awaitAcks(t *testing.T, rec *recordingAck, n int, why string) {
	t.Helper()
	deadline := time.Now().Add(10 * ackTick)
	for time.Now().Before(deadline) {
		if rec.count() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("only %d ack(s) after %v, want at least %d: %s", rec.count(), 10*ackTick, n, why)
}

// TestAckBatcher_CoalescesToTheHighestCursorAndNeverAcksOnTheDeliveryPath is the batcher's
// reason for existing, in one test because the two halves are one property: the delivery path
// records and returns, and the ONE ack that eventually happens carries the whole batch.
//
// A relay ack is a synchronous bolt fsync (p50 30.8 ms, max 129.2 ms measured), so an ack
// taken between delivering an item and re-parking the wait spends most of §6.0's 150 ms p50
// input budget on the keystroke path.
//
// THE HIGHEST, NOT THE LATEST, and the fixture makes those differ deliberately: the last
// recorded cursor is lower than the highest. A batcher that simply kept the most recent value
// would ack a cursor it has already passed, leaving the relay holding items both hops have
// durably consumed -- and it would pass a test whose records only ever increased.
func TestAckBatcher_CoalescesToTheHighestCursorAndNeverAcksOnTheDeliveryPath(t *testing.T) {
	rec := &recordingAck{}
	b := runBatcher(t, rec)

	const highest = 50
	for c := uint64(1); c <= highest; c++ {
		b.Record(c)
	}
	// Out of order, and LOWER: monotonic means the batch keeps 50.
	b.Record(7)

	// THE DELIVERY PATH DID NO I/O. This is checked immediately, inside the first tick, so it
	// is a statement about Record and not a race with the flush loop.
	if n := rec.count(); n != 0 {
		t.Fatalf("Record performed %d ack(s) synchronously; acks must ride the batcher OFF the "+
			"delivery path, or each one puts a relay fsync between an item's arrival and the "+
			"next wait", n)
	}

	awaitAcks(t, rec, 1, "the recorded batch must eventually be acked")
	got := rec.all()
	if got[0] != highest {
		t.Errorf("first ack carried cursor %d, want %d (the HIGHEST recorded, not the latest). "+
			"%d records produced this one ack, which is the batching the 1/s ceiling requires.",
			got[0], highest, highest+1)
	}
}

// TestAckBatcher_AnIdleDrainAcksNothing is the half that keeps the batcher inside the relay's
// budget when there is nothing to say.
//
// mailbox_ack meters against the relay's OpsPerMin window. A flush loop that acked on every
// tick regardless would spend one op per second per routing id forever -- 60/min of a 600/min
// window bought with no items consumed, on an idle phone in someone's pocket.
func TestAckBatcher_AnIdleDrainAcksNothing(t *testing.T) {
	rec := &recordingAck{}
	b := runBatcher(t, rec)

	b.Record(9)
	awaitAcks(t, rec, 1, "the recorded cursor must be acked")
	if got := rec.all()[0]; got != 9 {
		t.Fatalf("first ack carried %d, want 9", got)
	}

	// Nothing further is recorded. Several ticks must now pass with no ack at all.
	settled := rec.count()
	time.Sleep(3 * ackTick)
	if n := rec.count(); n != settled {
		t.Errorf("an idle batcher performed %d further ack(s) over %v; a tick with nothing "+
			"recorded must ack NOTHING, or an idle hop spends the relay's op budget to say so",
			n-settled, 3*ackTick)
	}
}

// TestAckBatcher_ARefusedAckIsRetriedRatherThanLost is the drop-safety clause, and it is the
// one behaviour here whose absence is invisible in production.
//
// Losing an ack is SAFE for correctness -- both hops advance a durable cursor before recording
// one, so an un-acked item is never re-delivered -- which is exactly why a lost ack is silent:
// nothing breaks, the relay simply keeps a copy of everything until retention expires. The
// coordinate is therefore kept and re-offered on the next tick, and re-acking is free because
// acks are monotonic and idempotent.
func TestAckBatcher_ARefusedAckIsRetriedRatherThanLost(t *testing.T) {
	rec := &recordingAck{failFirst: true}
	b := runBatcher(t, rec)

	b.Record(42)
	awaitAcks(t, rec, 2, "a refused ack must be re-offered on a later tick, not dropped")

	got := rec.all()
	if got[0] != 42 {
		t.Fatalf("first (refused) ack carried %d, want 42", got[0])
	}
	if got[1] < 42 {
		t.Errorf("the retry carried cursor %d, want >= 42: a refused ack must keep its "+
			"coordinate, or the relay holds items both hops consumed until retention expires -- "+
			"with nothing failing and nothing to notice", got[1])
	}
}
