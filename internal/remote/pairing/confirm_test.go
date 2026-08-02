// R-PAIR.5 — mandatory desktop confirm, fail-closed. No static pinned and no
// acceptance sent until the operator affirmatively answers; decline / timeout
// leaves no state.
package pairing

import (
	"context"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestPairing_PhotographedQRFailsAtConfirm models an attacker who photographed
// the QR and pairs their OWN device: the handshake completes (the attacker has
// the secret), but the operator sees an unrecognised device and declines. The
// machine returns ErrConfirmDeclined and pins nothing; the device is left with
// ErrPairingDeclined and no pin.
func TestPairing_PhotographedQRFailsAtConfirm(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	atkID, _ := crypto.GenerateIdentity()
	rid := fill16(0x44)
	secret := fill32(0x55)

	decline := func(ctx context.Context, sas [6]string, name string) (bool, error) { return false, nil }
	mp := newMachineParams(mID, secret, rid, decline)
	dp := newDeviceParams(atkID, secret, rid)

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(mp)
	mo, mErr, do, dErr := drivePair(t, m, dp, mEnd, dEnd)

	if !errors.Is(mErr, ErrConfirmDeclined) {
		t.Fatalf("machine err = %v, want ErrConfirmDeclined", mErr)
	}
	if mo != nil {
		t.Error("machine returned an outcome despite declining; it must pin nothing")
	}
	if !errors.Is(dErr, ErrPairingDeclined) {
		t.Errorf("device err = %v, want ErrPairingDeclined", dErr)
	}
	if do != nil {
		t.Error("device produced an outcome despite the machine declining")
	}
}

// TestPairing_DeclineLeavesNoState pins that a declined pairing pins nothing and
// records no routing on either side (fail-closed).
func TestPairing_DeclineLeavesNoState(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x45)
	secret := fill32(0x56)

	decline := func(ctx context.Context, sas [6]string, name string) (bool, error) { return false, nil }
	mp := newMachineParams(mID, secret, rid, decline)
	dp := newDeviceParams(dID, secret, rid)

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(mp)
	mo, mErr, do, dErr := drivePair(t, m, dp, mEnd, dEnd)

	if !errors.Is(mErr, ErrConfirmDeclined) {
		t.Fatalf("machine err = %v, want ErrConfirmDeclined", mErr)
	}
	if mo != nil {
		t.Error("declined pairing left a machine outcome")
	}
	if do != nil {
		t.Error("declined pairing left a device outcome")
	}
	if !errors.Is(dErr, ErrPairingDeclined) {
		t.Errorf("device err = %v, want ErrPairingDeclined", dErr)
	}
}

// TestPairing_ConfirmTimeoutFailsClosed pins that when the confirm prompt elapses
// (the callback owns its TTL and returns ErrConfirmTimeout), the pairing fails
// closed: the machine returns ErrConfirmTimeout, pins nothing, and the device is
// left declined.
func TestPairing_ConfirmTimeoutFailsClosed(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x46)
	secret := fill32(0x57)

	timeout := func(ctx context.Context, sas [6]string, name string) (bool, error) {
		return false, ErrConfirmTimeout
	}
	mp := newMachineParams(mID, secret, rid, timeout)
	dp := newDeviceParams(dID, secret, rid)

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(mp)
	mo, mErr, do, dErr := drivePair(t, m, dp, mEnd, dEnd)

	if !errors.Is(mErr, ErrConfirmTimeout) {
		t.Fatalf("machine err = %v, want ErrConfirmTimeout", mErr)
	}
	if mo != nil {
		t.Error("confirm timeout left a machine outcome; it must fail closed")
	}
	if do != nil {
		t.Error("confirm timeout left a device outcome")
	}
	if !errors.Is(dErr, ErrPairingDeclined) {
		t.Errorf("device err = %v, want ErrPairingDeclined", dErr)
	}
}
