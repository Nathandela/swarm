package swarmmobile

// Wave R9 gap closure: the phone-side wake-TTL expiry had no individually NAMED test.
// phonecore enforces WakeV1MaxAge inside AcceptWakeV1 (pushbinding.go) and its own suite
// covers the bound one layer down (TestR3A_AcceptWakeV1_FiveMinuteBoundIsEnforced), but
// nothing asserted the property at the seam the FirebaseMessagingService actually calls:
// HandlePushWake, where an expired wake must render NOTHING, must surface as the
// invalid-request class (mobile/pushwake.go: "past its TTL -> ErrClassInvalidRequest"),
// and must not burn the per-address replay coordinate -- a refusal decided before the
// high-water persists, so a FRESH wake with the very same seq is still accepted.
//
// This test asserts existing behaviour, so its RED is the mutation proof recorded in
// docs/verification/r9-red/phone-wait-red.txt (the TTL comparison in AcceptWakeV1
// perturbed, this test fails; restored, green) rather than a missing implementation.

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestR9_HandlePushWake_AWakePastItsTTLRendersNothing: a genuine WakeV1 sealed by the real
// machine-side producer, issued WakeV1MaxAge plus one minute ago, is refused at the facade
// with the invalid-request class, counted, and does not advance the replay window.
func TestR9_HandlePushWake_AWakePastItsTTLRendersNothing(t *testing.T) {
	app := r3ar3App(t)
	key, err := phonecore.NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey: %v", err)
	}
	var addr phonecore.PushAddress
	for i := range addr {
		addr[i] = 0xC5
	}
	if err := app.core.AdoptPushBinding(addr, key); err != nil {
		t.Fatalf("AdoptPushBinding: %v", err)
	}

	stale, err := remotegw.SealWakeV1(key, remotegw.PushAddress(addr), 1,
		time.Now().Add(-(phonecore.WakeV1MaxAge + time.Minute)))
	if err != nil {
		t.Fatalf("remotegw.SealWakeV1: %v", err)
	}
	alert, werr := app.HandlePushWake(base64.StdEncoding.EncodeToString(stale))
	if werr == nil {
		t.Fatalf("a wake %v past issue was accepted; WakeV1MaxAge is the whole of the capture-replay "+
			"bound for the one path that puts text on a lock screen", phonecore.WakeV1MaxAge+time.Minute)
	}
	if alert != nil {
		t.Errorf("alert = %+v for an expired wake; a refused wake renders NOTHING", alert)
	}
	if !errors.Is(werr, phonecore.ErrWakeExpired) {
		t.Errorf("expired wake surfaced as %v, want phonecore.ErrWakeExpired in the chain", werr)
	}
	if !strings.HasPrefix(werr.Error(), ErrClassInvalidRequest+": ") {
		t.Errorf("expired wake surfaced as %q, want class %q (pushwake.go's stated routing: past its "+
			"TTL is a wake this phone will not act on -- nothing rendered, nothing retried)", werr, ErrClassInvalidRequest)
	}
	if drops := app.core.WakeDrops(); drops != 1 {
		t.Errorf("WakeDrops = %d after the expired wake, want 1 (every refusal is counted)", drops)
	}

	// The refusal must not have burnt the coordinate: the SAME seq, freshly issued, is
	// still acceptable, because expiry is decided before the high-water persists.
	fresh, err := remotegw.SealWakeV1(key, remotegw.PushAddress(addr), 1, time.Now())
	if err != nil {
		t.Fatalf("remotegw.SealWakeV1 (fresh): %v", err)
	}
	if _, err := app.HandlePushWake(base64.StdEncoding.EncodeToString(fresh)); err != nil {
		t.Errorf("a fresh wake at the seq the expired one carried was refused (%v); an expired wake "+
			"must not advance the replay window, or a delayed capture silences the live producer", err)
	}
}
