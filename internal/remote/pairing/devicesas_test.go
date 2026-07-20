// PR-M1 FAILING-FIRST (TDD RED, GG-5): the device must SURFACE its SAS to the
// phone operator BEFORE it blocks on / commits to the machine's pairing decision.
// Today RunDevice derives the SAS but never hands it to the caller before waiting
// on the decision frame, so a phone UI cannot show the operator anything to
// compare out-of-band at the right moment. This test pins the contract for a new,
// OPTIONAL, nil-safe device-side callback the implementer must add.
//
// CONTRACT the implementer must deliver (mirrors Machine's ConfirmFunc seam):
//
//	type DeviceSASFunc func(ctx context.Context, sas [4]string) error
//	DeviceParams.DeviceSAS DeviceSASFunc  // optional; nil => surfaced nowhere (back-compat)
//
// RunDevice MUST invoke p.DeviceSAS (when non-nil) with the SAS derived from the
// Noise channel binding AFTER the handshake completes but BEFORE it blocks on the
// machine's decision frame and BEFORE any DeviceOutcome is applied. A non-nil
// error from the callback fails the pairing CLOSED (device pins nothing). A nil
// callback is a no-op, so every existing test that builds a device WITHOUT this
// field must still compile and pass.
//
// This file references DeviceParams.DeviceSAS / DeviceSASFunc, which do not exist
// yet, so the package test build fails (undefined-symbol RED) until the callback
// is added. It is otherwise ADDITIVE: no existing test and no implementation is
// modified.
package pairing

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// compile-time contract pin: the field's type and signature are exactly this.
var _ DeviceSASFunc = func(ctx context.Context, sas [4]string) error { return nil }

// TestPairing_DeviceSurfacesSASBeforeDecision pins PR-M1: the device SAS callback
// fires exactly once, with the SAS the device outcome carries, and BEFORE the
// device commits to the pairing decision — proven by gating the machine's desktop
// confirm on the device having already surfaced its SAS. On a clean run that SAS
// also matches the machine-side SAS.
func TestPairing_DeviceSurfacesSASBeforeDecision(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x91)
	secret := fill32(0x92)

	deviceSASShown := make(chan [4]string, 1)
	var deviceSASCalls int32
	var gotDeviceSAS [4]string

	// The machine's confirm only answers once the device has surfaced its SAS, so
	// an implementation that surfaces the device SAS only AFTER the decision would
	// deadlock. The timeout converts that would-be deadlock into a clean, loud
	// failure (the machine declines) instead of hanging the suite.
	confirm := func(ctx context.Context, sas [4]string, name string) (bool, error) {
		select {
		case gotDeviceSAS = <-deviceSASShown:
			return true, nil
		case <-time.After(2 * time.Second):
			return false, errors.New("device SAS was not surfaced before the pairing decision")
		}
	}

	mp := newMachineParams(mID, secret, rid, confirm)
	dp := newDeviceParams(dID, secret, rid)
	dp.DeviceSAS = func(ctx context.Context, sas [4]string) error {
		atomic.AddInt32(&deviceSASCalls, 1)
		deviceSASShown <- sas // buffered: the device does not block surfacing it
		return nil
	}

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(mp)
	mo, mErr, do, dErr := drivePair(t, m, dp, mEnd, dEnd)
	if mErr != nil {
		t.Fatalf("machine Pair: %v", mErr)
	}
	if dErr != nil {
		t.Fatalf("device RunDevice: %v", dErr)
	}
	if mo == nil || do == nil {
		t.Fatal("nil outcome on a completed pairing")
	}
	if n := atomic.LoadInt32(&deviceSASCalls); n != 1 {
		t.Fatalf("device SAS callback fired %d times, want exactly 1 (before the decision)", n)
	}
	if gotDeviceSAS != do.SAS {
		t.Errorf("device SAS callback saw %v, device outcome carries %v", gotDeviceSAS, do.SAS)
	}
	if gotDeviceSAS != mo.SAS {
		t.Errorf("device SAS %v != machine SAS %v on a clean run", gotDeviceSAS, mo.SAS)
	}
}
