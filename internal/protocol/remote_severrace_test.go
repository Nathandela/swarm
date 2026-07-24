package protocol

// FAILING-FIRST security test for re-audit FINDING A (concurrency): a sever (off/revoke)
// that races an in-flight take_control must not leave a silently-usable lease.
//
// take_control AUTHORIZES, then establishes the lease + PUBLISHES cc.control. If a blanket
// SeverAllRemoteControl runs in that window it snapshots the live leases BEFORE cc.control
// exists, so its release misses the escaping lease. The kill switch (clause 1) then merely
// PAUSES that lease while off — turning it back ON resumes control with NO fresh take_control
// (no new biometric gate). The OFF variant is undefended: `off` does not remove the device,
// so controlGateOpen clause 4 (device registered) still passes.
//
// Fix: take_control captures a monotonic sever generation BEFORE authz and re-checks it AFTER
// publishing cc.control (under ctlMu, the same lock the sever clears cc.control under). A
// concurrent sever advanced the generation, so take_control FAILS CLOSED — it releases the
// just-established lease and leaves cc.control nil — and a fresh take_control is required.

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

// TestProtocol_TakeControlRacingSeverFailsClosed drives the exact interleaving: a blanket
// sever lands DURING take_control's authorization — after authz passes but BEFORE the lease
// and cc.control are published. authzFn runs on the serve goroutine inside requireRemoteAuthz
// (before attach), so invoking the sever there reproduces the snapshot-misses-the-lease window
// deterministically. The kill switch stays ON throughout, so the ONLY thing that can stop a
// silent resume is the lease being failed-closed by the post-publish re-check.
func TestProtocol_TakeControlRacingSeverFailsClosed(t *testing.T) {
	stub := newStubDaemon() // KillSwitch reports ON; OperationClaimer + DeviceAuthenticator present
	sock, srv := serveRemoteAPISrv(t, stub)

	var fired atomic.Bool
	stub.authzFn = func(DeviceCommandAuth) error {
		if fired.CompareAndSwap(false, true) {
			srv.SeverAllRemoteControl() // an `off` sever landing between authz and publish
		}
		return nil
	}

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpTakeControl, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JTAKE0000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
		GateToken: "gate-tok", TTLSeconds: 3600,
	})

	// A keystroke on the (possibly-escaped) lease MUST NOT reach the shim: the racing sever
	// requires a FRESH take_control before control resumes. syncControlOp(OpList) proves the
	// data_in was fully handled before the assertion (one in-order serve loop per connection).
	inject := []byte("rm -rf ~\r")
	rc.writeFrame(wire.TDataIn, inject)
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	st := stub.lastStream()
	if st == nil {
		t.Fatalf("take_control opened no upstream stream; the race precondition did not hold")
	}
	if bytes.Contains(st.inputBytes(), inject) {
		t.Fatalf("a take_control that raced a concurrent sever left a silently-usable lease: "+
			"a keystroke reached the shim with NO fresh take_control: %q", st.inputBytes())
	}
}
