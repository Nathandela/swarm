// R-PAIR.1 (single-use secret), R-PAIR.8 (lifecycle + dual-side rate limits),
// R-PAIR.9 / D.0-A12 (headless-machine pairing scope).
package pairing

import (
	"context"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestPairing_SecretSingleUse pins R-PAIR.1: the single-use secret is consumed on
// the first completed handshake; a second Pair on the same Machine is refused
// with ErrSecretConsumed.
func TestPairing_SecretSingleUse(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x61)
	secret := fill32(0x71)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := newDeviceParams(dID, secret, rid)

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(mp)
	mo, mErr, _, dErr := drivePair(t, m, dp, mEnd, dEnd)
	if mErr != nil {
		t.Fatalf("machine Pair: %v", mErr)
	}
	if dErr != nil {
		t.Fatalf("device RunDevice: %v", dErr)
	}
	if mo == nil {
		t.Fatal("nil outcome on a completed pairing")
	}

	// The secret is spent: a second attempt on the same Machine is refused.
	mEnd2, _ := newRendezvousPipe()
	_, err := m.Pair(context.Background(), mEnd2)
	if !errors.Is(err, ErrSecretConsumed) {
		t.Fatalf("second Pair err = %v, want ErrSecretConsumed", err)
	}
}

// TestPairing_NoStandingListener pins R-PAIR.8: a Machine is not listening before
// Pair, and after Pair returns it is not listening and the rendezvous was created
// exactly once then completed (burned) — no standing listener between
// invocations.
func TestPairing_NoStandingListener(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x62)
	secret := fill32(0x72)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := newDeviceParams(dID, secret, rid)

	m := NewMachine(mp)
	if m.Listening() {
		t.Fatal("machine is listening before Pair")
	}

	mEnd, dEnd := newRendezvousPipe()
	mo, mErr, _, dErr := drivePair(t, m, dp, mEnd, dEnd)
	if mErr != nil {
		t.Fatalf("machine Pair: %v", mErr)
	}
	if dErr != nil {
		t.Fatalf("device RunDevice: %v", dErr)
	}
	if mo == nil {
		t.Fatal("nil outcome on a completed pairing")
	}
	if m.Listening() {
		t.Error("machine is still listening after Pair returned")
	}
	if n := len(mEnd.createdIDs()); n != 1 {
		t.Errorf("rendezvous created %d times, want 1", n)
	}
	if n := len(mEnd.completedIDs()); n != 1 {
		t.Errorf("rendezvous completed (burned) %d times, want 1", n)
	}
}

// TestPairing_RateLimitedBothSides pins R-PAIR.8's dual-side rate limiting: the
// gateway-side limiter refuses an over-budget attempt before touching the
// transport, and a relay-side rate refusal (transport Create error) surfaces as
// ErrRateLimited.
func TestPairing_RateLimitedBothSides(t *testing.T) {
	t.Run("gateway_side", func(t *testing.T) {
		mID, _ := crypto.GenerateIdentity()
		mp := newMachineParams(mID, fill32(0x01), fill16(0x01), acceptConfirm)
		mp.Limiter = &countingLimiter{remaining: 0} // budget exhausted
		m := NewMachine(mp)

		mEnd, _ := newRendezvousPipe()
		_, err := m.Pair(context.Background(), mEnd)
		if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("machine Pair err = %v, want ErrRateLimited", err)
		}
		if n := len(mEnd.createdIDs()); n != 0 {
			t.Errorf("machine created %d rendezvous despite being rate limited; want 0", n)
		}
	})

	t.Run("relay_side", func(t *testing.T) {
		mID, _ := crypto.GenerateIdentity()
		mp := newMachineParams(mID, fill32(0x01), fill16(0x01), acceptConfirm)
		m := NewMachine(mp)

		rt := &refusingRendezvous{createErr: ErrRateLimited}
		_, err := m.Pair(context.Background(), rt)
		if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("machine Pair err = %v, want relay-surfaced ErrRateLimited", err)
		}
	})
}

// TestPairing_RefusedWithoutLocalConsole pins R-PAIR.9 / D.0-A12: Phase-1 pairing
// requires a local operator at a physical console. A headless / SSH-only machine
// (no local console) is refused with ErrHeadlessRefused BEFORE any rendezvous is
// opened — collapsing the camera-OOB + independent-desktop-confirm pillars into a
// single in-band channel is an RCE-via-phone risk, so it is refused (headless
// out-of-band pairing is a named Phase-3 follow-up, not Phase 1).
func TestPairing_RefusedWithoutLocalConsole(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	mp := newMachineParams(mID, fill32(0x01), fill16(0x01), acceptConfirm)
	mp.LocalConsole = false // headless / SSH-only: no operator at a local display
	m := NewMachine(mp)

	mEnd, _ := newRendezvousPipe()
	_, err := m.Pair(context.Background(), mEnd)
	if !errors.Is(err, ErrHeadlessRefused) {
		t.Fatalf("headless Pair err = %v, want ErrHeadlessRefused", err)
	}
	if n := len(mEnd.createdIDs()); n != 0 {
		t.Errorf("headless pairing opened %d rendezvous before refusing; want 0", n)
	}
}
