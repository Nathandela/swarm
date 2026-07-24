package protocol

// FAILING-FIRST security tests for audit finding C1 [UNANIMOUS BLOCKER]: a device_revoke
// must SEVER the revoked device's LIVE control lease and terminal peek AT THE DAEMON — not
// merely refuse a FUTURE take_control. Today controlGateOpen checks only the GLOBAL kill
// switch (devices.Count()>0), so after `swarm remote revoke B` — with any OTHER paired
// device keeping the global switch ON — device B keeps injecting keystrokes into a session
// it controls for up to maxControlSessionTTL (30 min) and keeps reading terminal peeks. The
// daemon (the sole trusted component) must sever both, independent of the untrusted relay.
//
// The fix has two layers, BOTH exercised here:
//   - PER-KEYSTROKE defense: controlGateOpen re-checks that the LEASE-ESTABLISHING device
//     (recorded on controlSession.deviceID at grant time) is STILL registered, via an
//     optional backend DeviceRegistrar.DeviceRegistered — so a revoked device's next
//     keystroke is dropped even when the revoke was handled by a DIFFERENT Server (the
//     production owner/remote split shares ONE backend registry but SEPARATE lease maps).
//   - PROACTIVE release: handleDeviceRevoke, after the backend removes the device, releases
//     every control lease the revoked device established on THIS Server and cancels every
//     active terminal peek — so the live lease + peek are gone at once when the revoke
//     reaches the Server holding them.
//
// FROZEN API these tests expect (the GREEN implementer adds it):
//   - controlSession gains an (internal) deviceID, set from the AUTHENTICATED c.DeviceID in
//     handleTakeControl (after requireRemoteAuthz).
//   - type DeviceRegistrar interface { DeviceRegistered(deviceID string) bool } — optional
//     backend interface controlGateOpen consults (skipped when absent, like KillSwitch).
//   - handleDeviceRevoke severs the live lease + peek after RevokeDevice succeeds.

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

// revokeSeverStub is a full remote-tier backend (DaemonAPI + DeviceAuthenticator +
// KillSwitch + OperationClaimer via the embedded *stubDaemon) that ALSO implements
// DeviceRevoker, DeviceRegistrar, and TerminalTapper off a SINGLE live device set — exactly
// the production coreAPI shape. RemoteControlEnabled derives from the set size (Count()>0),
// so revoking ONE device while OTHERS remain leaves the GLOBAL kill switch ON: the sever
// must come from the per-device presence check + proactive release, NOT the global switch.
type revokeSeverStub struct {
	*stubDaemon
	mu      sync.Mutex
	devices map[string]bool
	taps    []*stubStream // one per TerminalTap call (newest last)
}

func newRevokeSeverStub(ids ...string) *revokeSeverStub {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return &revokeSeverStub{stubDaemon: newStubDaemon(), devices: m}
}

// RevokeDevice removes deviceID from the live set (making it a DeviceRevoker).
func (s *revokeSeverStub) RevokeDevice(deviceID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.devices[deviceID] {
		return false, nil
	}
	delete(s.devices, deviceID)
	return true, nil
}

// DeviceRegistered reports whether deviceID is still paired (making it a DeviceRegistrar) —
// the per-keystroke presence check controlGateOpen must consult.
func (s *revokeSeverStub) DeviceRegistered(deviceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devices[deviceID]
}

// RemoteControlEnabled derives the global kill switch from the live set size (Count()>0),
// exactly like production — so revoking ONE of several devices leaves it ON.
func (s *revokeSeverStub) RemoteControlEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.devices) > 0
}

// TerminalTap opens a read-only tap stream and records it, so a test can assert the peek was
// severed (the tap released) on revoke.
func (s *revokeSeverStub) TerminalTap(local string) (SessionStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := newStubStream()
	s.taps = append(s.taps, st)
	return st, nil
}

func (s *revokeSeverStub) lastPeekTap() *stubStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.taps) == 0 {
		return nil
	}
	return s.taps[len(s.taps)-1]
}

// Compile-time proof the stub satisfies every surface the C1 sever path asserts (DeviceRegistrar
// is undefined until GREEN — the undefined-only RED signal for this finding).
var (
	_ DeviceRevoker   = (*revokeSeverStub)(nil)
	_ DeviceRegistrar = (*revokeSeverStub)(nil)
	_ KillSwitch      = (*revokeSeverStub)(nil)
	_ TerminalTapper  = (*revokeSeverStub)(nil)
)

