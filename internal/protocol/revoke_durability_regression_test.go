package protocol

// TDD test for the revoke-durability path at the protocol layer. device.Registry.Remove returns
// (true, err) on a POST-RENAME dir-fsync failure -- the device is DURABLY removed, but the trailing
// dir-fsync errored. handleDeviceRevoke must SEVER the revoked device's LIVE control lease + peek
// whenever removed==true (the security property), REGARDLESS of the durability error.
//
// Round-6 (codex#3 + sonnet#2, CONSENSUS): the round-5 fix replied OpOK and SWALLOWED that error.
// The handler must instead SURFACE it -- sever on removed==true, then reply error if err != nil.
// internal/protocol has no logger, so a swallowed error is otherwise invisible; this mirrors
// round-3's TestRevokeDevice_SurfacesGrantDeleteError requirement (the grant.Delete error too must
// be surfaced). The device is revoked + severed, so the client's idempotent retry is harmless.
//
// Reuses the same-package harness (serveRemoteAPI, rawDial, takeControlDev, nextControl, wire).

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

// revokeCommittedErrStub is a remote-tier backend whose RevokeDevice returns (true, err): a
// committed removal (the device IS gone from the live set) that ALSO surfaces a trailing durability
// (dir-fsync) error -- exactly device.Registry.Remove's post-rename path. It is otherwise the
// production coreAPI shape (DeviceRevoker + DeviceRegistrar + KillSwitch off one live device set).
type revokeCommittedErrStub struct {
	*stubDaemon
	mu      sync.Mutex
	devices map[string]bool
}

func newRevokeCommittedErrStub(ids ...string) *revokeCommittedErrStub {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return &revokeCommittedErrStub{stubDaemon: newStubDaemon(), devices: m}
}

// RevokeDevice removes deviceID (committed) but returns a non-nil durability error alongside
// removed=true, mirroring the registry's post-rename dir-fsync failure.
func (s *revokeCommittedErrStub) RevokeDevice(deviceID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.devices[deviceID] {
		return false, nil
	}
	delete(s.devices, deviceID)
	return true, errors.New("devices dir fsync failed after rename")
}

func (s *revokeCommittedErrStub) DeviceRegistered(deviceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devices[deviceID]
}

func (s *revokeCommittedErrStub) RemoteControlEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.devices) > 0
}

var (
	_ DeviceRevoker   = (*revokeCommittedErrStub)(nil)
	_ DeviceRegistrar = (*revokeCommittedErrStub)(nil)
	_ KillSwitch      = (*revokeCommittedErrStub)(nil)
)

// TestProtocol_RevokeSeversLiveLease_OnCommittedDurabilityError: device B holds a live control
// lease; device A revokes B and the backend reports (removed=true, durabilityErr). The revoke
// STILL severs B's live lease (the device is durably revoked) AND surfaces the durability error as
// the reply (round-6): a trailing dir-fsync error must not skip the sever, and must not be swallowed.
func TestProtocol_RevokeSeversLiveLease_OnCommittedDurabilityError(t *testing.T) {
	stub := newRevokeCommittedErrStub("devA", "devB")
	sock := serveRemoteAPI(t, stub)

	// Device B establishes a live control lease over sess1 (the one upstream stream).
	rcB := rawDial(t, sock)
	repB := rcB.hello(Version, []string{CapRemoteGateway})
	sid := repB.EndpointID + "/sess1"
	takeControlDev(t, rcB, repB.EndpointID, sid, "devB", "devB:01JTAKEBDUR00000000000", 3600)

	// B's own keystroke reaches the shim FIRST -- proves the lease is live.
	warm := []byte("echo B\r")
	rcB.writeFrame(wire.TDataIn, warm)
	rcB.writeControl(Control{Op: OpList, EndpointID: repB.EndpointID})
	syncControlOp(t, rcB, OpList)
	st := stub.lastStream()
	if st == nil || !bytes.Contains(st.inputBytes(), warm) {
		t.Fatalf("precondition: controller B's own input did not reach the shim; the lease is not live")
	}

	// Device A revokes device B; the backend commits the removal but returns a durability error.
	rcA := rawDial(t, sock)
	repA := rcA.hello(Version, []string{CapRemoteGateway})
	exp := time.Now().Add(time.Minute)
	rcA.writeControl(Control{
		Op: OpDeviceRevoke, EndpointID: repA.EndpointID,
		OperationID:    "devA:01JREVOKEBDUR0000000000",
		DeviceID:       "devA", DeviceSig: "sig", ExpiresAt: &exp,
		TargetDeviceID: "devB",
	})
	// Finding 3 (round-6, codex#3 + sonnet#2): the device IS durably revoked AND severed, but the
	// trailing durability error must be SURFACED, not swallowed (there is no logger in
	// internal/protocol, so a swallowed error is invisible; round-3's grant.Delete-error surfacing
	// regressed the same way). The reply is now an ERROR carrying the durability failure; the client's
	// idempotent retry is harmless because the device is already revoked + severed.
	if got := nextControl(t, rcA); got.Op != OpError {
		t.Fatalf("device_revoke(devB) with a committed durability error = op %q code %q; want OpError "+
			"(the device is revoked+severed, but the durability error must be surfaced, not swallowed)", got.Op, got.ErrorCode)
	}

	// And the live lease was STILL PROACTIVELY severed despite the surfaced error: its upstream
	// stream is closed. This security property must NOT weaken -- the device is revoked, so its live
	// lease/peek must die regardless of the reply code.
	if !st.waitClosed(recvTimeout) {
		t.Fatal("Finding 3 (round-6): device_revoke did NOT sever the revoked device's live control lease " +
			"when RevokeDevice returned a committed (removed=true) durability error; the sever must fire on removed==true")
	}

	// B's NEXT keystroke is REFUSED -- it does not reach the shim.
	after := []byte("rm -rf ~\r")
	rcB.writeFrame(wire.TDataIn, after)
	rcB.writeControl(Control{Op: OpList, EndpointID: repB.EndpointID})
	syncControlOp(t, rcB, OpList)
	if bytes.Contains(st.inputBytes(), after) {
		t.Fatalf("revoked device B's keystroke still reached the shim after a committed-durability-error revoke: %q", st.inputBytes())
	}
}
