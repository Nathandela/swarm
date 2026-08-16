// FAILING-FIRST (TDD RED, GG-5) for Wave R3 ROUND 4, the msg4 NON-ACCEPT obligation.
//
// MEDIUM (raised BLOCKING in round 2, answered in round 3 with a comment): msg4 RELEASES
// the push binding -- the wake key, the submit capability and the machine-revoke
// capability, all live at the gateway -- BEFORE the machine's decision arrives. On every
// non-accept outcome below that release (ErrPairingDeclined, a decision that never arrives,
// ErrNotCommitted, a failed acknowledgement) the peer this device just authenticated keeps
// a working capability triple for a live address while this phone believes the pairing
// failed. DeviceParams.PushBinding's doc assigned the revoke duty to a future caller in
// PROSE. These tests make it STRUCTURAL: a DeviceParams.RevokePushBinding func RunDevice
// invokes on every non-accept return below sendConsent, and a device that would release a
// binding with no revoke arm fails CLOSED before msg4 rather than after it.
package pairing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// r3ar4RevokeSpy counts the revocations RunDevice performed.
type r3ar4RevokeSpy struct {
	mu sync.Mutex
	n  int
}

func (s *r3ar4RevokeSpy) fn() func() {
	return func() {
		s.mu.Lock()
		s.n++
		s.mu.Unlock()
	}
}

func (s *r3ar4RevokeSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// r3ar4Device builds a device that conveys a binding, with an operator who has already
// compared the SAS -- so the run reaches msg4 and RELEASES the record.
func r3ar4Device(id *crypto.Identity, secret [32]byte, rid [16]byte, spy *r3ar4RevokeSpy) DeviceParams {
	dp := newDeviceParams(id, secret, rid)
	matched := make(chan struct{})
	close(matched)
	dp.DeviceSAS = shippedDeviceSAS(make(chan struct{}, 1), matched)
	dp.PushBinding = r3ar2ValidBinding()
	if spy != nil {
		dp.RevokePushBinding = spy.fn()
	}
	return dp
}

// TestR3AR4_EveryNonAcceptExitBelowMsg4RevokesTheReleasedBinding drives the three
// non-accept outcomes a shipped device can reach after the consent frame is on the wire and
// requires the revoke arm to have fired on each.
func TestR3AR4_EveryNonAcceptExitBelowMsg4RevokesTheReleasedBinding(t *testing.T) {
	t.Run("the machine declines", func(t *testing.T) {
		mID, _ := crypto.GenerateIdentity()
		dID, _ := crypto.GenerateIdentity()
		secret, rid := fill32(0xA4), fill16(0xA4)

		spy := &r3ar4RevokeSpy{}
		mp := newMachineParams(mID, secret, rid, func(context.Context, [6]string, string) (bool, error) {
			return false, nil
		})
		dp := r3ar4Device(dID, secret, rid, spy)

		mEnd, dEnd := newRendezvousPipe()
		_, _, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
		if do != nil || !errors.Is(dErr, ErrPairingDeclined) {
			t.Fatalf("device: outcome=%v err=%v, want ErrPairingDeclined", do, dErr)
		}
		if got := spy.count(); got != 1 {
			t.Errorf("RevokePushBinding fired %d times on a declined pairing, want 1: the machine "+
				"holds the wake key and BOTH gateway capabilities from msg4 whatever it decided", got)
		}
	})

	t.Run("the durable commit refuses", func(t *testing.T) {
		mID, _ := crypto.GenerateIdentity()
		dID, _ := crypto.GenerateIdentity()
		secret, rid := fill32(0xA5), fill16(0xA5)

		spy := &r3ar4RevokeSpy{}
		mp := newMachineParams(mID, secret, rid, acceptConfirm)
		dp := r3ar4Device(dID, secret, rid, spy)
		dp.Commit = func(*DeviceOutcome) error { return errors.New("disk full") }

		mEnd, dEnd := newRendezvousPipe()
		_, _, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
		if do != nil || !errors.Is(dErr, ErrNotCommitted) {
			t.Fatalf("device: outcome=%v err=%v, want ErrNotCommitted", do, dErr)
		}
		if got := spy.count(); got != 1 {
			t.Errorf("RevokePushBinding fired %d times on a refused commit, want 1", got)
		}
	})

	t.Run("the decision never arrives", func(t *testing.T) {
		dID, _ := crypto.GenerateIdentity()
		mID, _ := crypto.GenerateIdentity()
		secret, rid := fill32(0xA6), fill16(0xA6)

		spy := &r3ar4RevokeSpy{}
		mp := newMachineParams(mID, secret, rid, func(ctx context.Context, _ [6]string, _ string) (bool, error) {
			<-ctx.Done() // the desktop operator never answers
			return false, ctx.Err()
		})
		dp := r3ar4Device(dID, secret, rid, spy)

		mEnd, dEnd := newRendezvousPipe()
		machineCtx, cancelMachine := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelMachine()
		deviceCtx, cancelDevice := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancelDevice()

		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = NewMachine(mp).Pair(machineCtx, mEnd) }()
		do, dErr := RunDevice(deviceCtx, dp, dEnd)
		cancelMachine()
		wg.Wait()

		if do != nil || dErr == nil {
			t.Fatalf("device: outcome=%v err=%v, want a fail-closed error", do, dErr)
		}
		if got := spy.count(); got != 1 {
			t.Errorf("RevokePushBinding fired %d times when the decision never arrived, want 1: "+
				"the binding was released on msg4 and the peer keeps it", got)
		}
	})
}

