package pairing

// ADR-007 B69(2) -- THE PAIRING DEADLINE CAN CANCEL THE MACHINE'S COMMIT AFTER THE PHONE
// HAS ALREADY BEEN TOLD "YES".
//
// B64 put the whole machine handshake under ONE deadline (internal/skeleton/pairing.go:244,
// context.WithTimeout(ctx, window)). On the accept path Machine.Pair sends the acceptance and
// then burns the rendezvous ON THAT SAME CONTEXT -- sendDecision does rt.Send followed by
// rt.Complete, both with the caller's ctx, and treats a Complete failure as the decision's
// failure (pairing.go:560).
//
// So if the deadline expires AFTER Send has forwarded the acceptance and BEFORE Complete
// returns:
//
//   1. the phone receives acceptance, pins this machine, and returns a DeviceOutcome;
//   2. sendDecision reports failure, because Complete failed;
//   3. Pair returns no MachineOutcome, so the daemon's commit never runs and it enrolls
//      nothing.
//
// PHONE PINNED, MACHINE NOT ENROLLED -- the half-pair PB-PAIR-4 forbids. B60(1) reached that
// state only with an injected filesystem error. B64 supplied an ORDINARY CLOCK: pairing near
// its own advertised expiry is now enough.
//
// THE DEADLINE HERE IS THE HAZARD, NOT A SAFETY PROPERTY THE TEST SUPPLIES. That distinction
// is what made b52_consent_release_test.go:223 vacuous -- it handed Pair a 2 s deadline
// production never gave it and took its green from it. This test hands Pair the deadline
// production DOES give it (skeleton builds exactly this shape) and asserts the machine
// commits anyway once the acceptance has left.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// acceptStallWindow is the pairing deadline handed to Machine.Pair below, in the shape
// internal/skeleton/pairing.go:244 builds it. Short because the stalled acceptance send
// waits it out; the handshake ahead of that send is in-memory Noise and takes milliseconds.
const acceptStallWindow = 2 * time.Second

// legBudget is how long these tests WATCH a leg before calling it hung. An observation
// bound on the assertion; never handed to production.
const legBudget = 20 * time.Second

// machineAcceptanceSend is the ordinal of the acceptance frame among the MACHINE's own
// sends. Machine.Pair writes exactly two: msg2 (pairing.go:376) and the decision
// (pairing.go:489, via sendDecision). The second one is the acceptance.
const machineAcceptanceSend = 2

// stalledAcceptRendezvous is the MACHINE end of a rendezvous, ctx-faithful the way
// relay.Conn is: every op hands the caller's ctx to a websocket roundtrip
// (relay/client.go control -> roundtrip -> ws), so an expired ctx fails the op outright.
// The bare fakeRendezvous ignores ctx in Complete and races it in Send, which is exactly
// how a real Complete failure disappears from a harness.
//
// Its one scripted behaviour is on the acceptance frame: the frame is FORWARDED to the
// phone, and only then does the call wait out the pairing deadline before returning nil.
// That is an ordinary slow relay write whose bytes landed -- the single ordering that
// produces the half-pair, and the one a hand-made Complete error cannot construct because
// it says nothing about whether the phone was told "yes" first.
type stalledAcceptRendezvous struct {
	*fakeRendezvous

	mu        sync.Mutex
	sends     int
	forwarded bool
}

var _ RendezvousTransport = (*stalledAcceptRendezvous)(nil)

func (s *stalledAcceptRendezvous) Send(ctx context.Context, msg []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.sends++
	n := s.sends
	s.mu.Unlock()

	if err := s.fakeRendezvous.Send(ctx, msg); err != nil {
		return err
	}
	if n == machineAcceptanceSend {
		s.mu.Lock()
		s.forwarded = true
		s.mu.Unlock()
		<-ctx.Done() // the bytes are with the phone; the clock then runs out mid-call
	}
	return nil
}

func (s *stalledAcceptRendezvous) Complete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.fakeRendezvous.Complete(ctx, id)
}

func (s *stalledAcceptRendezvous) acceptanceForwarded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forwarded
}

// TestB69_TheDeadlineCannotCancelTheCommitOnceAcceptanceIsOnTheWire is PB-PAIR-4's symmetry
// at the only instant it can be broken by a clock: the phone holds the acceptance and WILL
// pin, so from that instant the machine's commit is no longer optional and no longer the
// pairing deadline's to cancel.
func TestB69_TheDeadlineCannotCancelTheCommitOnceAcceptanceIsOnTheWire(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x69), fill16(0x69)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)

	matched := make(chan struct{})
	close(matched) // this operator compared the codes and they agree
	dp := newDeviceParams(dID, secret, rid)
	dp.DeviceSAS = shippedDeviceSAS(make(chan struct{}, 1), matched)

	mEndRaw, dEnd := newRendezvousPipe()
	mEnd := &stalledAcceptRendezvous{fakeRendezvous: mEndRaw}

	// The DAEMON's window, in production's own shape.
	pairCtx, cancelPair := context.WithTimeout(context.Background(), acceptStallWindow)
	defer cancelPair()

	type mres struct {
		out *MachineOutcome
		err error
	}
	mDone := make(chan mres, 1)
	go func() {
		out, err := NewMachine(mp).Pair(pairCtx, mEnd)
		mDone <- mres{out, err}
	}()

	// The phone runs on ITS OWN context, as it does in production: the machine's pairing
	// window is not a clock any handset can see.
	type dres struct {
		out *DeviceOutcome
		err error
	}
	dDone := make(chan dres, 1)
	go func() {
		out, err := RunDevice(context.Background(), dp, dEnd)
		dDone <- dres{out, err}
	}()

	var phone dres
	select {
	case phone = <-dDone:
	case <-time.After(legBudget):
		t.Fatal("the phone leg never resolved; it should have received acceptance immediately")
	}
	if phone.err != nil || phone.out == nil {
		t.Fatalf("the phone did not pin (outcome=%v err=%v). This test is not measuring what it "+
			"claims: it needs a handset that HAS been told yes, which is what makes the machine's "+
			"commit non-optional", phone.out, phone.err)
	}
	if !mEnd.acceptanceForwarded() {
		t.Fatal("the acceptance frame was never forwarded, so the ordering under test never happened")
	}

	select {
	case r := <-mDone:
		if r.err != nil || r.out == nil {
			t.Fatalf("HALF-PAIR (PB-PAIR-4): the phone pinned this machine and the machine returned "+
				"no outcome (err=%v).\n"+
				"  The pairing deadline expired AFTER rt.Send forwarded acceptance and BEFORE rt.Complete\n"+
				"  returned, so sendDecision (pairing.go:560) reported failure and the daemon enrolls\n"+
				"  nothing while the handset is already pinned. Once acceptance is on the wire the phone\n"+
				"  WILL pin, so the commit must no longer be cancellable by that deadline.", r.err)
		}
	case <-time.After(legBudget):
		t.Fatal("Machine.Pair never returned")
	}

	// The commit must actually RUN detached -- not have its failure ignored. An unburned
	// rendezvous id is re-creatable once its TTL lapses (ADR-007 B47b), so swallowing the
	// Complete error would trade one defect for another and still pass the assertion above.
	if got := mEndRaw.completedIDs(); len(got) == 0 {
		t.Error("the machine returned an outcome without burning the rendezvous: the detached commit " +
			"must complete the rendezvous, not merely ignore the failure to")
	}
}
