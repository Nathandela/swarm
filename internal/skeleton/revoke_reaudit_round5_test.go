package skeleton

// FAILING-FIRST (TDD RED, GG-5) tests for the ROUND-5 re-audit findings on the
// revoke/reconcile path (ADR-007, epoch-key coherence).
//
//   Finding 1 (codex#2 + opus#1, CRITICAL REGRESSION) -- the round-4 registry change made
//        Registry.Remove return (true, err) on a POST-RENAME dir-fsync failure (the device is
//        durably removed, but the trailing dir-fsync errored). RevokeDevice early-returned on
//        err!=nil BEFORE the last-device sever + grant.Delete, so a committed-but-fsync-failed
//        removal SKIPPED the sever (SeverAllRemoteControl -> the gateway journal unsubscription).
//        A still-running gateway holding the STALE epoch key then keeps its journal subscription
//        and, after a re-pair, re-seals the NEW session under the OLD key to the revoked mailbox.
//        The sever + grant.Delete MUST run whenever the device WAS removed, even on a durability
//        error; the error is surfaced afterward.
//   Finding 3 (codex#1, confidentiality-crash residual) -- a crash after rotateEpoch persisted
//        (epoch N+1) but before Remove persisted leaves the OLD device (GrantedEpoch==N)
//        registered. Serve's startup reconcile removed only a device with a MISSING grant
//        sidecar, NOT one whose GrantedEpoch != the current machine epoch, so the old device
//        survived, remote control re-enabled, and an old-epoch gateway resumed sealing under N.
//        The reconcile must ALSO clear any device whose GrantedEpoch != the current machine epoch.
//
// Reused (same package): assemble (serve_test.go), assembleWithMachineIdentity /
// writeTestIdentity (pairing_config_test.go), validDeviceRecord (pairing_findings_test.go).

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/machineid"
)

