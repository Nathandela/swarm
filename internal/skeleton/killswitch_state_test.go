package skeleton

// FAILING-FIRST tests for the DURABLE remote-control KILL-SWITCH STATE (plan R-KS.1 +
// R-KS.2 / fix-pack item 2b). Item 2a already landed the ENFORCEMENT in
// internal/protocol: requireRemoteAuthz refuses EVERY remote mutating op with
// CodeKillSwitch as its FIRST gate whenever the backend implements protocol.KillSwitch
// and RemoteControlEnabled() reports false (committed 3958c13). What is missing — and
// what these tests pin — is coreAPI actually IMPLEMENTING that interface, backed by
// durable state, with the user-chosen default OFF UNTIL A DEVICE IS PAIRED.
//
// PINNED MODEL (the implementer wires this; documented here so the seam is unambiguous):
//
//   - RemoteControlEnabled() is DERIVED FROM DEVICE PRESENCE and mirrored to a durable
//     0600 file at <stateDir>/remote-state.json (R-KS.1). Concretely: coreAPI reports
//     enabled == (device registry Count() > 0), recomputed at read time and written
//     through to remote-state.json on each transition.
//   - Default OFF until paired: a fresh assembly (no state file, no devices) has Count()==0
//     => RemoteControlEnabled()==false => every remote mutating op is refused CodeKillSwitch
//     (fail-closed).
//   - Enable-on-first-pair: registering the first device makes Count()>0 => enabled=true,
//     persisted.
//   - Auto-off-at-zero-devices (R-KS.2): removing the LAST device makes Count()==0 =>
//     enabled=false, persisted — device loss WITHOUT revocation is RCE, so the switch must
//     fail closed the instant the registry empties.
//
// WHY device-derived (and not a separate manual flag): the assembled E2E tests register
// devices by calling sk.api.devices.Add / .Remove DIRECTLY (registerPhone, enroll). Only a
// switch that consults device presence AT READ TIME flips on/off for those direct mutations
// without a hook the direct calls would bypass — so this model both satisfies R-KS.1/.2 and
// keeps every device-registering E2E test green for free. A manual off is therefore NOT
// modelled here (RemoteControlEnabled never returns false while a device is paired); if a
// later slice adds one, its persistence is a separate contract.
//
// RED today: *coreAPI does not implement protocol.KillSwitch, so killSwitchOf fails the
// type assertion — the durable switch is unwired. That is the correct failing-first reason.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// killSwitchOf asserts the assembled coreAPI implements protocol.KillSwitch (the seam
// slice 2b adds) and returns it. The type assertion is legal at compile time regardless,
// so this yields a RUNTIME RED with a descriptive reason until the switch is wired —
// keeping the whole skeleton package compiling in the meantime.
func killSwitchOf(t *testing.T, sk *Daemon) protocol.KillSwitch {
	t.Helper()
	ks, ok := any(sk.api).(protocol.KillSwitch)
	if !ok {
		t.Fatal("assembled coreAPI does not implement protocol.KillSwitch: the durable remote-control switch is unwired (slice 2b R-KS.1)")
	}
	return ks
}

// freshStateDir makes a short-pathed /tmp state dir (keeps the UDS under the 104-byte
// sun_path limit) with cleanup, for a test that reopens the SAME dir across a restart.
func freshStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swsk-ks")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// assembleRemoteAt stands up the assembly with a remote socket over an EXPLICIT stateDir,
// so a restart test can Serve twice over the same dir. Unlike assembleWithRemote it does
// NOT register a Close cleanup: the caller manages the lifecycle (Close before reopen).
// It seeds the deny-by-default remote-launch policy the same way assembleWithRemote does,
// so it stays a drop-in for remote launches.
func assembleRemoteAt(t *testing.T, dir string) (*Daemon, string) {
	t.Helper()
	buildBinaries(t)
	rsock := filepath.Join(dir, "r.sock")
	tmpRoot, terr := filepath.EvalSymlinks(os.TempDir())
	if terr != nil {
		tmpRoot = filepath.Clean(os.TempDir())
	}
	if werr := writeRemoteLaunchPolicy(dir, []string{tmpRoot}); werr != nil {
		t.Fatalf("seed remote launch policy: %v", werr)
	}
	sk, err := Serve(Config{
		StateDir:           dir,
		SocketPath:         filepath.Join(dir, "d.sock"),
		LockPath:           filepath.Join(dir, "d.lock"),
		LogPath:            filepath.Join(dir, "d.log"),
		ShimBinary:         swarmBin,
		MaxSessions:        16,
		PollInterval:       50 * time.Millisecond,
		StalenessThreshold: 2 * time.Second,
		FakeAgentBin:       fakeAgentBin,
		RemoteSocketPath:   rsock,
	})
	if err != nil {
		t.Fatalf("Serve at %s: %v", dir, err)
	}
	return sk, rsock
}

// refuseKillOnRemote sends a well-formed remote OpKill (valid operation_id + device
// fields, an ExpiresAt in the future) over the remote socket and returns the reply. The
// device fields are present and structurally valid so that IF the switch were on the op
// would reach device auth (and fail not_authorized); with the switch OFF the reply must
// instead be CodeKillSwitch, proving the switch is the FIRST gate — a signature-shaped op
// cannot slip past it.
func refuseKillOnRemote(t *testing.T, rsock, opID string) protocol.Control {
	t.Helper()
	rc := dialRemote(t, rsock)
	sid := rc.endpointID + "/sess1"
	exp := time.Now().Add(time.Minute)
	rc.write(protocol.Control{
		Op: protocol.OpKill, EndpointID: rc.endpointID, SessionID: sid,
		OperationID: opID, DeviceID: "devA", DeviceSig: "c2ln", ExpiresAt: &exp,
	})
	return rc.read(5 * time.Second)
}