// TestR3AR4_ASuccessfulPairingNeverRevokes is the guard against the obvious over-correction:
// the arm must fire on non-accept exits and on nothing else.
func TestR3AR4_ASuccessfulPairingNeverRevokes(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0xA7), fill16(0xA7)

	spy := &r3ar4RevokeSpy{}
	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := r3ar4Device(dID, secret, rid, spy)

	mEnd, dEnd := newRendezvousPipe()
	mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if dErr != nil || do == nil {
		t.Fatalf("device: outcome=%v err=%v, want a successful pairing", do, dErr)
	}
	if mErr != nil || mo == nil || mo.PushBinding == nil {
		t.Fatalf("machine: outcome=%v err=%v, want the conveyed binding", mo, mErr)
	}
	if got := spy.count(); got != 0 {
		t.Errorf("RevokePushBinding fired %d times on a SUCCESSFUL pairing, want 0", got)
	}
}

// TestR3AR4_ABindingWithNoRevokeArmIsRefusedBeforeMsg4 is the structural half: the
// obligation cannot be declined by omission. A caller that sets PushBinding without
// RevokePushBinding is refused BEFORE the record is released, so there is no state in which
// the peer holds a live capability triple this device cannot take back.
func TestR3AR4_ABindingWithNoRevokeArmIsRefusedBeforeMsg4(t *testing.T) {
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0xA8), fill16(0xA8)

	dp := r3ar4Device(dID, secret, rid, nil) // PushBinding set, RevokePushBinding nil

	// NO MACHINE IS STARTED, and that is the assertion's shape: the refusal must land
	// before the rendezvous is even claimed, so there is no peer, no transcript and
	// nothing released. A device that got as far as needing a machine has already
	// disclosed the binding.
	_, dEnd := newRendezvousPipe()
	do, dErr := RunDevice(context.Background(), dp, dEnd)
	if do != nil || !errors.Is(dErr, ErrNoPushRevoke) {
		t.Fatalf("device: outcome=%v err=%v, want ErrNoPushRevoke", do, dErr)
	}
	dEnd.mu.Lock()
	claims := len(dEnd.claimIDs)
	dEnd.mu.Unlock()
	if claims != 0 {
		t.Errorf("the device claimed the rendezvous %d time(s) before refusing; the refusal "+
			"must cost a pairing that has disclosed nothing", claims)
	}
}

// TestR3AR4_ARevokeArmWithoutABindingIsNeverCalled: a pre-R3 device (nil PushBinding) that
// happens to carry the arm releases nothing, so nothing is ever revoked.
func TestR3AR4_ARevokeArmWithoutABindingIsNeverCalled(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0xA9), fill16(0xA9)

	spy := &r3ar4RevokeSpy{}
	mp := newMachineParams(mID, secret, rid, func(context.Context, [6]string, string) (bool, error) {
		return false, nil
	})
	dp := r3ar4Device(dID, secret, rid, spy)
	dp.PushBinding = nil

	mEnd, dEnd := newRendezvousPipe()
	_, _, _, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if !errors.Is(dErr, ErrPairingDeclined) {
		t.Fatalf("device err = %v, want ErrPairingDeclined", dErr)
	}
	if got := spy.count(); got != 0 {
		t.Errorf("RevokePushBinding fired %d times for a device that conveyed NO binding, want 0", got)
	}
}
