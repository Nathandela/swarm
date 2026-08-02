package protocol

// FAILING-FIRST protocol tests for the two control-plane READ ops of remote slice
// A3.1: device_list (the paired-device roster) and policy_query (the machine's
// remote launch policy, i.e. its allowed cwd roots). Both are NON-mutating — no
// requireRemoteAuthz choke point — and are gated purely by the negotiated
// capability plus an optional backend interface, exactly the existing
// handleJournalRead / journalBackend() pattern (cap check -> type-assert the
// backend -> serve). RED is undefined-only: these tests do not compile today.
//
// FROZEN API these tests expect (the GREEN implementer adds all of it):
//
//	const OpDeviceList = "device_list"
//	const OpPolicyQuery = "policy_query"
//
//	// Control gains two additive, omitempty carriers.
//	//   Devices []DeviceView `json:"devices,omitempty"`
//	//   Policy  *PolicyView  `json:"policy,omitempty"`
//
//	type DeviceView struct {
//	    DeviceID   string    `json:"device_id"`
//	    Name       string    `json:"name"`
//	    Capability string    `json:"capability"`
//	    PairedAt   time.Time `json:"paired_at"`
//	}
//	type PolicyView struct {
//	    AllowedCwdRoots []string `json:"allowed_cwd_roots"`
//	}
//
//	// DeviceLister is the optional interface a DaemonAPI implements to expose
//	// device_list (backed by device.Registry.List() in production); the Server
//	// serves device_list only when the `pairing` capability was negotiated AND the
//	// backend implements this (mirrors journalBackend()'s cap+type-assert gate).
//	type DeviceLister interface {
//	    ListDevices() []DeviceView
//	}
//
//	// PolicyDescriber is the optional interface a DaemonAPI implements to expose
//	// policy_query (backed by the remote launch policy's configured roots); gated
//	// the same way on the `policy` capability.
//	type PolicyDescriber interface {
//	    DescribePolicy() PolicyView
//	}
//
// Neither op touches requireRemoteAuthz: they are reads, so no operation_id/device
// signature/kill-switch gate applies (mirrors journal_read, not kill/launch/delete).

import (
	"testing"
	"time"
)

// deviceListStub is a DaemonAPI (via the embedded *stubDaemon) that ALSO
// implements the expected DeviceLister, so the Server exposes device_list over it.
type deviceListStub struct {
	*stubDaemon
	devices []DeviceView
}

func (d deviceListStub) ListDevices() []DeviceView { return d.devices }

// Compile-time proof the stub satisfies both surfaces (undefined until implemented).
var (
	_ DaemonAPI    = deviceListStub{}
	_ DeviceLister = deviceListStub{}
)

