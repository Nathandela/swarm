package protocol

// FAILING-FIRST protocol tests for the take_control op — slice A5-a (ESTABLISHMENT +
// AUTHORIZATION only). take_control is a signed, mutating, REMOTE-tier op: it runs
// through the SAME requireRemoteAuthz choke point every remote mutating op uses
// (kill/launch/delete), and on success establishes a controller lease through the
// SAME s.attach path handleAttach uses on the owner tier. It is the anti-abuse gate
// that must precede any remote keystroke reaching a code-editing agent, so the
// security assertions here are the point:
//
//   - an AUTHORIZED take_control opens a lease (an OpLease grant carrying a nonzero
//     generation) AND opens exactly one upstream stream (a lease established);
//   - an authenticator REJECTION refuses take_control not_authorized and opens NO
//     lease (no upstream stream);
//   - a DISABLED kill switch refuses take_control CodeKillSwitch BEFORE any lease and
//     before the authenticator is even consulted (the switch is the first gate).
//
// SCOPE (A5-a is establishment + authz ONLY): these tests deliberately do NOT assert
// input forwarding. Reopening the OpDataIn / OpResize input path under a take_control
// lease is a LATER slice (A5-b), and gate-token single-use is A5-c. Here we assert only
// that an authorized take_control opens a lease and an unauthorized / kill-switched one
// does not.
//
// RED is undefined-only: OpTakeControl (internal/protocol/types.go) and
// ActionTakeControl (internal/protocol/remote.go) do not exist yet, so this file fails
// to compile until the implementer defines them and dispatches handleTakeControl from
// handleControl. No assertion runs until those production symbols exist.
//
// Harness reuse (all defined in sibling _test.go files, package protocol):
//   - serveRemote / serveRemoteAPI stand up a REMOTE-tier Server;
//   - newStubDaemon's authzFn seam forces the DeviceAuthenticator to accept (nil) or
//     reject (errForged), and streamCount() proves whether an upstream lease was opened;
//   - killSwitchStub wraps the stub as a toggleable KillSwitch (enabled:false = off);
//   - nextControl skips the snapshot/live frames an attach pump interleaves and returns
//     the first control frame (the OpLease grant on success, OpError on refusal).

import (
	"testing"
	"time"
)

// TestProtocol_TakeControlEstablishesLeaseUnderAuthz: on the remote tier, an authorized
// take_control (the stub authenticator accepts) is granted a controller lease — the
// reply is an OpLease carrying a nonzero generation — and exactly one upstream stream is
// opened (the lease established through s.attach). The choke point is consulted with a
// take_control-action tuple bound to the target session, proving take_control goes
// through requireRemoteAuthz like every other remote mutating op.
func TestProtocol_TakeControlEstablishesLeaseUnderAuthz(t *testing.T) {
	stub := newStubDaemon() // authzFn nil => the signed command is accepted
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpTakeControl, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JTAKE0000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
		// The stub backend is an OperationClaimer, so take_control now requires a gate token
		// (present-check before authz); a non-empty token satisfies it.
		GateToken: "gate-tok",
	})

	got := nextControl(t, rc)
	if got.Op != OpLease {
		t.Fatalf("authorized take_control = op %q code %q; want a lease grant (OpLease)", got.Op, got.ErrorCode)
	}
	if got.Generation == 0 {
		t.Fatalf("take_control lease generation = 0; want a nonzero lease generation (lease established)")
	}
	if n := stub.streamCount(); n != 1 {
		t.Fatalf("authorized take_control opened %d upstream streams; want 1 (a lease established through s.attach)", n)
	}

	// take_control was authorized through requireRemoteAuthz with the right tuple.
	tuples := stub.authorizedTuples()
	if len(tuples) != 1 {
		t.Fatalf("authenticator saw %d commands for take_control; want 1 (must pass through the choke point)", len(tuples))
	}
	if tuples[0].Action != ActionTakeControl {
		t.Errorf("take_control tuple Action = %q, want %q", tuples[0].Action, ActionTakeControl)
	}
	if tuples[0].Machine != rep.EndpointID {
		t.Errorf("take_control tuple Machine = %q, want endpoint id %q", tuples[0].Machine, rep.EndpointID)
	}
	if tuples[0].Session != sid {
		t.Errorf("take_control tuple Session = %q, want %q", tuples[0].Session, sid)
	}
}

// TestProtocol_TakeControlRejectedByAuthzOpensNoLease: when the authenticator rejects
// (a forged/expired signature, or a device whose capability forbids control), the remote
// take_control is refused not_authorized and NO lease is established — no upstream stream
// is opened. This is the anti-abuse property: an unauthorized take_control must never
// reach a lease that could later carry keystrokes.
func TestProtocol_TakeControlRejectedByAuthzOpensNoLease(t *testing.T) {
	stub := newStubDaemon()
	stub.authzFn = func(DeviceCommandAuth) error { return errForged }
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpTakeControl, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JTAKE0000000000000000",
		DeviceID:    "devA", DeviceSig: "forged", ExpiresAt: &exp,
		// Gate token present (the stub is an OperationClaimer) so the present-check passes and
		// the refusal comes from the authenticator rejection under test, not a missing token.
		GateToken: "gate-tok",
	})

	got := nextControl(t, rc)
	if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("rejected take_control = op %q code %q; want error/not_authorized", got.Op, got.ErrorCode)
	}
	if n := stub.streamCount(); n != 0 {
		t.Fatalf("rejected take_control opened %d upstream streams; want 0 (no lease may be established on refusal)", n)
	}
}

// TestProtocol_TakeControlKillSwitchOff: with the remote-control kill switch DISABLED, a
// take_control carrying a valid operation_id + device fields + a signature the
// authenticator WOULD accept is nonetheless refused error/CodeKillSwitch, opens NO lease
// (no upstream stream), and the authenticator is never even consulted — proving the kill
// switch precedes and overrides the take_control lease path, exactly as it does for kill
// (killswitch_test.go). A disabled switch must stop remote control before any lease.
func TestProtocol_TakeControlKillSwitchOff(t *testing.T) {
	stub := newStubDaemon() // authzFn nil => the signature would be accepted
	sock := serveRemoteAPI(t, killSwitchStub{stubDaemon: stub, enabled: false})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpTakeControl, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JTAKE0000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
		// Gate token present (the stub is an OperationClaimer) so the present-check passes and
		// the refusal comes from the kill switch under test — proving it gates before authz.
		GateToken: "gate-tok",
	})

	got := nextControl(t, rc)
	if got.Op != OpError || got.ErrorCode != CodeKillSwitch {
		t.Fatalf("kill-switch-off take_control = op %q code %q; want error/kill_switch", got.Op, got.ErrorCode)
	}
	if n := stub.streamCount(); n != 0 {
		t.Fatalf("kill-switch-off take_control opened %d upstream streams; want 0 (switch refuses before any lease)", n)
	}
	if n := len(stub.authorizedTuples()); n != 0 {
		t.Fatalf("authenticator consulted %d times with the kill switch off; want 0 (switch must be the FIRST gate)", n)
	}
}
