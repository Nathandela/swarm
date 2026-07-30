package pairing

// PB-PAIR-4 / PB-SAS-4 -- THE FINAL FRAME OF THE CEREMONY IS UNACKNOWLEDGED BY CONSTRUCTION,
// SO THE TWO LEGS CAN END THE SAME CEREMONY DISAGREEING.
//
// msg4's answer -- the machine's accept/decline decision -- is the last frame either side
// sends. Nothing acknowledges it, and it rides outside the SAS transcript, so the operators'
// emoji comparison attests nothing about whether the two sides agreed. That is why a
// disagreement is invisible to both people looking straight at it (ADR-007 B86(2)).
//
// THE PROPERTY THIS FILE FENCES, AND WHY IT IS A DISJUNCTION.
//
// Perfect agreement is unobtainable: this is the two-generals problem, and a fifth frame only
// moves which side holds the residual uncertainty. So the property cannot be "the two legs
// always agree". It is:
//
//	Where NOTHING was lost -- every protocol frame the two legs exchanged was delivered
//	intact in both directions -- the two legs MUST agree on the ceremony's outcome. Residual
//	uncertainty is what two generals costs; disagreement with nothing lost is a defect.
//
// The case in this file is that one: the acceptance reaches the phone, the phone pins, and the
// only thing that misbehaves afterwards is the machine's own bookkeeping -- the rendezvous burn
// on a loaded or distant relay (ADR-007 B82(1)). No attacker, no injected error, and no clock
// this test hands to production.
//
// WHY THE ASSERTION IS ON Machine.Pair's OUTCOME AND NOT ON ENROLMENT. `Machine.Pair` returning
// a MachineOutcome is not device authority; internal/skeleton's commit is. A remedy that DEFERS
// enrolment (the machine records the outcome and mints authority only once the phone is seen)
// is a plausible fix and must stay possible, so nothing here says anything about what the
// daemon does with the outcome -- only that the machine must not throw its own outcome away and
// orphan a phone it has already told "yes". The daemon-side half of the property, including the
// orientation whose recovery needs a desktop revoke, lives in
// internal/skeleton/pbpair4_lockout_test.go.
//
// SCOPE: nothing here touches internal/remote/crypto (FROZEN) and no existing test is modified.
// The only contact with an existing file is REUSE of b64_shipped_abort_test.go's
// shippedDeviceSAS, which is the shape mobile/pairing.go installs.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// burnPatience is how long these tests WATCH a leg before calling it unresolved. It is an
// observation bound on the assertion and is NEVER handed to production: every Machine.Pair
// below runs on context.Background(), the shape internal/protocol/server.go builds. Injecting a
// deadline into Pair is what made b52_consent_release_test.go:223 vacuous.
//
// It is deliberately far wider than acceptCommitWindow so that "widen the window" cannot pass
// by arriving late: a machine that waits out a burn it cannot finish still fails, which is
// right, because an unbounded park is the defect B64 was raised to fix.
const burnPatience = 20 * time.Second

// pbPair4Identities mints the two ends' identities plus a distinct secret/rendezvous pair, so
// each test in this file is independent of every other.
func pbPair4Identities(t *testing.T, tag byte) (machine, device *crypto.Identity, secret [32]byte, rid [16]byte) {
	t.Helper()
	mID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	dID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("device identity: %v", err)
	}
	return mID, dID, fill32(tag), fill16(tag)
}

// burnRendezvous is the MACHINE end of a rendezvous on a relay that may be loaded or distant.
//
// It is ctx-faithful the way relay.Conn is -- every op hands the caller's ctx to a websocket
// roundtrip, so an expired ctx fails the op outright -- and its ONE scripted behaviour is the
// rendezvous burn: when stalled, Complete does not return until either the test's cleanup
// releases it or the ctx it was given dies. Every DATA frame, in both directions, is forwarded
// verbatim and promptly.
//
// That is the whole point. Nothing the two legs say to each other is lost, corrupted or
// delayed. The only slow call is the machine talking to the relay about its own housekeeping,
// which the phone cannot see and has no stake in.
type burnRendezvous struct {
	*fakeRendezvous

	stalled   bool
	release   chan struct{} // closed by cleanup, so a blocked Complete can always exit
	burnOnce  sync.Once
	burnBegan chan struct{} // closed the first time Complete is entered
}