// takeControlDev drives a signed, authorized take_control AS deviceID and returns the OpLease
// grant. Mirrors takeControl (takecontrol_input_test.go) but pins the establishing device id
// so a revoke can target it and the sever can be observed.
func takeControlDev(t *testing.T, rc *rawConn, ep, sid, deviceID, opID string, ttlSeconds int) Control {
	t.Helper()
	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpTakeControl, EndpointID: ep, SessionID: sid,
		OperationID: opID,
		DeviceID:    deviceID, DeviceSig: "sig", ExpiresAt: &exp,
		GateToken:  "gate-tok",
		TTLSeconds: ttlSeconds,
	})
	got := nextControl(t, rc)
	if got.Op != OpLease || got.Generation == 0 {
		t.Fatalf("take_control(%s) = op %q code %q gen %d; want an OpLease grant", deviceID, got.Op, got.ErrorCode, got.Generation)
	}
	return got
}

// TestProtocol_RevokeSeversLiveControlLease: two devices paired; device B establishes a live
// take_control lease; a SECOND device (A) revokes B over the SAME remote Server. The global
// kill switch stays ON (device A remains), so ONLY the per-device sever can stop B. The NEXT
// data_in frame on B's lease must be REFUSED (no shim side effect) AND the lease must be
// released (its upstream stream closed) — NOT merely a future take_control refused.
func TestProtocol_RevokeSeversLiveControlLease(t *testing.T) {
	stub := newRevokeSeverStub("devA", "devB") // two devices: revoking one keeps the global switch ON
	sock := serveRemoteAPI(t, stub)

	// Device B establishes a live control lease over sess1 (the one upstream stream).
	rcB := rawDial(t, sock)
	repB := rcB.hello(Version, []string{CapRemoteGateway})
	sid := repB.EndpointID + "/sess1"
	takeControlDev(t, rcB, repB.EndpointID, sid, "devB", "devB:01JTAKEB0000000000000000", 3600)

	// B's own keystroke reaches the shim FIRST — proves the stream is live and B controls it.
	warm := []byte("echo B\r")
	rcB.writeFrame(wire.TDataIn, warm)
	rcB.writeControl(Control{Op: OpList, EndpointID: repB.EndpointID})
	syncControlOp(t, rcB, OpList)
	st := stub.lastStream()
	if st == nil || !bytes.Contains(st.inputBytes(), warm) {
		t.Fatalf("precondition: controller B's own input did not reach the shim; the lease is not live")
	}

	// Device A revokes device B over the SAME remote Server.
	rcA := rawDial(t, sock)
	repA := rcA.hello(Version, []string{CapRemoteGateway})
	exp := time.Now().Add(time.Minute)
	rcA.writeControl(Control{
		Op: OpDeviceRevoke, EndpointID: repA.EndpointID,
		OperationID:    "devA:01JREVOKEB000000000000",
		DeviceID:       "devA", DeviceSig: "sig", ExpiresAt: &exp,
		TargetDeviceID: "devB",
	})
	if got := nextControl(t, rcA); got.Op != OpOK {
		t.Fatalf("device_revoke(devB) = op %q code %q; want ok", got.Op, got.ErrorCode)
	}
	if !stub.RemoteControlEnabled() {
		t.Fatalf("precondition: the global kill switch went OFF after revoking devB; want it ON (devA remains) so ONLY the per-device sever can stop B")
	}

	// The lease was PROACTIVELY released: its upstream stream is closed.
	if !st.waitClosed(recvTimeout) {
		t.Fatalf("device_revoke did not release the revoked device's live control lease (upstream stream never closed); B keeps its lease for up to maxControlSessionTTL")
	}

	// And B's NEXT keystroke is REFUSED — it does NOT reach the shim.
	after := []byte("rm -rf ~\r")
	rcB.writeFrame(wire.TDataIn, after)
	rcB.writeControl(Control{Op: OpList, EndpointID: repB.EndpointID})
	syncControlOp(t, rcB, OpList)
	if bytes.Contains(st.inputBytes(), after) {
		t.Fatalf("revoked device B's keystroke still reached the shim after revoke; the daemon must sever the live lease: %q", st.inputBytes())
	}
}

