package protocol

// FAILING-FIRST protocol tests for slice A5-e1 — two hardening fixes surfaced by the
// A5 cross-model review (docs/verification/remote-phaseA-a5-review.md). Both live in
// internal/protocol/server.go and both are keystroke-injection / availability faults in
// the remote-tier take_control control-session lifecycle, so the property under test is
// the daemon-side SIDE EFFECT — whether a subsequent keystroke reaches the session's
// shim stub — exactly as the A5-b tests (takecontrol_input_test.go) assert.
//
// R3 — a REPLAYED/stale take_control_end wrongly shuts a NEWER control session.
// handleTakeControlEnd clears cc.control BEFORE checking the session/generation match,
// so a stale end carrying an OLD generation (reordered by the untrusted relay) shuts the
// input gate on a live, superseded-to-newer control session even though releaseLease
// correctly refuses to release it on the generation mismatch. The fix clears cc.control
// ONLY when its target AND generation match the end request.
//
// R5 — a huge TTLSeconds overflows int64 and yields an immediately-expired session.
// handleTakeControl computes `time.Duration(c.TTLSeconds) * time.Second`; a very large
// TTLSeconds overflows int64 to a NEGATIVE duration, which is not `> maxControlSessionTTL`,
// so the clamp misses it and expiry lands in the PAST — the session is immediately expired
// and every keystroke is dropped, violating the "never immediately-expired" invariant. The
// fix clamps the lower bound AFTER the multiply (`if ttl <= 0 { ttl = default }`).
//
// These are BEHAVIORAL RED: the code compiles (the A5-a/b/c symbols already exist and are
// GREEN), so each test fails on its assertion — the injected keystroke is dropped — not on
// a compile error.
//
// Harness reuse (sibling _test.go files, package protocol): serveRemote (remote-tier
// Server on the accepting stubDaemon, which is NOT an OperationClaimer, so no gate token
// is required), rawDial/hello, the takeControl helper + nextControl/syncControlOp
// (takecontrol_input_test.go / remote_input_refused_test.go), serverNowNS (the server-clock
// seam), and stubStream.inputBytes (the shim side effect).

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

// TestProtocol_ReplayedTakeControlEndDoesNotShutNewerSession pins R3: on ONE remote-tier
// connection, take_control establishes a lease at generation G1, then take_control AGAIN
// on the SAME session supersedes it to generation G2 > G1 (cc.control now bound to G2).
// A STALE take_control_end carrying the OLD generation G1 arrives (reordered by the
// untrusted relay). It targets the superseded lease, so it must NOT touch the live G2
// control session: a subsequent keystroke must still reach the shim.
//
// Two generations on ONE connection: calling take_control twice on the same session
// supersedes (attach bumps the monotonic genCounter), and the two OpLease grants carry
// G1 then G2 (G2 > G1) — no second connection is needed.
//
// RED today: handleTakeControlEnd clears cc.control UNCONDITIONALLY before the
// generation check, so the stale G1 end shuts the input gate on the live G2 session and
// the keystroke is dropped (releaseLease still refuses to release G2, so the lease is
// alive-but-input-dead). The assertion below fails until the end clears cc.control only
// on a session+generation match.
func TestProtocol_ReplayedTakeControlEndDoesNotShutNewerSession(t *testing.T) {
	stub := newStubDaemon() // authzFn nil => the signed take_control is accepted; not an OperationClaimer
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	// Take control (generation G1), then AGAIN on the same session/connection: the second
	// take_control supersedes the first, so its OpLease carries a higher generation G2 and
	// cc.control is rebound to G2.
	lease1 := takeControl(t, rc, rep.EndpointID, sid, 3600)
	lease2 := takeControl(t, rc, rep.EndpointID, sid, 3600)
	if lease2.Generation <= lease1.Generation {
		t.Fatalf("second take_control generation = %d; want > first generation %d (a supersede yields a higher generation)", lease2.Generation, lease1.Generation)
	}

	// A STALE take_control_end carrying the OLD generation G1 (a relay reorder). It targets
	// the superseded lease, so it must NOT clear the live G2 control session.
	rc.writeControl(Control{Op: OpTakeControlEnd, EndpointID: rep.EndpointID, SessionID: sid, Generation: lease1.Generation})

	// A keystroke on the still-live G2 session must reach the shim.
	inject := []byte("echo still-alive\r")
	rc.writeFrame(wire.TDataIn, inject)
	// Ordered sync: once the OpList reply arrives, the end + input written before it were
	// already fully handled (one in-order serve loop per connection).
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	st := stub.lastStream()
	if st == nil {
		t.Fatalf("take_control opened no upstream stream; want the live G2 control lease's stream")
	}
	if !bytes.Contains(st.inputBytes(), inject) {
		t.Fatalf("keystroke dropped after a STALE take_control_end (old generation %d) shut the live G2 (generation %d) session: got %q, want it to contain %q",
			lease1.Generation, lease2.Generation, st.inputBytes(), inject)
	}
}

// TestProtocol_TakeControlHugeTTLNotImmediatelyExpired pins R5: a take_control whose
// TTLSeconds is large enough that `time.Duration(TTLSeconds) * time.Second` overflows
// int64 to a NEGATIVE duration must NOT produce an immediately-expired control session.
// The server clock is frozen at a base instant, take_control is issued with the
// overflowing TTL, and a keystroke sent IMMEDIATELY (clock not advanced) must reach the
// shim.
//
// RED today: the overflow yields a negative ttl that slips past the `> maxControlSessionTTL`
// clamp, so expiry = now.Add(negative) lands in the PAST and the four-clause gate's lazy-
// expiry clause drops the keystroke at once. The assertion fails until the lower bound is
// clamped AFTER the multiply.
func TestProtocol_TakeControlHugeTTLNotImmediatelyExpired(t *testing.T) {
	base := time.Now()
	old := serverNowNS.Load()
	serverNowNS.Store(base.UnixNano()) // freeze the server clock at base
	defer serverNowNS.Store(old)

	// TTLSeconds large enough that TTLSeconds * time.Second (1e9 ns/s) overflows int64 to a
	// NEGATIVE time.Duration. Kept as a runtime variable (not a constant) so Go does not
	// reject the deliberate overflow at compile time, mirroring the runtime multiply on the
	// c.TTLSeconds struct field in handleTakeControl.
	ttlSeconds := int(math.MaxInt64/int64(time.Second)) + 1 // 9223372037 on a 64-bit int
	if d := time.Duration(ttlSeconds) * time.Second; d > 0 {
		t.Fatalf("test premise invalid: TTLSeconds=%d yields Duration %v; it must overflow int64 to a NEGATIVE duration", ttlSeconds, d)
	}

	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	// Establish the control session with the overflowing TTL; the clock is NOT advanced.
	takeControl(t, rc, rep.EndpointID, sid, ttlSeconds)

	// A keystroke sent IMMEDIATELY (server clock still at base) must reach the shim: a huge
	// TTL must never produce an immediately-expired session.
	inject := []byte("echo not-expired\r")
	rc.writeFrame(wire.TDataIn, inject)
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	st := stub.lastStream()
	if st == nil {
		t.Fatalf("take_control opened no upstream stream; want the control lease's stream")
	}
	if !bytes.Contains(st.inputBytes(), inject) {
		t.Fatalf("keystroke dropped immediately after take_control with a huge TTL (int64 overflow -> negative ttl -> past expiry): got %q, want it to contain %q", st.inputBytes(), inject)
	}
}
