// FAILING-FIRST (TDD RED, GG-5) for Wave R3 round 3, the review's production-reachability
// finding, receipt half: "FCM message receipt must verify the WakeV1 envelope (74 bytes,
// AAD-covered) with the pairing's wake key before acting" (scope 3's hard requirement) was
// true of NO shipped path -- SwarmMessagingService -> App.HandlePushWake fed every payload
// to the legacy epoch-key receiver (AcceptWake, 78 bytes, 10m TTL), so a WakeV1 arriving
// at a real handset was refused as a parse error and never counted by the v1 counter.
//
// WHAT IS UNDER TEST: HandlePushWake -- the ONE facade verb the FirebaseMessagingService
// calls -- must route by wire shape: the 74-byte WakeV1 envelope goes to the per-pairing
// receiver (Core.AcceptWakeV1, PG-WAKE-13), everything else stays on the legacy path
// unchanged (P12 keeps the epoch receiver until the migration retires it; the legacy
// conformance suite in mobile/conformance pins that half). The producer is the REAL
// machine-side remotegw.SealWakeV1, so the routed path is proven against the bytes a
// machine actually submits.
package swarmmobile

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// r3ar3App is an App over one in-memory phone core, as the push-woken Android process
// holds it: never Started, no relay, content tier absent. The DURABILITY of the v1
// receiver's coordinate and counter is phonecore's contract (r3a/r3ar3 tests there); what
// is under test here is the facade ROUTING, which durability does not change.
func r3ar3App(t *testing.T) *App {
	t.Helper()
	core, err := phonecore.Resume(phonecore.Config{})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}
	return &App{core: core}
}

// r3ar3SealV1 seals one genuine WakeV1 with the machine-side producer and returns it as
// the base64 string the FCM data block carries under "e".
func r3ar3SealV1(t *testing.T, key crypto.WakeKey, addr phonecore.PushAddress, seq uint64) string {
	t.Helper()
	env, err := remotegw.SealWakeV1(key, remotegw.PushAddress(addr), seq, time.Now())
	if err != nil {
		t.Fatalf("remotegw.SealWakeV1: %v", err)
	}
	return base64.StdEncoding.EncodeToString(env)
}

// TestR3AR3_HandlePushWake_RoutesTheWakeV1EnvelopeToTheV1Receiver: the shipped FCM
// receipt, given the 74-byte envelope a machine submits for an adopted pairing, must
// verify it with THAT pairing's wake key and only then report the constant alert -- and
// must refuse a replay of the same bytes, which is the cheapest observable proof that the
// v1 receiver (not a canned success, not the legacy parser) handled the wake.
func TestR3AR3_HandlePushWake_RoutesTheWakeV1EnvelopeToTheV1Receiver(t *testing.T) {
	app := r3ar3App(t)
	key, err := phonecore.NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey: %v", err)
	}
	var addr phonecore.PushAddress
	for i := range addr {
		addr[i] = 0xC3
	}
	if err := app.core.AdoptPushBinding(addr, key); err != nil {
		t.Fatalf("AdoptPushBinding: %v", err)
	}

	payload := r3ar3SealV1(t, key, addr, 7)
	alert, err := app.HandlePushWake(payload)
	if err != nil {
		t.Fatalf("HandlePushWake on a genuine WakeV1 for an adopted pairing: %v "+
			"(the shipped receipt does not reach the v1 receiver)", err)
	}
	if alert == nil || alert.Text != WakeNotificationText {
		t.Fatalf("alert = %+v, want the constant %q", alert, WakeNotificationText)
	}
	if alert.ContentReady {
		t.Error("ContentReady = true on a push-woken process that never held the content key")
	}

	// NON-VACUITY (the s17 conformance pattern): the byte-identical payload again must be
	// refused as a replay by the DURABLE per-address coordinate, and the refusal counted.
	if _, err := app.HandlePushWake(payload); err == nil {
		t.Fatal("the same WakeV1 payload was accepted twice; the v1 replay window never saw it")
	}
	if drops := app.core.WakeDrops(); drops != 1 {
		t.Errorf("WakeDrops = %d after the replayed v1 wake, want 1 (every v1 refusal is counted)", drops)
	}
}

// TestR3AR3_HandlePushWake_AV1WakeForAnUnknownAddressIsTheWaitingClass: PG-WAKE-13 step
// 2 at the facade seam. A phone paired minutes ago and backgrounded before its binding
// landed receives a v1 wake it holds no key for; that is the WAITING verdict
// (ErrClassAwaitingKey), never "invalid request" -- the same class split the legacy path
// already draws, because the two states are indistinguishable on screen otherwise.
func TestR3AR3_HandlePushWake_AV1WakeForAnUnknownAddressIsTheWaitingClass(t *testing.T) {
	app := r3ar3App(t)
	key, err := phonecore.NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey: %v", err)
	}
	var unknown phonecore.PushAddress
	for i := range unknown {
		unknown[i] = 0xC4
	}

	_, werr := app.HandlePushWake(r3ar3SealV1(t, key, unknown, 1))
	if werr == nil {
		t.Fatal("a v1 wake for an address this phone holds no key for was accepted")
	}
	if !strings.HasPrefix(werr.Error(), ErrClassAwaitingKey+": ") {
		t.Errorf("unknown-address v1 wake surfaced as %q, want class %q (the waiting verdict; "+
			"a healthy just-paired phone must not be told the request was invalid)", werr, ErrClassAwaitingKey)
	}
	if drops := app.core.WakeDrops(); drops != 1 {
		t.Errorf("WakeDrops = %d after the unknown-address v1 wake, want 1", drops)
	}
}
