package pairing

// PB-PAIR-4 -- THE ACKNOWLEDGEMENT ATTESTS A DURABLE COMMIT, NOT THE ARRIVAL OF A FRAME.
//
// pbpair4_agreement_test.go fences the orientation where the machine throws away an outcome it
// has already given. This file fences the OTHER one, which that file's subject could not reach:
// the machine holding a device whose phone wrote nothing.
//
// The machine no longer commits on having SENT its acceptance -- it waits to be acknowledged
// (ADR-007 B81(2)) and internal/skeleton enrols on that acknowledgement -- so every guarantee
// the machine has rests on what the frame MEANS. A device that acknowledges first and writes
// afterwards makes it mean "the acceptance arrived", and a full disk, a read-only data
// directory, a Keystore refusal or process death in the window that follows leaves the machine
// enrolled with remote control live against a handset holding nothing.
//
// WHAT IS ASSERTED, IN BOTH DIRECTIONS.
//
//	A refused commit must leave the machine claiming NOTHING -- the two legs agreeing, not a
//	half-pair -- and it must get there by never sending the frame, rather than by any later
//	retraction, because there is no later.
//
//	A successful commit must be COMPLETE before that frame leaves. Stated over the wire rather
//	than over a flag: exactly one device frame -- the acknowledgement -- may follow the commit.
//	A fixture that only watched the outcome would pass on an implementation that acknowledged
//	first and committed second, since both ends still agree when nothing fails.
//
// SCOPE: no existing test is modified and nothing here touches internal/remote/crypto (FROZEN).

import (
	"errors"
	"testing"
)

// commitRecorder is DeviceParams.Commit plus the one observation that distinguishes the two
// implementations: how many frames this device had put on the wire at the moment the commit
// ran. The acknowledgement is a frame, so "the commit ran before it" is decidable from the
// count and does not depend on knowing how many frames precede it.
type commitRecorder struct {
	end          *fakeRendezvous
	err          error
	called       int
	sendsAtEntry int
	outcome      *DeviceOutcome
}

func (c *commitRecorder) fn() DeviceCommitFunc {
	return func(out *DeviceOutcome) error {
		c.called++
		c.sendsAtEntry = len(c.end.sentBytes())
		c.outcome = out
		return c.err
	}
}

// TestPBPAIR4_ARefusedDurableCommitLeavesTheMachineClaimingNothing is the requirement.
//
// Every frame of the ceremony is delivered intact, both operators consent, and the ONE thing
// that fails is the device's own durable write. The machine must end the ceremony holding no
// device: the alternative is a machine enrolled against a handset that knows nothing about it,
// which spends the single-device slot, refuses every further pairing, and is recovered only
// from the desktop.
func TestPBPAIR4_ARefusedDurableCommitLeavesTheMachineClaimingNothing(t *testing.T) {
	mID, dID, secret, rid := pbPair4Identities(t, 0x91)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)

	mEnd, dEnd := newRendezvousPipe()

	matched := make(chan struct{})
	close(matched) // both operators compared the codes and they agree
	dp := newDeviceParams(dID, secret, rid)
	dp.DeviceSAS = shippedDeviceSAS(make(chan struct{}, 1), matched)

	// The disk is full, the data directory is read-only, or the Keystore refused. Whatever the
	// cause, the phone cannot remember this machine.
	refused := errors.New("the phone could not write its durable state")
	commit := &commitRecorder{end: dEnd, err: refused}
	dp.Commit = commit.fn()

	mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)

	if commit.called != 1 {
		t.Fatalf("the durable commit was invoked %d times; want exactly 1. This test asserts "+
			"nothing about a ceremony that never reached it", commit.called)
	}
	if do != nil || !errors.Is(dErr, ErrNotCommitted) || !errors.Is(dErr, refused) {
		t.Fatalf("device: outcome=%v err=%v; want no outcome and an error wrapping both "+
			"ErrNotCommitted and the refusal itself -- a phone that reports a pairing it could "+
			"not write is the defect ADR-007 B60 closed one frame earlier", do, dErr)
	}

	// THE PROPERTY.
	if mo != nil {
		t.Fatalf("HALF-PAIR (PB-PAIR-4): the machine claimed device %x while the device's durable "+
			"commit REFUSED (err=%v).\n"+
			"  Nothing was lost: every frame arrived and both operators consented. The machine\n"+
			"  enrols on the acknowledgement, so an acknowledgement sent before the commit\n"+
			"  attests only that the acceptance frame arrived -- and this is the orientation\n"+
			"  that spends the single-device slot, leaves remote control live against a handset\n"+
			"  holding nothing, and is recovered only by a desktop revoke.", mo.DeviceStatic, dErr)
	}
	if !errors.Is(mErr, ErrAcceptUnacknowledged) {
		t.Fatalf("machine err = %v; want ErrAcceptUnacknowledged. The machine must report the "+
			"ABSENCE of an acknowledgement -- it said yes and was never told -- rather than some "+
			"other cause that happens to leave it empty-handed", mErr)
	}

	// AND IT MUST GET THERE BY NOT SENDING, not by anything after. There is no frame after the
	// acknowledgement in which a device could take one back.
	if n := len(dEnd.sentBytes()); n != commit.sendsAtEntry {
		t.Fatalf("the device sent %d frame(s) after its durable commit refused (%d at entry, %d "+
			"total). A refused commit must fail the pairing CLOSED with nothing further on the "+
			"wire", n-commit.sendsAtEntry, commit.sendsAtEntry, n)
	}
}