// TestProtocol_RevokeSeversLivePeek: a live terminal peek must be severed at the daemon on
// device_revoke. The global kill switch stays ON (device A remains), so the sever must be the
// proactive peek cancellation, not the global switch. Terminal peeks carry no device identity
// (terminal_subscribe is unsigned), so ALL active peeks are cancelled (coarse v1) — other
// devices simply reconnect.
func TestProtocol_RevokeSeversLivePeek(t *testing.T) {
	stub := newRevokeSeverStub("devA", "devB")
	sock := serveRemoteAPI(t, stub)

	// A live terminal peek on sess1 (unsigned read; no take_control needed).
	rcP := rawDial(t, sock)
	repP := rcP.hello(Version, []string{CapRemoteGateway})
	psid := repP.EndpointID + "/sess1"
	rcP.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: repP.EndpointID, SessionID: psid})
	if ack := nextControl(t, rcP); ack.Op != OpOK {
		t.Fatalf("terminal_subscribe = op %q code %q; want OpOK", ack.Op, ack.ErrorCode)
	}
	tap := stub.lastPeekTap()
	if tap == nil {
		t.Fatalf("peek opened no tap")
	}
	tap.frames <- []byte("PEEK")
	_ = readTerminalSnapshot(t, rcP) // the peek is live

	// Device A revokes device B; the global kill switch stays ON (devA remains).
	rcA := rawDial(t, sock)
	repA := rcA.hello(Version, []string{CapRemoteGateway})
	exp := time.Now().Add(time.Minute)
	rcA.writeControl(Control{
		Op: OpDeviceRevoke, EndpointID: repA.EndpointID,
		OperationID:    "devA:01JREVOKEPEEK0000000000",
		DeviceID:       "devA", DeviceSig: "sig", ExpiresAt: &exp,
		TargetDeviceID: "devB",
	})
	if got := nextControl(t, rcA); got.Op != OpOK {
		t.Fatalf("device_revoke = op %q code %q; want ok", got.Op, got.ErrorCode)
	}

	// The peek terminated at the daemon: its read-only tap is released ...
	if !tap.waitClosed(recvTimeout) {
		t.Fatalf("device_revoke did not sever the live terminal peek (its read-only tap was never released)")
	}
	// ... and the peeker is signaled the peek ended (OpError), so its gateway stops reading.
	got := nextControl(t, rcP)
	if got.Op != OpError {
		t.Fatalf("after revoke the peek conn got op %q; want OpError (the peek-ended signal)", got.Op)
	}
}

// TestProtocol_RevokeSeversLeaseViaSeparateServer isolates the PER-KEYSTROKE defense
// (controlGateOpen's device-presence clause): production runs an owner-tier and a remote-tier
// Server that SHARE one backend registry but hold SEPARATE lease maps, so a revoke handled by
// the owner Server cannot proactively reach a lease on the remote Server. The daemon must
// STILL drop the revoked device's keystrokes — via the backend DeviceRegistered re-check on
// every frame, not the proactive release.
func TestProtocol_RevokeSeversLeaseViaSeparateServer(t *testing.T) {
	stub := newRevokeSeverStub("devA", "devB")
	remoteSock := serveRemoteAPI(t, stub)
	ownerSock := serveOwner(t, stub)

	// Device B takes control on the REMOTE Server.
	rcB := rawDial(t, remoteSock)
	repB := rcB.hello(Version, []string{CapRemoteGateway})
	sid := repB.EndpointID + "/sess1"
	takeControlDev(t, rcB, repB.EndpointID, sid, "devB", "devB:01JTAKEBX000000000000000", 3600)

	warm := []byte("echo B\r")
	rcB.writeFrame(wire.TDataIn, warm)
	rcB.writeControl(Control{Op: OpList, EndpointID: repB.EndpointID})
	syncControlOp(t, rcB, OpList)
	st := stub.lastStream()
	if st == nil || !bytes.Contains(st.inputBytes(), warm) {
		t.Fatalf("precondition: B's input did not reach the shim; the lease is not live")
	}

	// Revoke B via the OWNER Server (owner tier => no device signature needed). This removes
	// devB from the SHARED registry but does NOT touch the remote Server's lease map.
	rcO := rawDial(t, ownerSock)
	repO := rcO.hello(Version, []string{CapPairing})
	rcO.writeControl(Control{Op: OpDeviceRevoke, EndpointID: repO.EndpointID, TargetDeviceID: "devB"})
	if got := nextControl(t, rcO); got.Op != OpOK {
		t.Fatalf("owner device_revoke(devB) = op %q code %q; want ok", got.Op, got.ErrorCode)
	}

	// B's NEXT keystroke on the still-open remote lease is dropped by the per-keystroke
	// device-presence re-check, even though the owner Server never touched this lease.
	after := []byte("cat /etc/shadow\r")
	rcB.writeFrame(wire.TDataIn, after)
	rcB.writeControl(Control{Op: OpList, EndpointID: repB.EndpointID})
	syncControlOp(t, rcB, OpList)
	if bytes.Contains(st.inputBytes(), after) {
		t.Fatalf("revoked device B kept injecting keystrokes after an owner-path revoke; the daemon must re-check device presence per keystroke (cross-Server defense): %q", st.inputBytes())
	}
}
