package protocol

// R-POL.9 protocol-layer enforcement: the remote-tier choke point that authorizes a
// mutating op via a device signature + capability BEFORE any side effect. These tests
// exercise the choke point with a fake DeviceAuthenticator (accept/reject on demand);
// the real crypto+registry authenticator (signature verification, the R-POL.6
// capability matrix, and expiry against a clock) is wired and tested at the skeleton
// layer in R-POL.9b. The security contract pinned here:
//   - a remote mutating op missing the device identity fields is invalid_field;
//   - an authenticator rejection (forged/expired sig, or insufficient capability) is
//     not_authorized and the op is NOT actioned;
//   - a remote-tier Server whose backend exposes NO DeviceAuthenticator refuses every
//     mutating op (fail-closed against misassembly);
//   - the canonical tuple presented to the authenticator carries the right
//     action/machine/session/operation_id/expiry;
//   - the owner (main) tier is exempt (R-POL.1): local ops keep full trust.

import (
	"errors"
	"testing"
	"time"
)

// errForged stands in for any authenticator rejection (forged/expired signature or a
// device whose capability forbids the action); the protocol layer maps every such
// rejection to not_authorized.
var errForged = errors.New("authenticator rejected the command")

// serveRemoteAPI stands up a remote-tier Server on an arbitrary DaemonAPI (so a
// non-authenticating backend can be supplied for the fail-closed case).
func serveRemoteAPI(t *testing.T, d DaemonAPI) string {
	t.Helper()
	sock := tmpSock(t)
	srv, err := ServeRemote(d, sock)
	if err != nil {
		t.Fatalf("ServeRemote: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// TestProtocol_RemoteMutatingOpRequiresDeviceFields: with operation_id present but the
// device identity fields absent, a remote mutating op is refused invalid_field and
// never reaches the daemon (R-POL.9 structural precondition).
func TestProtocol_RemoteMutatingOpRequiresDeviceFields(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid, OperationID: "devA:01JKILL0000000000000000"})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("remote kill missing device fields = op %q code %q; want error/invalid_field", got.Op, got.ErrorCode)
	}
	if n := len(stub.killedIDs()); n != 0 {
		t.Fatalf("daemon executed %d kills for an unauthorized op; want 0 (refused before action)", n)
	}
}

// TestProtocol_ForgedDeviceCommandRefused: when the authenticator rejects (a forged or
// expired signature, or a device whose capability forbids the action), the op is
// refused not_authorized and never actioned.
func TestProtocol_ForgedDeviceCommandRefused(t *testing.T) {
	stub := newStubDaemon()
	stub.authzFn = func(DeviceCommandAuth) error { return errForged }
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JKILL0000000000000000",
		DeviceID:    "devA", DeviceSig: "forged", ExpiresAt: &exp,
	})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("forged remote kill = op %q code %q; want error/not_authorized", got.Op, got.ErrorCode)
	}
	if n := len(stub.killedIDs()); n != 0 {
		t.Fatalf("daemon executed %d kills for a rejected op; want 0", n)
	}
}

// TestProtocol_RemoteAuthzFailClosedWithoutAuthenticator: a remote-tier Server whose
// backend does NOT implement DeviceAuthenticator refuses every mutating op, even one
// carrying operation_id and all device fields (fail-closed against a misassembled
// remote server that forgot to wire authorization).
func TestProtocol_RemoteAuthzFailClosedWithoutAuthenticator(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemoteAPI(t, daemonOnly{DaemonAPI: stub}) // strips DeviceAuthenticator
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
	if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("mutating op on a no-authenticator remote server = op %q code %q; want error/not_authorized (fail-closed)", got.Op, got.ErrorCode)
	}
	if n := len(stub.killedIDs()); n != 0 {
		t.Fatalf("daemon executed %d kills with no authenticator wired; want 0 (fail-closed)", n)
	}
}

// TestProtocol_AuthorizedCommandForwardedWithTuple: an accepted op is forwarded, and
// the authenticator was presented the correct canonical tuple (action/machine/session/
// operation_id/expiry). This is what the phone-core must sign over.
func TestProtocol_AuthorizedCommandForwardedWithTuple(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub) // stub authenticator accepts by default
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JKILL0000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
	})
	if got := rc.readControl(); got.Op == OpError {
		t.Fatalf("authorized kill refused: %q / %q", got.Error, got.ErrorCode)
	}
	if n := len(stub.killedIDs()); n != 1 {
		t.Fatalf("daemon executed %d kills for an authorized op; want 1", n)
	}

	tuples := stub.authorizedTuples()
	if len(tuples) != 1 {
		t.Fatalf("authenticator saw %d commands; want 1", len(tuples))
	}
	a := tuples[0]
	if a.Action != ActionKill {
		t.Errorf("tuple Action = %q, want %q", a.Action, ActionKill)
	}
	if a.Machine != rep.EndpointID {
		t.Errorf("tuple Machine = %q, want endpoint id %q", a.Machine, rep.EndpointID)
	}
	if a.Session != sid {
		t.Errorf("tuple Session = %q, want %q", a.Session, sid)
	}
	if a.OperationID != "devA:01JKILL0000000000000000" {
		t.Errorf("tuple OperationID = %q, want the sent operation_id", a.OperationID)
	}
	if a.DeviceID != "devA" {
		t.Errorf("tuple DeviceID = %q, want devA", a.DeviceID)
	}
	if !a.ExpiresAt.Equal(exp) {
		t.Errorf("tuple ExpiresAt = %v, want %v", a.ExpiresAt, exp)
	}
}

// TestProtocol_LocalTierSkipsDeviceAuth: on the owner (main) tier a mutating op is NOT
// subject to the device-auth choke point (R-POL.1 local exemption): a local kill with
// no device fields and no operation_id is forwarded.
func TestProtocol_LocalTierSkipsDeviceAuth(t *testing.T) {
	stub := newStubDaemon()
	sock := tmpSock(t)
	srv, err := Serve(stub, sock) // main tier, not remote
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	rc := rawDial(t, sock)
	rep := rc.hello(Version, nil)
	sid := rep.EndpointID + "/sess1"
	rc.writeControl(Control{Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid})
	if got := rc.readControl(); got.Op == OpError {
		t.Fatalf("local kill refused: %q / %q (owner tier must be exempt from device auth)", got.Error, got.ErrorCode)
	}
	if n := len(stub.killedIDs()); n != 1 {
		t.Fatalf("local kill executed %d times; want 1", n)
	}
	if n := len(stub.authorizedTuples()); n != 0 {
		t.Fatalf("local tier invoked the device authenticator %d times; want 0 (exempt)", n)
	}
}
