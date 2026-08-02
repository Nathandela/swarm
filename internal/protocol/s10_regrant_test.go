package protocol

// PB-KEY-3's machine-side unblock at the WIRE. internal/skeleton proves RegrantDevice does
// the right thing; this proves an owner can actually REACH it, which is the difference
// between "a documented machine-side unblock" and a Go function nothing invokes. The
// standing class-(v) trap is exactly that shape: a real-components test supplies the entry
// point by hand and nobody notices no process has one.
//
// Two properties, and the tier is the whole decision:
//
//   - OWNER tier serves it. The owner is at the machine and the phone is the thing that is
//     broken.
//   - REMOTE tier refuses it BEFORE the backend is consulted. A device whose grant was lost
//     holds no epoch CONTENT key, so it cannot seal a command for the gateway at all -- a
//     remote-tier regrant would be unusable by precisely the device that needs one, while
//     handing any remote caller a lever to make the machine re-issue key material.

import (
	"sync"
	"testing"
)

// regrantStub is a stubDaemon that ALSO records re-grants, so a test can prove the op
// reached the backend -- or provably did not.
type regrantStub struct {
	*stubDaemon
	mu      sync.Mutex
	granted []string
}

func newRegrantStub() *regrantStub { return &regrantStub{stubDaemon: newStubDaemon()} }

// RegrantDevice makes regrantStub a protocol.DeviceRegranter.
func (s *regrantStub) RegrantDevice(deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.granted = append(s.granted, deviceID)
	return nil
}

func (s *regrantStub) regrants() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.granted...)
}

// TestS10_TheOwnerCanRegrantADevice is the reachability fence: the verb exists on the wire
// the owner CLI dials, and it names the target device.
func TestS10_TheOwnerCanRegrantADevice(t *testing.T) {
	stub := newRegrantStub()
	sock := servePairingHost(t, stub) // OWNER tier
	c := dialClient(t, sock, []string{CapPairing})

	if err := c.RegrantDevice("device-abc"); err != nil {
		t.Fatalf("RegrantDevice: %v. Without a wire verb the only exit from a lost epoch grant "+
			"is physical access to the machine: the relay purges the bootstrap frame past its "+
			"retention cap even when never acked, and re-pairing is refused outright while a "+
			"device is registered", err)
	}
	if got := stub.regrants(); len(got) != 1 || got[0] != "device-abc" {
		t.Errorf("the backend saw re-grants %v, want [device-abc]", got)
	}
}

// TestS10_ARegrantIsRefusedOnTheRemoteTier is the mirror, and the non-vacuity control for
// the test above: an implementation that served the op on every tier passes that one and
// fails this one.
func TestS10_ARegrantIsRefusedOnTheRemoteTier(t *testing.T) {
	stub := newRegrantStub()
	sock := serveRemoteAPI(t, stub) // REMOTE tier
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapPairing})

	rc.writeControl(Control{Op: OpDeviceRegrant, EndpointID: rep.EndpointID, TargetDeviceID: "device-abc"})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("remote-tier device_regrant = op %q code %q; want error/not_authorized. Re-issuing "+
			"key material is the owner's act, at the machine: the device that needs one cannot "+
			"seal a command at all, so a remote-tier verb serves only a caller that does not need it",
			got.Op, got.ErrorCode)
	}
	if n := len(stub.regrants()); n != 0 {
		t.Errorf("the backend minted %d grant(s) for a refused remote-tier request; the refusal "+
			"must precede the backend", n)
	}
}

// TestS10_ARegrantWithNoTargetIsRefused: the target id is the whole request, and an empty
// one must not reach a backend that would have to guess which device was meant.
func TestS10_ARegrantWithNoTargetIsRefused(t *testing.T) {
	stub := newRegrantStub()
	sock := servePairingHost(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapPairing})

	rc.writeControl(Control{Op: OpDeviceRegrant, EndpointID: rep.EndpointID})
	if got := rc.readControl(); got.Op != OpError {
		t.Errorf("device_regrant with no target device id = op %q, want a refusal", got.Op)
	}
	if n := len(stub.regrants()); n != 0 {
		t.Errorf("a targetless device_regrant reached the backend %d time(s)", n)
	}
}
