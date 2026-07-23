package protocol

// FAILING-FIRST protocol test for slice A7 input Slice 6 (R7): the control-session
// lifetime must bind to what the DEVICE SIGNED, not merely the unsigned TTLSeconds hint.
//
// The signed ExpiresAt is covered by the device command signature
// (crypto.Command.Canonical appends it to the signing input), so a relay cannot lengthen
// it without breaking the signature. requireRemoteAuthz already guarantees a non-nil
// ExpiresAt on the remote tier and verifies *c.ExpiresAt against the signature, so in
// handleTakeControl c.ExpiresAt IS the device-signed expiry. R7 makes the session expiry
// the EARLIEST of three bounds: the signed ExpiresAt, now+maxControlSessionTTL (the server
// cap), and — when TTLSeconds > 0 — now+TTLSeconds (the A5-b hint).
//
// Expiry is a private controlSession field, so it is observed BEHAVIORALLY (as the sibling
// lazy-expiry test does): freeze the server clock at base, establish the session, advance
// the clock to a probe instant, and check whether a keystroke still reaches the shim — a
// live session forwards it, an expired one drops it.
//
// RED today: handleTakeControl ignores the signed ExpiresAt and derives expiry from the
// TTLSeconds hint alone (defaulting to 5m / clamping to 30m). So a 2m-signed session with a
// 1h TTL wrongly lives to base+30m (property 1 probe at base+3m REACHES, assertion wants
// DROPPED), and a 90m-signed session with no TTL wrongly expires at the 5m default (property
// 2 probe at base+29m DROPPED, assertion wants REACHES). Both assertions fail until the R7
// earliest-of binding lands.

import (
	"bytes"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

// controlSessionLiveAt establishes a signed take_control at a frozen base instant with the
// given device-signed ExpiresAt offset and TTLSeconds, advances the server clock to
// base+probe, and reports whether a keystroke then reaches the shim (true = the control
// session is still live; false = dropped by lazy expiry). The stub authenticator accepts
// any signature, so the resulting expiry is a pure function of the R7 binding under test.
func controlSessionLiveAt(t *testing.T, signedExp time.Duration, ttlSeconds int, probe time.Duration) bool {
	t.Helper()
	base := time.Now()
	old := serverNowNS.Load()
	serverNowNS.Store(base.UnixNano()) // freeze the server clock at base
	defer serverNowNS.Store(old)

	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := base.Add(signedExp)
	rc.writeControl(Control{
		Op: OpTakeControl, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JTAKE0000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
		GateToken:  "gate-tok",
		TTLSeconds: ttlSeconds,
	})
	if got := nextControl(t, rc); got.Op != OpLease || got.Generation == 0 {
		t.Fatalf("take_control refused: op %q code %q gen %d; want an OpLease grant", got.Op, got.ErrorCode, got.Generation)
	}

	// Advance the server clock to the probe instant, then send a keystroke and sync.
	serverNowNS.Store(base.Add(probe).UnixNano())
	inject := []byte("probe-input\r")
	rc.writeFrame(wire.TDataIn, inject)
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	st := stub.lastStream()
	return st != nil && bytes.Contains(st.inputBytes(), inject)
}

// TestTakeControl_ExpiryBoundToSignedExpiresAt pins R7: the control-session lifetime is the
// earliest of the signed ExpiresAt, now+server-max, and (when set) now+TTLSeconds.
func TestTakeControl_ExpiryBoundToSignedExpiresAt(t *testing.T) {
	// Property 1 (R7): a signed ExpiresAt shorter than both the TTL hint and the server cap
	// binds the lifetime. TTLSeconds=3600 (1h) and the 30m cap are both far larger than the
	// signed 2m, so a keystroke at base+3m must be DROPPED (bound to the signed 2m, not 30m
	// nor 1h) while one at base+90s still REACHES (never clamped below the signed value).
	if controlSessionLiveAt(t, 2*time.Minute, 3600, 3*time.Minute) {
		t.Fatalf("R7: keystroke reached at base+3m; want DROPPED — expiry must bind to the signed ExpiresAt (base+2m), not the 30m cap or the 1h TTL")
	}
	if !controlSessionLiveAt(t, 2*time.Minute, 3600, 90*time.Second) {
		t.Fatalf("keystroke dropped at base+90s; want REACHES — expiry must not clamp below the signed ExpiresAt (base+2m)")
	}

	// Property 2 (R7 + server cap): a signed ExpiresAt beyond the server max (base+90m) clamps
	// to base+max (30m). A keystroke at base+29m REACHES (the session lives to the cap, NOT the
	// old 5m default) and one at base+31m is DROPPED (the signed 90m cannot exceed the cap).
	if !controlSessionLiveAt(t, 90*time.Minute, 0, 29*time.Minute) {
		t.Fatalf("keystroke dropped at base+29m with signed ExpiresAt=base+90m; want REACHES — the lifetime must extend to the server max (30m)")
	}
	if controlSessionLiveAt(t, 90*time.Minute, 0, 31*time.Minute) {
		t.Fatalf("keystroke reached at base+31m; want DROPPED — a signed ExpiresAt beyond the server max must clamp to base+30m")
	}

	// Property 3 (A5-b preserved): a TTLSeconds smaller than the signed ExpiresAt still caps at
	// base+TTLSeconds. TTL=60s < the signed 10m, so a keystroke at base+90s is DROPPED (capped
	// at the 1m TTL) while one at base+30s REACHES.
	if controlSessionLiveAt(t, 10*time.Minute, 60, 90*time.Second) {
		t.Fatalf("A5-b: keystroke reached at base+90s with TTLSeconds=60; want DROPPED — expiry must cap at base+TTLSeconds (1m)")
	}
	if !controlSessionLiveAt(t, 10*time.Minute, 60, 30*time.Second) {
		t.Fatalf("keystroke dropped at base+30s with TTLSeconds=60; want REACHES — the 1m TTL window must still be live")
	}
}
