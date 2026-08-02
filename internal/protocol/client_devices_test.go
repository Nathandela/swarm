package protocol

// FAILING-FIRST tests for slice A4-cli: the protocol.Client methods the
// `swarm remote devices` / `swarm remote revoke` CLI subcommands need. The wire ops
// (device_list/device_revoke) already exist and are already tested end to end at the
// wire level (remote_controlplane_test.go, remote_devicerevoke_test.go) — this file
// pins only the CLIENT-side convenience methods, exercised through the SAME
// serve+dial harness the existing Client tests use (harness_test.go's serveStub/
// dialClient for the plumbing shape, plus remote_controlplane_test.go's
// deviceListStub/serveDeviceLister and remote_devicerevoke_test.go's
// recordingRevoker, reused here rather than duplicated).
//
// FROZEN API this file expects (the GREEN implementer adds both, mirroring the
// existing List()/simpleOp() shape in client.go):
//
//	// ListDevices sends Control{Op: OpDeviceList, EndpointID: c.endpointID} and
//	// returns the reply's Devices (mirrors List()'s Sessions round trip).
//	func (c *Client) ListDevices() ([]DeviceView, error)
//
//	// RevokeDevice sends Control{Op: OpDeviceRevoke, EndpointID: c.endpointID,
//	// TargetDeviceID: targetID}; nil on an OK reply, the daemon's error on an error
//	// reply (mirrors simpleOp(), but simpleOp itself cannot be reused as-is: it sets
//	// SessionID, not TargetDeviceID).
//	func (c *Client) RevokeDevice(targetID string) error
//
// Both are tested at the OWNER tier (Serve, not ServeRemote): requireRemoteAuthz is a
// no-op there (R-POL.9's remote-tier gate), so a local CLI needs no device
// signature/operation_id — it just sends the op, exactly like Kill/Delete today.
//
// RED today: ListDevices/RevokeDevice do not exist, so this file does not compile —
// an acceptable compile-fail RED for a new API, unambiguous by name.

import (
	"errors"
	"testing"
	"time"
)

// serveOwnerAPI stands up an OWNER-tier Server (Serve, not ServeRemote) over an
// arbitrary DaemonAPI backend, same shape as serveDeviceLister/serveStub. Named
// distinctly from serveDeviceLister (remote_controlplane_test.go) only so this
// file's intent — device_revoke, not device_list — reads clearly at the call site;
// it is otherwise the identical one-line Serve wrapper.
func serveOwnerAPI(t *testing.T, backend DaemonAPI) string {
	t.Helper()
	sock := tmpSock(t)
	srv, err := Serve(backend, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// erroringRevoker is a DaemonAPI (via the embedded *stubDaemon) that ALSO implements
// DeviceRevoker, but always fails — so TestClient_RevokeDevice_ErrorReply can prove
// RevokeDevice surfaces the daemon's error rather than swallowing it.
type erroringRevoker struct {
	*stubDaemon
}

func (e erroringRevoker) RevokeDevice(deviceID string) (bool, error) {
	return false, errors.New("device not found")
}

var _ DeviceRevoker = erroringRevoker{}

// TestClient_ListDevices pins ListDevices()'s happy path: a device_list reply's
// Devices carries through to the Client's return value, in order, every field intact.
func TestClient_ListDevices(t *testing.T) {
	pairedA := time.Now().Add(-time.Hour).Truncate(time.Second)
	pairedB := pairedA.Add(time.Minute)
	stub := newStubDaemon()
	backend := deviceListStub{
		stubDaemon: stub,
		devices: []DeviceView{
			{DeviceID: "devA", Name: "Nathan's iPhone", Capability: "full", PairedAt: pairedA},
			{DeviceID: "devB", Name: "Nathan's iPad", Capability: "read_only", PairedAt: pairedB},
		},
	}
	sock := serveDeviceLister(t, backend)
	c := dialClient(t, sock, []string{CapPairing})

	got, err := c.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListDevices returned %d devices, want 2", len(got))
	}
	if got[0].DeviceID != "devA" || got[0].Name != "Nathan's iPhone" ||
		got[0].Capability != "full" || !got[0].PairedAt.Equal(pairedA) {
		t.Errorf("ListDevices[0] = %+v; want devA/Nathan's iPhone/full/%v", got[0], pairedA)
	}
	if got[1].DeviceID != "devB" || got[1].Name != "Nathan's iPad" ||
		got[1].Capability != "read_only" || !got[1].PairedAt.Equal(pairedB) {
		t.Errorf("ListDevices[1] = %+v; want devB/Nathan's iPad/read_only/%v", got[1], pairedB)
	}
}

// TestClient_RevokeDevice pins RevokeDevice()'s happy path: it returns nil AND the
// daemon-side backend saw exactly the target id — proving RevokeDevice carries
// TargetDeviceID (not SessionID or DeviceID) on the wire.
func TestClient_RevokeDevice(t *testing.T) {
	stub := newRecordingRevoker()
	sock := serveOwnerAPI(t, stub)
	c := dialClient(t, sock, nil)

	if err := c.RevokeDevice("devB"); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	revoked := stub.revokedIDs()
	if len(revoked) != 1 || revoked[0] != "devB" {
		t.Fatalf("daemon-side RevokeDevice calls = %v, want [devB]", revoked)
	}
}

// TestClient_RevokeDevice_ErrorReply pins that a daemon-side device_revoke failure
// surfaces as a non-nil error from RevokeDevice, rather than being swallowed.
func TestClient_RevokeDevice_ErrorReply(t *testing.T) {
	stub := erroringRevoker{stubDaemon: newStubDaemon()}
	sock := serveOwnerAPI(t, stub)
	c := dialClient(t, sock, nil)

	if err := c.RevokeDevice("devX"); err == nil {
		t.Fatal("RevokeDevice: err = nil, want non-nil on an error reply")
	}
}