// serveDeviceLister stands up a Server over a device-listing DaemonAPI. The Server
// is expected to expose device_list when its backend also implements DeviceLister
// (optional-interface assertion), so passing it through the existing Serve entry
// point is enough — same shape as serveJournal.
func serveDeviceLister(t *testing.T, backend DaemonAPI) string {
	t.Helper()
	sock := tmpSock(t)
	srv, err := Serve(backend, sock)
	if err != nil {
		t.Fatalf("Serve(deviceLister): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// policyStub is a DaemonAPI (via the embedded *stubDaemon) that ALSO implements
// the expected PolicyDescriber, so the Server exposes policy_query over it.
type policyStub struct {
	*stubDaemon
	roots []string
}

func (p policyStub) DescribePolicy() PolicyView {
	return PolicyView{AllowedCwdRoots: p.roots}
}

// Compile-time proof the stub satisfies both surfaces (undefined until implemented).
var (
	_ DaemonAPI       = policyStub{}
	_ PolicyDescriber = policyStub{}
)

// servePolicyDescriber stands up a Server over a policy-describing DaemonAPI, same
// shape as serveDeviceLister/serveJournal.
func servePolicyDescriber(t *testing.T, backend DaemonAPI) string {
	t.Helper()
	sock := tmpSock(t)
	srv, err := Serve(backend, sock)
	if err != nil {
		t.Fatalf("Serve(policyDescriber): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// TestProtocol_DeviceListReturnsRegistry: device_list returns the backend's full
// paired-device roster, in order, with every DeviceView field carried through.
func TestProtocol_DeviceListReturnsRegistry(t *testing.T) {
	pairedA := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	pairedB := pairedA.Add(time.Hour)
	stub := newStubDaemon()
	backend := deviceListStub{
		stubDaemon: stub,
		devices: []DeviceView{
			{DeviceID: "devA", Name: "Nathan's iPhone", Capability: "control", PairedAt: pairedA},
			{DeviceID: "devB", Name: "Nathan's iPad", Capability: "view", PairedAt: pairedB},
		},
	}
	sock := serveDeviceLister(t, backend)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapPairing})

	rc.writeControl(Control{Op: OpDeviceList, EndpointID: rep.EndpointID})
	got := rc.readControl()
	if got.Op != OpDeviceList {
		t.Fatalf("device_list reply op = %q; want %q", got.Op, OpDeviceList)
	}
	if len(got.Devices) != 2 {
		t.Fatalf("device_list returned %d devices; want 2", len(got.Devices))
	}
	if got.Devices[0].DeviceID != "devA" || got.Devices[0].Name != "Nathan's iPhone" ||
		got.Devices[0].Capability != "control" || !got.Devices[0].PairedAt.Equal(pairedA) {
		t.Fatalf("device_list[0] = %+v; want devA/Nathan's iPhone/control/%v", got.Devices[0], pairedA)
	}
	if got.Devices[1].DeviceID != "devB" || got.Devices[1].Name != "Nathan's iPad" ||
		got.Devices[1].Capability != "view" || !got.Devices[1].PairedAt.Equal(pairedB) {
		t.Fatalf("device_list[1] = %+v; want devB/Nathan's iPad/view/%v", got.Devices[1], pairedB)
	}
}

// TestProtocol_PolicyQueryReturnsRoots: policy_query returns the backend's
// configured remote allowed-cwd roots, in order.
func TestProtocol_PolicyQueryReturnsRoots(t *testing.T) {
	want := []string{"/home/n/work", "/tmp/x"}
	stub := newStubDaemon()
	backend := policyStub{stubDaemon: stub, roots: want}
	sock := servePolicyDescriber(t, backend)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapPolicy})

	rc.writeControl(Control{Op: OpPolicyQuery, EndpointID: rep.EndpointID})
	got := rc.readControl()
	if got.Op != OpPolicyQuery {
		t.Fatalf("policy_query reply op = %q; want %q", got.Op, OpPolicyQuery)
	}
	if got.Policy == nil {
		t.Fatalf("policy_query reply Policy is nil; want a *PolicyView")
	}
	if len(got.Policy.AllowedCwdRoots) != len(want) {
		t.Fatalf("policy_query roots = %v; want %v", got.Policy.AllowedCwdRoots, want)
	}
	for i, r := range want {
		if got.Policy.AllowedCwdRoots[i] != r {
			t.Fatalf("policy_query roots[%d] = %q; want %q", i, got.Policy.AllowedCwdRoots[i], r)
		}
	}
}

// TestProtocol_ControlPlaneReadOpsRequireCapability: device_list/policy_query are
// refused when their capability was not negotiated at hello — cheap negative
// coverage mirroring journalBackend()'s cap gate, alongside the happy paths above.
func TestProtocol_ControlPlaneReadOpsRequireCapability(t *testing.T) {
	t.Run("device_list_without_pairing_cap", func(t *testing.T) {
		stub := newStubDaemon()
		backend := deviceListStub{stubDaemon: stub, devices: []DeviceView{{DeviceID: "devA"}}}
		sock := serveDeviceLister(t, backend)
		rc := rawDial(t, sock)
		rep := rc.hello(Version, nil) // no capabilities offered

		rc.writeControl(Control{Op: OpDeviceList, EndpointID: rep.EndpointID})
		if got := rc.readControl(); got.Op != OpError {
			t.Fatalf("device_list without pairing cap = op %q; want error", got.Op)
		}
	})

	t.Run("policy_query_without_policy_cap", func(t *testing.T) {
		stub := newStubDaemon()
		backend := policyStub{stubDaemon: stub, roots: []string{"/tmp"}}
		sock := servePolicyDescriber(t, backend)
		rc := rawDial(t, sock)
		rep := rc.hello(Version, nil) // no capabilities offered

		rc.writeControl(Control{Op: OpPolicyQuery, EndpointID: rep.EndpointID})
		if got := rc.readControl(); got.Op != OpError {
			t.Fatalf("policy_query without policy cap = op %q; want error", got.Op)
		}
	})
}