var _ RendezvousTransport = (*burnRendezvous)(nil)

func newBurnRendezvous(t *testing.T, inner *fakeRendezvous, stalled bool) *burnRendezvous {
	t.Helper()
	b := &burnRendezvous{
		fakeRendezvous: inner,
		stalled:        stalled,
		release:        make(chan struct{}),
		burnBegan:      make(chan struct{}),
	}
	t.Cleanup(func() { close(b.release) })
	return b
}

func (b *burnRendezvous) Send(ctx context.Context, msg []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.fakeRendezvous.Send(ctx, msg)
}

func (b *burnRendezvous) Complete(ctx context.Context, id string) error {
	b.burnOnce.Do(func() { close(b.burnBegan) })
	if !b.stalled {
		return b.fakeRendezvous.Complete(ctx, id)
	}
	select {
	case <-b.release:
		return b.fakeRendezvous.Complete(ctx, id)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *burnRendezvous) burnAttempted() bool {
	select {
	case <-b.burnBegan:
		return true
	default:
		return false
	}
}

// TestPBPAIR4_NothingLostMeansTheTwoLegsAgree is orientation A of the half-pair, reached from
// an ordinary clock with no attacker: the phone has been told "yes", pins, and returns a
// DeviceOutcome, while the machine throws its own side of the same ceremony away because the
// rendezvous BURN -- a housekeeping call to the relay, made after the decision has already
// left -- outran an internal window.
//
// ADR-007 B82(1) states this as the residual the B69(2) fix created: pre-fix the accept-path
// Complete ran on the pairing context with tens of seconds left; post-fix it runs on a flat two
// seconds, and "on a loaded or distant relay, Complete exceeding 2s manufactures the very
// half-pair the detach prevents".
//
// EVERY PROTOCOL FRAME IS DELIVERED, INTACT, IN BOTH DIRECTIONS. There is no residual
// uncertainty for either side to be caught by: the machine's Send returned success, so it KNOWS
// the phone was told. A disagreement here is not two generals, it is a machine discarding an
// answer it has already given.
func TestPBPAIR4_NothingLostMeansTheTwoLegsAgree(t *testing.T) {
	mID, dID, secret, rid := pbPair4Identities(t, 0x74)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)

	matched := make(chan struct{})
	close(matched) // both operators compared the codes and they agree
	dp := newDeviceParams(dID, secret, rid)
	dp.DeviceSAS = shippedDeviceSAS(make(chan struct{}, 1), matched)

	mEndRaw, dEnd := newRendezvousPipe()
	mEnd := newBurnRendezvous(t, mEndRaw, true)

	type mres struct {
		out *MachineOutcome
		err error
	}
	mDone := make(chan mres, 1)
	go func() {
		// context.Background(): this test hands production no deadline at all. The two-second
		// bound that bites here is acceptCommitWindow, which lives INSIDE Machine.Pair --
		// production's own value.
		out, err := NewMachine(mp).Pair(context.Background(), mEnd)
		mDone <- mres{out, err}
	}()

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
	case <-time.After(burnPatience):
		t.Fatal("the phone leg never resolved; it should have received the acceptance immediately, " +
			"because this transport delays only the machine's rendezvous burn")
	}

	var machine mres
	select {
	case machine = <-mDone:
	case <-time.After(burnPatience):
		t.Fatalf("Machine.Pair never returned within %s while the rendezvous burn was stalled "+
			"(burn attempted = %v). A machine that parks unbounded on its own housekeeping is "+
			"ADR-007 B64's defect wearing a different hat: the burn must be bounded AND its "+
			"failure must not be the pairing's failure.", burnPatience, mEnd.burnAttempted())
	}

	if !mEnd.burnAttempted() {
		t.Fatal("the machine never attempted the rendezvous burn, so the ordering under test " +
			"never happened; this test is not measuring what it claims")
	}

	// THE PROPERTY. Nothing was lost in either direction, so the two legs must not end this
	// ceremony holding different answers. Both-hold and neither-holds are equally acceptable:
	// a machine that burns before it accepts would fail this ceremony on both legs, which is a
	// retry, not a half-pair. The controls below are what forbid resolving it that way always.
	phonePinned := phone.out != nil && phone.err == nil
	machineHeld := machine.out != nil && machine.err == nil
	if phonePinned == machineHeld {
		return
	}

	if phonePinned {
		t.Fatalf("HALF-PAIR (PB-PAIR-4): the phone pinned this machine and the machine returned "+
			"no outcome (err=%v).\n"+
			"  Every frame between the two legs was delivered intact; the only slow call was the\n"+
			"  machine's own rendezvous burn, AFTER rt.Send had already forwarded the acceptance.\n"+
			"  sendDecision folds that burn's failure into the decision's failure (pairing.go:613),\n"+
			"  so a loaded or distant relay orphans a phone the machine has already told yes\n"+
			"  (ADR-007 B82(1)). A burn that cannot finish is housekeeping, not consent.", machine.err)
	}
	t.Fatalf("HALF-PAIR (PB-PAIR-4), inverted: the machine holds the ceremony and the phone does "+
		"not (device err=%v).\n"+
		"  Nothing was lost in either direction, so there is no uncertainty to blame. This is the\n"+
		"  orientation that spends the single-device slot on a handset holding nothing, and the one\n"+
		"  whose recovery needs a desktop revoke.", phone.err)
}

