package remotegw

// Slice S11 REVIEW ROUND 1 -- FAILING-FIRST (TDD RED, GG-5) test for the reply-bucket
// ordering defect S11 introduced.
//
// THE DEFECT. sealReply allocates a seq, seals, and appends, with NOTHING serialising the
// three steps. Before S11 every producer on this bucket -- confirmLease and forward -- ran
// on the SINGLE command-poll goroutine, so allocation order was append order by
// construction. lease_sever.go added a producer that runs on the per-conn watcher
// (`go m.watch(...)`, leasemanager.go), so two goroutines now interleave allocate -> append
// and a later seq can reach the relay first.
//
// WHY IT MATTERS, and why it is not theoretical. The phone has ONE MailboxReceiver for the
// sender-zero reply bucket. It refuses an out-of-order seq with crypto.ErrStaleSeq, and
// mobile/relay.go returns early on that error, so LeaseState.Apply never sees the frame.
// The collision is GUARANTEED on a supersede: LeaseManager.Begin closes the old conn (whose
// watcher seals an OpDetach) microseconds before the poll goroutine seals the new lease's
// OpLease -- one user action driving both producers at once. Losing the OpLease is a dead
// keyboard until another take_control; losing the OpDetach is precisely the defect
// lease_sever.go exists to close.
//
// THE FIX IS ALREADY IN THE TREE, one bucket over: RelaySink.sealAtSeqLocked runs the whole
// allocate -> append under s.mu, and its comment names this exact hazard ("releasing the
// lock after allocating seq would let a later seq reach the phone's single MailboxReceiver
// first, which drops the earlier one as ErrStaleSeq"). The reply bucket must do the same.
//
// No existing test covers it: the two halves are driven separately and never concurrently.
//
// This file contains NO implementation.

import (
	"context"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// s11rSeqOf reads the envelope seq of one sealed reply.
func s11rSeqOf(t *testing.T, raw []byte) uint64 {
	t.Helper()
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("parse reply envelope: %v", err)
	}
	return env.Header.Seq
}

// TestS11R_ConcurrentReplyProducersAppendInSeqOrder drives BOTH producers at once -- the
// watcher path (a lease death, through the OnSever sink) and the poll path (a take_control
// confirmation) -- and asserts that the order the envelopes reached the mailbox is the order
// their seqs were allocated in.
//
// The assertion is on APPEND ORDER, not on the set of seqs: every seq being unique is what a
// bare atomic counter already gives, and it is not the property the phone needs. The phone's
// receiver is a high-water mark, so what it requires is that seq N is appended before N+1.
func TestS11R_ConcurrentReplyProducersAppendInSeqOrder(t *testing.T) {
	const (
		severances    = 250
		confirmations = 250
	)

	key := s11Key()
	mb := &fakeMailbox{}
	lr := &s11SeverRouter{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: &fakeForwarder{}, Leases: lr,
		Key: key, EpochID: s11Epoch, ReplyTarget: "phone-routing-id",
	})

	// Both producers start together, so the allocate -> append windows genuinely overlap.
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(severances + confirmations)

	for i := 0; i < severances; i++ {
		go func() {
			defer wg.Done()
			<-start
			// The watcher path: LeaseManager.watch fires this from its own goroutine.
			lr.fire(SeveredLease{
				Session: "m1/s1", OperationID: "op-take-1", Generation: 1,
				Reason: "the lease connection to the machine closed",
			})
		}()
	}
	for i := 0; i < confirmations; i++ {
		go func() {
			defer wg.Done()
			<-start
			// The poll path: routeCommand -> confirmLease, on the command-poll goroutine.
			_ = b.confirmLease(context.Background(), protocol.RemoteCommand{
				DeviceCommandAuth: protocol.DeviceCommandAuth{
					Action: protocol.ActionTakeControl, Machine: "m1",
					Session: "m1/s1", OperationID: "op-take-2",
				},
			}, nil)
		}()
	}
	close(start)
	wg.Wait()

	mb.mu.Lock()
	replies := append([][]byte(nil), mb.replies...)
	mb.mu.Unlock()

	if len(replies) != severances+confirmations {
		t.Fatalf("appended %d replies, want %d -- a producer dropped one", len(replies), severances+confirmations)
	}

	// PREMISE: the two producers really did share one seq space, so an ordering violation is
	// possible at all. A per-producer counter would make this test vacuous.
	seen := map[uint64]bool{}
	prev := uint64(0)
	inversions := 0
	for i, raw := range replies {
		seq := s11rSeqOf(t, raw)
		if seen[seq] {
			t.Fatalf("reply %d re-used seq %d; the phone stale-drops the duplicate and one lease event is invisible", i, seq)
		}
		seen[seq] = true
		if seq < prev {
			inversions++
		}
		prev = seq
	}
	if inversions > 0 {
		t.Fatalf("%d of %d appends arrived out of seq order. The phone's reply-bucket receiver "+
			"refuses every one of them with crypto.ErrStaleSeq and mobile/relay.go returns early, "+
			"so LeaseState.Apply never sees them: on a supersede that is either the new OpLease "+
			"(a dead keyboard) or the OpDetach lease_sever.go exists to deliver. Serialise "+
			"allocate -> append the way RelaySink.sealAtSeqLocked does",
			inversions, len(replies))
	}
}
