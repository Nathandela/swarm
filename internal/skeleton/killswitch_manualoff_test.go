package skeleton

// FAILING-FIRST test for A4: the DURABLE MANUAL kill switch (`swarm remote off`/`on`).
// Slice 2b made RemoteControlEnabled() purely DEVICE-DERIVED (registry Count() > 0). A4
// layers a durable OWNER override on top: `off` disables remote control REGARDLESS of
// paired devices — MANUAL OFF WINS over device presence — and `on` returns to the
// device-derived value. The override is durable at <stateDir>/remote-state.json and
// survives a daemon restart, so an owner who severs remote control stays severed.
//
// RED today: *coreAPI does not implement protocol.RemoteControlSetter, so the type
// assertion in remoteControlSetterOf fails — the manual override is unwired. That is the
// correct failing-first reason (mirrors killSwitchOf's runtime RED in
// killswitch_state_test.go, keeping the whole skeleton package compiling meanwhile).

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// remoteControlSetterOf asserts the assembled coreAPI implements the A4
// protocol.RemoteControlSetter seam and returns it. The assertion is legal at compile
// time regardless, so this yields a descriptive RUNTIME RED until the setter is wired.
func remoteControlSetterOf(t *testing.T, sk *Daemon) protocol.RemoteControlSetter {
	t.Helper()
	rc, ok := any(sk.api).(protocol.RemoteControlSetter)
	if !ok {
		t.Fatal("assembled coreAPI does not implement protocol.RemoteControlSetter: the durable manual off/on override is unwired (A4)")
	}
	return rc
}

// TestKillSwitch_ManualOffWinsOverDevicePresence: with a device paired (device-derived
// switch ON), `SetRemoteControl(false)` disables remote control and WINS over the paired
// device; the override persists to remote-state.json and survives a restart; and
// `SetRemoteControl(true)` returns to device-derived enablement.
func TestKillSwitch_ManualOffWinsOverDevicePresence(t *testing.T) {
	dir := freshStateDir(t)
	sk, _ := assembleRemoteAt(t, dir)

	// A paired device makes the device-derived switch ON.
	registerPhone(t, sk, device.CapFull)
	if !killSwitchOf(t, sk).RemoteControlEnabled() {
		t.Fatal("precondition: switch not enabled after pairing a device")
	}

	// Manual OFF must WIN over device presence.
	if err := remoteControlSetterOf(t, sk).SetRemoteControl(false); err != nil {
		t.Fatalf("SetRemoteControl(false): %v", err)
	}
	if killSwitchOf(t, sk).RemoteControlEnabled() {
		t.Fatal("manual OFF must win over a paired device; RemoteControlEnabled() still true")
	}

	// Durable: remote-state.json records the manual override (ManualOff=true).
	st, err := loadRemoteState(dir)
	if err != nil {
		t.Fatalf("loadRemoteState: %v", err)
	}
	if !st.ManualOff {
		t.Fatal("manual off must persist to remote-state.json (ManualOff=true)")
	}

	// Survives a restart: reopen over the SAME stateDir with the device still paired.
	if err := sk.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	sk2, _ := assembleRemoteAt(t, dir)
	t.Cleanup(func() { _ = sk2.Close() })
	if killSwitchOf(t, sk2).RemoteControlEnabled() {
		t.Fatal("manual off did not survive restart: RemoteControlEnabled() true after re-open; want disabled (durable override)")
	}

	// Manual ON returns to device-derived enablement (the device is still paired).
	if err := remoteControlSetterOf(t, sk2).SetRemoteControl(true); err != nil {
		t.Fatalf("SetRemoteControl(true): %v", err)
	}
	if !killSwitchOf(t, sk2).RemoteControlEnabled() {
		t.Fatal("manual on must return to device-derived enablement; a device is paired so want enabled")
	}
}
