package protocol

// FAILING-FIRST protocol tests for the MUTATING remote-tier op device_revoke (slice
// A3.2): removes a paired device from the daemon's device registry. It mirrors the
// existing kill/delete mutating-op shape (R-POL.9 requireRemoteAuthz choke point),
// with one critical wrinkle: the device being revoked (the TARGET) is a distinct
// wire field from the CALLER's own authenticating device id (Control.DeviceID, used
// to look up the caller's command-signing key). Reusing DeviceID for both would make
// a device only able to revoke itself -- TestProtocol_DeviceRevokeRemovesTargetNotCaller
// exists specifically to catch that regression. RED is undefined-only: this file does
// not compile today (OpDeviceRevoke / ActionDeviceRevoke / Control.TargetDeviceID /
// DeviceRevoker do not exist yet).
//
// FROZEN API this file expects (the GREEN implementer adds all of it):
//
//	const OpDeviceRevoke = "device_revoke"
//	const ActionDeviceRevoke = "device_revoke"  // internal/protocol/remote.go, alongside ActionKill/ActionLaunch/ActionDelete
//
//	// Control gains one additive, omitempty carrier: the device to REVOKE. Distinct
//	// from DeviceID (the caller's own authenticating device).
//	//   TargetDeviceID string `json:"target_device_id,omitempty"`
//
//	// DeviceRevoker is the optional interface a DaemonAPI implements to expose
//	// device_revoke (backed by device.Registry.Remove in production).
//	type DeviceRevoker interface {
//	    RevokeDevice(deviceID string) (bool, error)
//	}
//
// Handler shape (mirrors handleKill): requireRemoteAuthz(c, ActionDeviceRevoke,
// c.TargetDeviceID, nil) -- note the resource arg is the TARGET, so the caller's
// signature binds the target -- then RevokeDevice(c.TargetDeviceID), then replyOK.
//
// Auto-off on last-device revoke needs NO new code: RemoteControlEnabled already
// derives from device Count()>0 elsewhere, so a revoke that empties the registry
// flips remote control off as a side effect (TestProtocol_DeviceRevokeAutoOff proves
// it end to end through the protocol layer, backed by a registry-shaped stub).

import (
	"sync"
	"testing"
	"time"
)

// recordingRevoker is a DaemonAPI (via the embedded *stubDaemon, so it is ALSO a
// DeviceAuthenticator) that additionally implements the expected DeviceRevoker,
// recording every device id RevokeDevice was called with so a test can assert
// exactly which device was removed.
type recordingRevoker struct {
	*stubDaemon
	mu      sync.Mutex
	revoked []string
}

func newRecordingRevoker() *recordingRevoker {
	return &recordingRevoker{stubDaemon: newStubDaemon()}
}

// Compile-time proof both stubs satisfy the expected DeviceRevoker (undefined until
// implemented; mirrors the DeviceLister compile-time proof in
// remote_controlplane_test.go).
var (
	_ DeviceRevoker = (*recordingRevoker)(nil)
	_ DeviceRevoker = (*registryRevoker)(nil)
)

func (r *recordingRevoker) RevokeDevice(deviceID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked = append(r.revoked, deviceID)
	return true, nil
}

func (r *recordingRevoker) revokedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.revoked...)
}

// registryRevoker is a DaemonAPI (via the embedded *stubDaemon) that behaves like the
// real device.Registry for the purposes of this slice: RevokeDevice removes from an
// in-memory device set, and RemoteControlEnabled (making it ALSO a KillSwitch) is
// derived live from that set's size -- exactly the production wiring
// (RemoteControlEnabled := Count()>0), so revoking the last device is proven to flip
// the switch off with no extra code, driven end to end through the protocol layer.
type registryRevoker struct {
	*stubDaemon
	mu      sync.Mutex
	devices map[string]bool
}

func newRegistryRevoker(deviceIDs ...string) *registryRevoker {
	m := make(map[string]bool, len(deviceIDs))
	for _, id := range deviceIDs {
		m[id] = true
	}
	return &registryRevoker{stubDaemon: newStubDaemon(), devices: m}
}

func (r *registryRevoker) RevokeDevice(deviceID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.devices[deviceID] {
		return false, nil
	}
	delete(r.devices, deviceID)
	return true, nil
}

func (r *registryRevoker) RemoteControlEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.devices) > 0
}