// TestPBPAIR4_ControlAnOrdinaryPairingStillCompletes keeps the fence above from being
// satisfiable by a machine that never completes anything. On the SAME transport with the burn
// healthy, an ordinary pairing between two consenting operators completes on both legs, the
// machine holds the relay-route consent, and the rendezvous is burned.
func TestPBPAIR4_ControlAnOrdinaryPairingStillCompletes(t *testing.T) {
	mID, dID, secret, rid := pbPair4Identities(t, 0x75)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)

	matched := make(chan struct{})
	close(matched)
	dp := newDeviceParams(dID, secret, rid)
	dp.DeviceSAS = shippedDeviceSAS(make(chan struct{}, 1), matched)

	mEndRaw, dEnd := newRendezvousPipe()
	mEnd := newBurnRendezvous(t, mEndRaw, false)

	mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if mErr != nil || mo == nil {
		t.Fatalf("machine: outcome=%v err=%v; want a completed pairing on a healthy relay", mo, mErr)
	}
	if dErr != nil || do == nil {
		t.Fatalf("device: outcome=%v err=%v; want a completed pairing on a healthy relay", do, dErr)
	}
	if len(mo.Device.ConsentSig) == 0 {
		t.Fatal("the machine completed the pairing holding no relay-route consent")
	}
	if len(mEndRaw.completedIDs()) == 0 {
		t.Error("the machine completed a pairing without burning the rendezvous")
	}
}

// TestPBPAIR4_ControlAGenuineDeclineStillDeclines is the other half of the control, and the one
// that kills the laziest wrong fix: a machine that returned an outcome unconditionally would
// satisfy every agreement assertion above while enrolling devices its operator refused.
func TestPBPAIR4_ControlAGenuineDeclineStillDeclines(t *testing.T) {
	mID, dID, secret, rid := pbPair4Identities(t, 0x76)

	declined := &confirmRecorder{}
	mp := newMachineParams(mID, secret, rid, declined.fn(false, nil))

	matched := make(chan struct{})
	close(matched) // the PHONE's operator is happy; the DESKTOP operator is the one saying no
	dp := newDeviceParams(dID, secret, rid)
	dp.DeviceSAS = shippedDeviceSAS(make(chan struct{}, 1), matched)

	mEndRaw, dEnd := newRendezvousPipe()
	mEnd := newBurnRendezvous(t, mEndRaw, false)

	mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if mo != nil {
		t.Fatalf("the machine produced an outcome for a pairing its own operator DECLINED (err=%v)", mErr)
	}
	if !errors.Is(mErr, ErrConfirmDeclined) {
		t.Fatalf("machine err = %v; want ErrConfirmDeclined", mErr)
	}
	if do != nil {
		t.Fatalf("the phone pinned a machine that DECLINED the pairing (err=%v)", dErr)
	}
	if !errors.Is(dErr, ErrPairingDeclined) {
		t.Fatalf("device err = %v; want ErrPairingDeclined", dErr)
	}
	if _, _, called := declined.snapshot(); called != 1 {
		t.Fatalf("the desktop confirm was invoked %d times; want exactly 1", called)
	}
}