// TestPBPAIR4_TheCommitCompletesBeforeTheAcknowledgementLeaves is the ordering itself, and the
// half a fence watching only outcomes cannot see: with nothing failing, both ends agree whether
// the commit ran first or second.
func TestPBPAIR4_TheCommitCompletesBeforeTheAcknowledgementLeaves(t *testing.T) {
	mID, dID, secret, rid := pbPair4Identities(t, 0x92)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)

	mEnd, dEnd := newRendezvousPipe()

	matched := make(chan struct{})
	close(matched)
	dp := newDeviceParams(dID, secret, rid)
	dp.DeviceSAS = shippedDeviceSAS(make(chan struct{}, 1), matched)

	commit := &commitRecorder{end: dEnd}
	dp.Commit = commit.fn()

	mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if mErr != nil || mo == nil {
		t.Fatalf("machine: outcome=%v err=%v; want a completed pairing", mo, mErr)
	}
	if dErr != nil || do == nil {
		t.Fatalf("device: outcome=%v err=%v; want a completed pairing", do, dErr)
	}
	if commit.called != 1 {
		t.Fatalf("the durable commit was invoked %d times; want exactly 1", commit.called)
	}

	// EXACTLY ONE FRAME MAY FOLLOW THE COMMIT, and it is the acknowledgement. Counting rather
	// than naming the frame keeps this from encoding how many frames precede it.
	sent := len(dEnd.sentBytes())
	switch {
	case sent-commit.sendsAtEntry == 0:
		t.Fatal("the device sent NO frame after its durable commit, so the machine was never " +
			"acknowledged and this control did not drive the ceremony it claims to")
	case sent-commit.sendsAtEntry != 1:
		t.Fatalf("%d device frame(s) followed the durable commit (%d at entry, %d total); want "+
			"exactly 1, the acknowledgement.\n"+
			"  The commit must be COMPLETE before that frame leaves: the machine enrols on it, so\n"+
			"  a frame sent first attests only that the acceptance arrived, and everything that\n"+
			"  can fail afterwards leaves the machine holding a device whose phone wrote nothing\n"+
			"  (PB-PAIR-4).", sent-commit.sendsAtEntry, commit.sendsAtEntry, sent)
	}

	// The commit is handed the outcome the device is about to return, not a copy that could
	// drift from it: a phone pinning coordinates other than the ones it reports is a half-pair
	// the two legs cannot see either.
	if commit.outcome != do {
		t.Fatalf("the commit was handed %v and RunDevice returned %v; they must be the same "+
			"outcome", commit.outcome, do)
	}
}

// TestPBPAIR4_ADeviceWithNothingDurableStillPairs states the nil case rather than leaving it to
// be discovered.
//
// A nil Commit means this device holds NO durable state -- internal/skeleton's simulated phones
// are all of them -- so its commit is trivially complete before the acknowledgement leaves and
// the frame's meaning is unchanged. Refusing to pair would break every one of those callers to
// protect a property they already satisfy.
func TestPBPAIR4_ADeviceWithNothingDurableStillPairs(t *testing.T) {
	mID, dID, secret, rid := pbPair4Identities(t, 0x93)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)

	matched := make(chan struct{})
	close(matched)
	dp := newDeviceParams(dID, secret, rid)
	dp.DeviceSAS = shippedDeviceSAS(make(chan struct{}, 1), matched)
	dp.Commit = nil

	mEnd, dEnd := newRendezvousPipe()
	mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if mErr != nil || mo == nil {
		t.Fatalf("machine: outcome=%v err=%v; want a completed pairing", mo, mErr)
	}
	if dErr != nil || do == nil {
		t.Fatalf("device: outcome=%v err=%v; want a completed pairing", do, dErr)
	}
}