// TestProtocol_DeviceRevokeRemovesTargetNotCaller is THE field-collision guard: an
// authorized device_revoke where the CALLER is device "devA" (its signing key is what
// the authenticator verifies against) and TargetDeviceID is "devB" (the device to
// remove). RevokeDevice must be called with the TARGET "devB", never the caller
// "devA" -- and the tuple presented to the authenticator must carry DeviceID=devA
// (the signer) but Session=devB (the signed resource), proving the caller's
// signature binds the TARGET, not itself. This is the whole reason TargetDeviceID is
// a separate field from DeviceID.
func TestProtocol_DeviceRevokeRemovesTargetNotCaller(t *testing.T) {
	stub := newRecordingRevoker() // authzFn nil => the signature is accepted
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpDeviceRevoke, EndpointID: rep.EndpointID,
		OperationID:    "devA:01JREVOKE00000000000TGT1",
		DeviceID:       "devA", // the CALLER (authenticating device)
		DeviceSig:      "sig",
		ExpiresAt:      &exp,
		TargetDeviceID: "devB", // the device being REVOKED
	})
	got := rc.readControl()
	if got.Op == OpError {
		t.Fatalf("authorized device_revoke refused: %q / %q", got.Error, got.ErrorCode)
	}

	revoked := stub.revokedIDs()
	if len(revoked) != 1 {
		t.Fatalf("RevokeDevice called %d times; want 1", len(revoked))
	}
	if revoked[0] != "devB" {
		t.Fatalf("RevokeDevice called with %q; want the TARGET %q -- NOT the caller %q (field-collision bug)", revoked[0], "devB", "devA")
	}

	tuples := stub.authorizedTuples()
	if len(tuples) != 1 {
		t.Fatalf("authenticator saw %d commands; want 1", len(tuples))
	}
	a := tuples[0]
	if a.Action != ActionDeviceRevoke {
		t.Errorf("tuple Action = %q, want %q", a.Action, ActionDeviceRevoke)
	}
	if a.DeviceID != "devA" {
		t.Errorf("tuple DeviceID (caller/signer) = %q, want %q", a.DeviceID, "devA")
	}
	if a.Session != "devB" {
		t.Errorf("tuple Session (the signed resource) = %q, want the TARGET %q -- the caller's signature must bind the target, not itself", a.Session, "devB")
	}
}

// TestProtocol_DeviceRevokeAutoOff: a registry-backed stub with exactly ONE device;
// send an authorized device_revoke for that device; the reply is OK and
// RemoteControlEnabled() is false afterward. Auto-off needs no dedicated code path --
// it falls out of RemoteControlEnabled already deriving from Count()>0 -- so this
// proves that composition holds end to end through the protocol layer.
func TestProtocol_DeviceRevokeAutoOff(t *testing.T) {
	stub := newRegistryRevoker("devA")
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	if !stub.RemoteControlEnabled() {
		t.Fatalf("precondition: RemoteControlEnabled() = false before revoking the only device; want true")
	}

	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpDeviceRevoke, EndpointID: rep.EndpointID,
		OperationID:    "devA:01JREVOKE00000000AUTOOFF1",
		DeviceID:       "devA",
		DeviceSig:      "sig",
		ExpiresAt:      &exp,
		TargetDeviceID: "devA",
	})
	got := rc.readControl()
	if got.Op != OpOK {
		t.Fatalf("device_revoke of the last device = op %q code %q; want ok", got.Op, got.ErrorCode)
	}
	if stub.RemoteControlEnabled() {
		t.Fatalf("RemoteControlEnabled() = true after revoking the last device; want false (auto-off)")
	}
}

// TestProtocol_DeviceRevokeRequiresDeviceAuth mirrors
// TestProtocol_RemoteMutatingOpRequiresDeviceFields exactly: with operation_id
// present but the device identity fields absent, a remote device_revoke is refused
// invalid_field and never reaches the daemon.
func TestProtocol_DeviceRevokeRequiresDeviceAuth(t *testing.T) {
	stub := newRecordingRevoker()
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(Control{
		Op: OpDeviceRevoke, EndpointID: rep.EndpointID,
		OperationID:    "devA:01JREVOKE0000000000NOAUTH",
		TargetDeviceID: "devB",
	})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("remote device_revoke missing device fields = op %q code %q; want error/invalid_field", got.Op, got.ErrorCode)
	}
	if n := len(stub.revokedIDs()); n != 0 {
		t.Fatalf("daemon executed %d device revokes for an unauthorized op; want 0 (refused before action)", n)
	}
}
