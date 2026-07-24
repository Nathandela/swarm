package protocol

// FAILING-FIRST (TDD RED) test for the ROUND-5 re-audit REGRESSION at the protocol layer
// (codex#2 + opus#1): device.Registry.Remove now returns (true, err) on a POST-RENAME dir-fsync
// failure -- the device is DURABLY removed, but the trailing dir-fsync errored. handleDeviceRevoke
// early-returned on err!=nil (replyError) BEFORE severRevokedDeviceControl, so a committed-but-
// fsync-failed revoke left the revoked device's LIVE control lease + peek intact. Since the device
// IS revoked, the sever must run REGARDLESS of the durability error, and the reply is OK.
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
// still severs B's live lease and replies OK -- the device is durably revoked, so a trailing
// dir-fsync error must NOT skip the sever nor turn the reply into an error.
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
	// The device IS durably revoked: the reply must be OK, NOT an error, despite the dir-fsync error.
	if got := nextControl(t, rcA); got.Op != OpOK {
		t.Fatalf("device_revoke(devB) with a committed durability error = op %q code %q; want OpOK "+
			"(the device is durably revoked; a dir-fsync error must not turn the revoke into a failure)", got.Op, got.ErrorCode)
	}

	// And the live lease was PROACTIVELY severed: its upstream stream is closed.
	if !st.waitClosed(recvTimeout) {
		t.Fatal("Finding 1 (round-5 REGRESSION): device_revoke did NOT sever the revoked device's live control lease " +
			"when RevokeDevice returned a committed (removed=true) durability error; the early-return skipped the sever")
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