// TestRevokeDevice_SeversOnCommittedDurabilityError (Finding 1, round-5 REGRESSION): a
// RevokeDevice whose Registry.Remove commits the removal (device durably gone) yet ALSO returns a
// trailing post-rename dir-fsync durability error must STILL run the last-device sever and
// grant.Delete, and surface the error. The round-4 early-return on err!=nil skipped both -- the
// hole this pins: the skipped sever leaves a still-running gateway's stale-epoch journal
// subscription alive to re-seal a re-paired session to the revoked device's mailbox.
func TestRevokeDevice_SeversOnCommittedDurabilityError(t *testing.T) {
	sk := assemble(t)
	// A machine identity so rotateEpoch (which runs before Remove) succeeds and the full
	// transaction reaches Remove; it touches <stateDir>/remote, NOT the devices dir chmodded below.
	writeTestIdentity(t, sk.api.stateDir, "round5-durability-host")

	// Wire a sever observer we can OBSERVE (assemble wires none -- there is no remote socket).
	var severed atomic.Bool
	sk.api.SetRemoteControlObserver(func() { severed.Store(true) })

	// Seed EXACTLY ONE device so its removal drops Count to 0 and triggers the last-device sever.
	rec := validDeviceRecord(t)
	if err := sk.api.devices.Add(rec); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	// Seed its grant sidecar so we can assert grant.Delete RAN (the sidecar is gone afterward).
	if err := grant.Save(sk.api.registryDir(), rec.DeviceID, &crypto.EpochGrant{}); err != nil {
		t.Fatalf("seed grant sidecar: %v", err)
	}

	// Make the devices-dir dir-fsync FAIL after a successful rename: a 0300 (write+exec, no read)
	// directory lets os.CreateTemp + os.Rename succeed but os.Open(dir) in Registry.persistLocked's
	// syncDir fail (EACCES), so Remove returns (true, <durability err>) -- exactly the committed
	// post-rename path the round-4 registry change introduced. The `grants` subdir stays 0700, so
	// grant.Delete (which acts on devices/grants) is unaffected.
	devDir := sk.api.registryDir()
	if err := os.Chmod(devDir, 0o300); err != nil {
		t.Fatalf("chmod devices dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(devDir, 0o700) }) // ensure temp-dir cleanup can remove it

	removed, err := sk.api.RevokeDevice(rec.DeviceID)

	// Restore perms so the assertions below can read the registry dir.
	_ = os.Chmod(devDir, 0o700)

	if !removed {
		t.Fatal("RevokeDevice reported removed=false on a committed (post-rename) removal; want true")
	}
	if err == nil {
		t.Fatal("RevokeDevice swallowed the post-rename dir-fsync durability error; it must be surfaced")
	}
	if !severed.Load() {
		t.Fatal("Finding 1 (round-5 REGRESSION): the last-device sever did NOT fire on a committed-but-fsync-failed " +
			"revoke; a still-running gateway keeps its stale-epoch journal subscription and can re-seal a re-paired " +
			"session to the revoked device's mailbox")
	}
	if _, statErr := os.Stat(grant.Path(sk.api.registryDir(), rec.DeviceID)); !os.IsNotExist(statErr) {
		t.Fatalf("Finding 1: grant.Delete did NOT run on a committed-but-fsync-failed revoke (sidecar still present: %v)", statErr)
	}
}

// TestServe_ReconcilesStaleEpochDevice (Finding 3): a device registered under a STALE epoch
// (GrantedEpoch != the current machine epoch) -- modelling a crash after rotateEpoch persisted
// N+1 but before Remove persisted -- must be cleared by Serve's startup reconcile, while a device
// granted at the CURRENT epoch survives. Both devices carry a grant sidecar, so ONLY the epoch
// check (not the missing-sidecar reconcile) can distinguish them.
func TestServe_ReconcilesStaleEpochDevice(t *testing.T) {
	stale := validDeviceRecord(t) // GrantedEpoch defaults to 1 -> stale vs the machine epoch 2 below
	current := validDeviceRecord(t)
	current.GrantedEpoch = 2 // matches the rotated machine epoch: must SURVIVE

	sk, err := assembleWithMachineIdentity(t, func(stateDir string) {
		// Provision a machine identity ROTATED to epoch 2, so `stale` (epoch 1) is a confirmed
		// mismatch and `current` (epoch 2) matches.
		remoteDir := filepath.Join(stateDir, "remote")
		if err := os.MkdirAll(remoteDir, 0o700); err != nil {
			t.Fatalf("mkdir remote: %v", err)
		}
		id, err := machineid.Generate("round5-epoch-host")
		if err != nil {
			t.Fatalf("machineid.Generate: %v", err)
		}
		if err := id.RotateEpoch(); err != nil { // epoch 1 -> 2
			t.Fatalf("RotateEpoch: %v", err)
		}
		if id.EpochID() != 2 {
			t.Fatalf("test setup: machine epoch = %d, want 2", id.EpochID())
		}
		if err := id.Save(filepath.Join(remoteDir, "machine.key")); err != nil {
			t.Fatalf("save machine identity: %v", err)
		}

		regDir := filepath.Join(stateDir, "devices")
		reg, err := device.Open(regDir)
		if err != nil {
			t.Fatalf("seed registry: %v", err)
		}
		if err := reg.Add(stale); err != nil {
			t.Fatalf("seed stale-epoch device: %v", err)
		}
		if err := reg.Add(current); err != nil {
			t.Fatalf("seed current-epoch device: %v", err)
		}
		// BOTH get a sidecar, so the missing-grant reconcile leaves both alone -- isolating the
		// epoch check as the ONLY thing that can clear the stale device.
		if err := grant.Save(regDir, stale.DeviceID, &crypto.EpochGrant{}); err != nil {
			t.Fatalf("seed stale sidecar: %v", err)
		}
		if err := grant.Save(regDir, current.DeviceID, &crypto.EpochGrant{}); err != nil {
			t.Fatalf("seed current sidecar: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	if _, ok := sk.api.devices.Get(stale.DeviceID); ok {
		t.Fatal("Finding 3: a device granted under a stale epoch survived load; Serve must reconcile it away " +
			"(an old-epoch gateway would resume sealing under the dead epoch to the revoked phone)")
	}
	if _, ok := sk.api.devices.Get(current.DeviceID); !ok {
		t.Fatal("a device granted at the CURRENT machine epoch was wrongly reconciled away; the epoch reconcile must clear only a CONFIRMED mismatch")
	}
}
