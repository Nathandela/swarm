package protocol

// FAILING-FIRST tests for A4: the owner-tier remote-control master-switch toggle op
// (OpRemoteSetControl) — the wire seam behind `swarm remote off`/`on`. Contract:
//
//   - on the OWNER (main) tier it flips the durable switch every choke point reads
//     (RemoteControlSetter.SetRemoteControl), replying OK;
//   - on the REMOTE tier it is REFUSED not_authorized BEFORE the backend is consulted —
//     toggling the master switch is OWNER-ONLY, exactly like pair_start. A remote device
//     must never be able to RE-ENABLE a switch its owner turned off, so the refusal must
//     precede the setter (mirrors handlePairStart's remote-tier fail-closed refusal).
//
// RED today: OpRemoteSetControl has no dispatch case, so the owner-tier op is answered
// "unknown op" (not OK, so the switch is never flipped) and the remote-tier op is
// "unknown op" (not not_authorized). RemoteControlSetter / Control.RemoteControl /
// Client.SetRemoteControl do not exist yet either, so this file fails to compile until
// GREEN adds them — an acceptable compile-fail RED for a new API, unambiguous by name.

import (
	"sync"
	"testing"
)

// setControlStub is a stubDaemon that ALSO honors the A4 owner toggle: SetRemoteControl
// flips an override its RemoteControlEnabled reports, so a test can prove the owner op
// flips the VERY switch the choke points read. It embeds *stubDaemon for the rest of the
// DaemonAPI plus the remote-tier construction guards (OperationClaimer + DeviceAuthenticator).
type setControlStub struct {
	*stubDaemon
	mu       sync.Mutex
	override *bool // nil => stubDaemon's default ON
}

func newSetControlStub() *setControlStub { return &setControlStub{stubDaemon: newStubDaemon()} }

// RemoteControlEnabled overrides the embedded stubDaemon's constant ON so SetRemoteControl
// can flip it (nil override => ON, matching the embedded default).
func (s *setControlStub) RemoteControlEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.override != nil {
		return *s.override
	}
	return true
}

// SetRemoteControl makes setControlStub a protocol.RemoteControlSetter (A4): it flips the
// override RemoteControlEnabled reports.
func (s *setControlStub) SetRemoteControl(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := enabled
	s.override = &v
	return nil
}

// TestRemoteSetControl_OwnerTierFlipsSwitch: on the owner tier, remote_set_control(off)
// flips the switch the choke points read to OFF, and remote_set_control(on) re-enables it.
func TestRemoteSetControl_OwnerTierFlipsSwitch(t *testing.T) {
	stub := newSetControlStub()
	sock := servePairingHost(t, stub) // OWNER tier (Serve)
	c := dialClient(t, sock, []string{CapPairing})

	if !stub.RemoteControlEnabled() {
		t.Fatal("precondition: stub RemoteControlEnabled() = false; want true (default ON)")
	}

	// `remote off` -> SetRemoteControl(false).
	if err := c.SetRemoteControl(false); err != nil {
		t.Fatalf("SetRemoteControl(false): %v", err)
	}
	if stub.RemoteControlEnabled() {
		t.Fatal("owner-tier remote_set_control(off) did not flip the switch; RemoteControlEnabled() still true")
	}

	// `remote on` -> SetRemoteControl(true).
	if err := c.SetRemoteControl(true); err != nil {
		t.Fatalf("SetRemoteControl(true): %v", err)
	}
	if !stub.RemoteControlEnabled() {
		t.Fatal("owner-tier remote_set_control(on) did not re-enable the switch")
	}
}

// TestRemoteSetControl_RemoteTierRefused: on the remote tier, remote_set_control is
// refused not_authorized BEFORE the setter runs — the master switch is owner-only, so a
// remote device cannot re-enable a switch its owner turned off.
func TestRemoteSetControl_RemoteTierRefused(t *testing.T) {
	stub := newSetControlStub()
	sock := serveRemoteAPI(t, stub) // REMOTE tier (ServeRemote)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapPairing})

	enabled := false
	rc.writeControl(Control{Op: OpRemoteSetControl, EndpointID: rep.EndpointID, RemoteControl: &enabled})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("remote-tier remote_set_control = op %q code %q; want error/not_authorized (owner-only)", got.Op, got.ErrorCode)
	}
	// The backend switch must be untouched — the op was refused BEFORE the setter.
	if !stub.RemoteControlEnabled() {
		t.Fatal("remote-tier remote_set_control flipped the switch; want refused before the setter (still enabled)")
	}
}