// TestKillSwitch_DefaultOffUntilDevicePaired: a fresh assembly with no state file and no
// paired device reports RemoteControlEnabled()==false, and a remote mutating op is refused
// CodeKillSwitch. After a device is paired the switch reports true and a validly-signed op
// is allowed (not refused CodeKillSwitch). This pins the user-authoritative default:
// OFF-UNTIL-PAIRED (fail-closed).
func TestKillSwitch_DefaultOffUntilDevicePaired(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := killSwitchOf(t, sk)

	// Fresh: no devices, no state file => OFF.
	if ks.RemoteControlEnabled() {
		t.Fatal("fresh assembly (no devices, no state file) reports remote control ENABLED; want off-until-paired (fail-closed)")
	}
	if got := refuseKillOnRemote(t, rsock, "op-ks-off-1"); got.Op != protocol.OpError || got.ErrorCode != protocol.CodeKillSwitch {
		t.Fatalf("remote kill with the default switch off = op %q code %q; want error/kill_switch", got.Op, got.ErrorCode)
	}

	// Pair the first device: the switch flips ON.
	phone := registerPhone(t, sk, device.CapFull)
	if !ks.RemoteControlEnabled() {
		t.Fatal("after pairing the first device, remote control still reports DISABLED; want enabled (enable-on-first-pair)")
	}

	// A validly-signed op is now allowed — not refused CodeKillSwitch.
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	namespaced := protocol.NamespacedID(sk.api.endpointID, meta.ID)
	cmd, err := phonecore.SignCommand(phone, phonecore.CommandInput{
		Action:      protocol.ActionKill,
		Machine:     sk.api.endpointID,
		Session:     namespaced,
		OperationID: "op-ks-on-1",
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("phone sign: %v", err)
	}
	reply, err := remotegw.New(rsock, nil).ForwardCommand(protocol.OpKill, namespaced, cmd, nil)
	if err != nil {
		t.Fatalf("gateway forward: %v", err)
	}
	if reply.Op == protocol.OpError && reply.ErrorCode == protocol.CodeKillSwitch {
		t.Fatalf("switch reported enabled after pairing, but the op was still refused kill_switch: %q", reply.Error)
	}
	if reply.Op == protocol.OpError {
		t.Fatalf("paired-device kill refused with the switch on: %q / %q", reply.Error, reply.ErrorCode)
	}
}

// TestKillSwitch_StateSurvivesRestart: enable the switch by pairing a device, tear the
// assembly down, and re-open it over the SAME stateDir; RemoteControlEnabled() is still
// true. It also pins R-KS.1's durable artifact: a 0600 remote-state.json under the state
// dir once enabled.
func TestKillSwitch_StateSurvivesRestart(t *testing.T) {
	dir := freshStateDir(t)
	sk, _ := assembleRemoteAt(t, dir)

	registerPhone(t, sk, device.CapFull)
	if !killSwitchOf(t, sk).RemoteControlEnabled() {
		t.Fatal("switch not enabled after pairing a device (before restart)")
	}

	// R-KS.1: the enabled state is durable at <stateDir>/remote-state.json, mode 0600.
	statePath := filepath.Join(dir, "remote-state.json")
	fi, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("durable kill-switch state file %s missing after enable (R-KS.1): %v", statePath, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("remote-state.json perms = %#o; want 0600 (R-KS.1)", perm)
	}

	// Restart: close and re-open over the SAME stateDir.
	if err := sk.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	sk2, _ := assembleRemoteAt(t, dir)
	t.Cleanup(func() { _ = sk2.Close() })

	if !killSwitchOf(t, sk2).RemoteControlEnabled() {
		t.Fatal("kill switch did not survive restart: reports DISABLED after re-open over the same stateDir; want enabled (R-KS.1)")
	}
}

// TestKillSwitch_AutoOffOnZeroDevices: with one device paired (switch on), revoke it; the
// switch flips OFF and remote mutating ops are refused CodeKillSwitch. Pins R-KS.2 — losing
// the last device must fail closed, since a device lost WITHOUT revocation is otherwise an
// open remote-control surface (RCE).
func TestKillSwitch_AutoOffOnZeroDevices(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := killSwitchOf(t, sk)

	phone := registerPhone(t, sk, device.CapFull)
	if !ks.RemoteControlEnabled() {
		t.Fatal("switch not enabled after pairing a device")
	}

	// Revoke the LAST device.
	devID := device.DeviceIDFor(phone.CommandSigningPublic())
	removed, err := sk.api.devices.Remove(devID)
	if err != nil {
		t.Fatalf("remove device: %v", err)
	}
	if !removed {
		t.Fatalf("device %s was not present to remove", devID)
	}

	if ks.RemoteControlEnabled() {
		t.Fatal("after revoking the LAST paired device, remote control still reports ENABLED; want auto-off (R-KS.2)")
	}
	if got := refuseKillOnRemote(t, rsock, "op-ks-auto-off-1"); got.Op != protocol.OpError || got.ErrorCode != protocol.CodeKillSwitch {
		t.Fatalf("remote kill after the last device was revoked = op %q code %q; want error/kill_switch (R-KS.2 auto-off)", got.Op, got.ErrorCode)
	}
}
