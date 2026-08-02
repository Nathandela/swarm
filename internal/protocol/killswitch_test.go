package protocol

// FAILING-FIRST protocol tests for the remote-control KILL SWITCH enforcement at the
// protocol choke point (plan R-KS.1 / fix-pack item 2a; closes review finding #1 /
// GW-M3 / MED-2). RED: requireRemoteAuthz does not consult a kill switch yet, so a
// disabled switch currently ALLOWS a validly-signed remote op — TestKillSwitch_Off-
// RefusesRemoteOps must fail on that.
//
// The security contract pinned here (the implementer adds it):
//
//	// A new OPTIONAL backend interface, discovered by type-assertion on the remote-tier
//	// Server's DaemonAPI (same seam as JournalBackend / DeviceAuthenticator). When the
//	// backend implements it AND RemoteControlEnabled() returns false, requireRemoteAuthz
//	// refuses EVERY remote-origin mutating op with CodeKillSwitch BEFORE any operation_id
//	// / device-authenticator work — a perfectly valid device signature must NOT bypass it.
//	// When the backend does NOT implement it, behavior is unchanged (fail-open only
//	// against a backend that never opted in; the durable default state is slice 2b).
//	type KillSwitch interface {
//	    RemoteControlEnabled() bool
//	}
//
// Placement: the kill-switch check is the FIRST gate in requireRemoteAuthz, ahead of
// requireOperationID and deviceAuthenticator, so a disabled switch overrides authz. We
// prove the ordering by feeding a VALID signed op (authenticator would accept) and
// asserting it is STILL refused CodeKillSwitch and the authenticator is never consulted.
//
// This slice pins ENFORCEMENT only. The durable remote-state.json file and its default
// lifecycle are a SEPARATE follow-up (slice 2b) and are NOT tested here.

import (
	"testing"
	"time"
)

// killSwitchStub is a remote backend that is a full DaemonAPI + DeviceAuthenticator (via
// the embedded *stubDaemon, whose authzFn accepts by default) AND a toggleable KillSwitch
// (its RemoteControlEnabled reports `enabled`). Because it satisfies both optional
// interfaces, a remote-tier Server type-asserts BOTH off the same backend — exactly the
// production assembly. Assertions read through the embedded stub (killedIDs / authorized-
// Tuples).
type killSwitchStub struct {
	*stubDaemon
	enabled bool
}

// RemoteControlEnabled makes killSwitchStub the pinned KillSwitch: the switch is ON when
// enabled is true, OFF (remote control disabled) when false.
func (k killSwitchStub) RemoteControlEnabled() bool { return k.enabled }

// serveOwner stands up an OWNER (main) tier Server on an arbitrary DaemonAPI, so a
// KillSwitch-bearing backend can be exercised on the local tier (which must ignore it).
func serveOwner(t *testing.T, d DaemonAPI) string {
	t.Helper()
	sock := tmpSock(t)
	srv, err := Serve(d, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// TestKillSwitch_OffRefusesRemoteOps: with the switch DISABLED, a remote mutating op
// (OpKill) carrying a valid operation_id + device fields + a signature the authenticator
// WOULD accept is nonetheless refused error/CodeKillSwitch, the kill does NOT happen, and
// the authenticator is never even consulted — proving the switch precedes and overrides
// the valid-signature path.
func TestKillSwitch_OffRefusesRemoteOps(t *testing.T) {
	stub := newStubDaemon() // authzFn nil => the signature would be accepted
	sock := serveRemoteAPI(t, killSwitchStub{stubDaemon: stub, enabled: false})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JKILL0000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
	})

	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeKillSwitch {
		t.Fatalf("disabled kill switch: valid signed kill = op %q code %q; want error/kill_switch", got.Op, got.ErrorCode)
	}
	if n := len(stub.killedIDs()); n != 0 {
		t.Fatalf("daemon executed %d kills while the kill switch was disabled; want 0", n)
	}
	if n := len(stub.authorizedTuples()); n != 0 {
		t.Fatalf("authenticator consulted %d times while the kill switch was disabled; want 0 (switch must be the FIRST gate)", n)
	}
}

// TestKillSwitch_OnAllowsAuthorizedRemoteOp: with the switch ENABLED, the same op is
// authorized and actioned (authenticator accepts) — no CodeKillSwitch. Guards against a
// too-aggressive gate that would block remote ops even when the switch is on.
func TestKillSwitch_OnAllowsAuthorizedRemoteOp(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemoteAPI(t, killSwitchStub{stubDaemon: stub, enabled: true})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JKILL0000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
	})

	got := rc.readControl()
	if got.Op == OpError {
		t.Fatalf("enabled kill switch refused an authorized kill: %q / %q", got.Error, got.ErrorCode)
	}
	if got.ErrorCode == CodeKillSwitch {
		t.Fatalf("enabled kill switch still returned kill_switch; want the op authorized")
	}
	if n := len(stub.killedIDs()); n != 1 {
		t.Fatalf("daemon executed %d kills with the switch enabled; want 1", n)
	}
}

// TestKillSwitch_OwnerTierUnaffectedWhenDisabled: on the OWNER (main) tier, a backend
// whose kill switch reports DISABLED still runs a local kill — the switch governs only
// the remote tier (R-KS.1: local/owner-tier ops keep unconditional trust). A local kill
// with no device fields and no operation_id is forwarded.
func TestKillSwitch_OwnerTierUnaffectedWhenDisabled(t *testing.T) {
	stub := newStubDaemon()
	sock := serveOwner(t, killSwitchStub{stubDaemon: stub, enabled: false})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, nil)
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid})
	if got := rc.readControl(); got.Op == OpError {
		t.Fatalf("owner-tier local kill refused with kill switch off: %q / %q (kill switch must only govern the remote tier)", got.Error, got.ErrorCode)
	}
	if n := len(stub.killedIDs()); n != 1 {
		t.Fatalf("owner-tier local kill executed %d times; want 1", n)
	}
}

// TestKillSwitch_BackendWithoutSwitchUnchanged: a remote-tier backend that does NOT
// implement KillSwitch behaves exactly as today — a valid signed op is authorized. Proves
// the optional interface is additive and does not break an assembly that never opted in
// (this is why the existing device-sig tests keep passing).
func TestKillSwitch_BackendWithoutSwitchUnchanged(t *testing.T) {
	stub := newStubDaemon() // plain stubDaemon: DeviceAuthenticator yes, KillSwitch no
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JKILL0000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
	})

	got := rc.readControl()
	if got.Op == OpError {
		t.Fatalf("no-KillSwitch backend refused a valid signed kill: %q / %q", got.Error, got.ErrorCode)
	}
	if got.ErrorCode == CodeKillSwitch {
		t.Fatalf("no-KillSwitch backend returned kill_switch; the optional interface must be additive")
	}
	if n := len(stub.killedIDs()); n != 1 {
		t.Fatalf("no-KillSwitch backend executed %d kills; want 1 (unchanged behavior)", n)
	}
}
