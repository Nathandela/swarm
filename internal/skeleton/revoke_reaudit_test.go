package skeleton

// FAILING-FIRST (TDD RED, GG-5) tests for the ROUND-3 re-audit findings on the
// revoke/pairing/persistence lifecycle (ADR-007, epoch-key coherence). Each test drives a
// concrete interleaving or fault and asserts the security/durability invariant the round-3
// committee found violated.
//
//   Finding 1 (UNANIMOUS) - a RevokeDevice that ROTATES the machine epoch, racing an in-flight
//        BeginPairing, must never let the pairing enroll the new device under the STALE
//        (pre-rotation) epoch the revoked device still holds. The pairing must abort (fail
//        closed) or seal under the new epoch.
//   Finding 3 - revoke must be crash-atomic: rotate the epoch BEFORE removing the device so
//        "device removed => epoch rotated" holds. A rotation failure must leave the device
//        STILL registered (never removed under a stale, still-live key).
//   Finding 4b - a grant.Delete failure during revoke must be SURFACED, not swallowed.
//   Finding 5 - a device whose sealed grant sidecar is absent (a crash between AddSole and
//        grant.Save) is not fully paired; Serve's startup reconcile must clear the slot.
//
// Reused (same package): assemble (serve_test.go), injectPairing / runDeviceLeg /
// recvDeviceEnd (pairing_integration_test.go), validDeviceRecord (pairing_findings_test.go),
// writeTestIdentity / assembleWithMachineIdentity (pairing_config_test.go).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// TestBeginPairing_ConcurrentRevokeRotate_NoStaleEpochEnroll (Finding 1, UNANIMOUS): a
// concurrent RevokeDevice rotates the machine epoch WHILE a BeginPairing is parked mid-
// handshake at its SAS gate, holding the pre-rotation epoch snapshot. When the handshake
// then commits it must NOT enroll the new device under that stale epoch -- the revoked
// device retains that epoch's content key, so a stale-epoch enrollment shares a live key
// with a revoked phone, defeating the rotation.
func TestBeginPairing_ConcurrentRevokeRotate_NoStaleEpochEnroll(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk) // a.pairing at epoch 1, in-memory rendezvous

	// RevokeDevice's rotation reads/rewrites <stateDir>/remote/machine.key; provision one so
	// the rotation actually advances the on-disk (and reloaded a.pairing) epoch.
	writeTestIdentity(t, sk.api.stateDir, "reaudit-rotate-host")
	staleEpoch := sk.api.pairing.EpochID // the epoch the parked handshake seals under

	sasReached := make(chan struct{})
	proceed := make(chan struct{})
	resultCh := make(chan protocol.PairResult, 1)
	confirm := func([]string, string) (bool, error) {
		close(sasReached) // handshake has reached the SAS gate
		<-proceed         // park here until the test injects the revoke-rotate race
		return true, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	view, err := sk.api.BeginPairing(ctx, protocol.PairStartReq{Capability: "full"}, confirm,
		func(r protocol.PairResult) { resultCh <- r })
	if err != nil {
		t.Fatalf("BeginPairing: %v", err)
	}

	// Drive the phone leg on the shared rendezvous so the machine handshake advances to the gate.
	dEnd := recvDeviceEnd(t, deviceEnds)
	qp, err := pairing.DecodeQR(view.QR)
	if err != nil {
		t.Fatalf("decode pair_start QR: %v", err)
	}
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("device keystore: %v", err)
	}
	devDone := runDeviceLeg(ctx, ks, dEnd, qp)

	select {
	case <-sasReached:
	case <-time.After(5 * time.Second):
		t.Fatal("handshake never reached the SAS confirm gate")
	}

	// THE RACE: while the pairing is parked on the stale snapshot, a concurrent revoke rotates
	// the epoch and reloads a.pairing. (Add then revoke a throwaway device so rotation fires.)
	d1 := validDeviceRecord(t)
	if err := sk.api.devices.Add(d1); err != nil {
		t.Fatalf("seed the to-be-revoked device: %v", err)
	}
	if _, err := sk.api.RevokeDevice(d1.DeviceID); err != nil {
		t.Fatalf("concurrent RevokeDevice (rotate): %v", err)
	}
	if sk.api.pairing == nil || sk.api.pairing.EpochID == staleEpoch {
		t.Fatalf("revoke did not rotate the epoch (still %d); the test cannot exercise the race", staleEpoch)
	}

	close(proceed) // release the gate: the parked handshake now commits

	select {
	case r := <-devDone:
		if r.err != nil {
			t.Fatalf("device leg failed: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("device leg never completed")
	}

	var res protocol.PairResult
	select {
	case res = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("pairing never reported a result")
	}

	// The invariant: no device may be enrolled under the stale epoch. Either the pairing
	// aborted (fail closed, nothing enrolled) or it sealed under the NEW epoch.
	if res.Err == nil {
		rec, ok := sk.api.devices.Get(res.DeviceID)
		if ok && rec.GrantedEpoch == staleEpoch {
			t.Fatalf("Finding 1: device %q enrolled under STALE epoch %d during a concurrent revoke-rotate; "+
				"the revoked device retains that epoch's content key", res.DeviceID, staleEpoch)
		}
	} else if got := sk.api.devices.Count(); got != 0 {
		t.Fatalf("pairing aborted but %d device(s) remain enrolled; want 0 (fail closed)", got)
	}
}

// TestRevokeDevice_RotationFailureKeepsDeviceRegistered (Finding 3): revoke must rotate the
// epoch BEFORE removing the device, so a rotation/persist fault aborts the removal and the
// device stays registered (still severable) rather than leaving no device under a stale,
// still-live epoch key ("removed => rotated" invariant).
func TestRevokeDevice_RotationFailureKeepsDeviceRegistered(t *testing.T) {
	sk := assemble(t)
	rec := validDeviceRecord(t)
	if err := sk.api.devices.Add(rec); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	// A CORRUPT machine identity makes rotateEpoch fail at machineid.Load -- a stand-in for any
	// rotation/persist fault (Save f-sync error, etc.).
	remoteDir := filepath.Join(sk.api.stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remoteDir, "machine.key"), []byte("corrupt-not-an-identity"), 0o600); err != nil {
		t.Fatalf("write corrupt identity: %v", err)
	}

	removed, err := sk.api.RevokeDevice(rec.DeviceID)
	if err == nil {
		t.Fatal("RevokeDevice with a failing epoch rotation returned nil error; the rotation fault must be surfaced")
	}
	if removed {
		t.Fatal("RevokeDevice removed the device despite a rotation failure; rotate-before-remove must abort the removal (Finding 3)")
	}
	if _, ok := sk.api.devices.Get(rec.DeviceID); !ok {
		t.Fatal("device was removed despite the rotation failure; the 'removed => rotated' invariant is broken (Finding 3)")
	}
}

// TestRevokeDevice_SurfacesGrantDeleteError (Finding 4b): a grant-sidecar delete failure
// during revoke must be surfaced, not swallowed. An unremovable non-empty directory at the
// sidecar path makes grant.Delete's unlink fail with ENOTEMPTY (deterministic, privilege-
// independent), which RevokeDevice must propagate.
func TestRevokeDevice_SurfacesGrantDeleteError(t *testing.T) {
	sk := assemble(t)
	rec := validDeviceRecord(t)
	if err := sk.api.devices.Add(rec); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	sidecar := grant.Path(sk.api.registryDir(), rec.DeviceID)
	if err := os.MkdirAll(sidecar, 0o700); err != nil {
		t.Fatalf("mkdir sidecar-as-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sidecar, "block"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	if _, err := sk.api.RevokeDevice(rec.DeviceID); err == nil {
		t.Fatal("RevokeDevice swallowed the grant.Delete failure; the sidecar-cleanup error must be surfaced (Finding 4b)")
	}
}

// TestServe_ReconcilesDeviceWithoutGrantSidecar (Finding 5): a device persisted in the
// registry with NO grant sidecar models a crash between AddSole and grant.Save -- it holds
// the single-device slot yet has no deliverable bootstrap grant. Serve's startup reconcile
// must clear it, while leaving a fully-paired device (sidecar present) untouched.
func TestServe_ReconcilesDeviceWithoutGrantSidecar(t *testing.T) {
	orphan := validDeviceRecord(t)   // no sidecar: not fully paired
	withGrant := validDeviceRecord(t) // sidecar present: fully paired (control)

	sk, err := assembleWithMachineIdentity(t, func(stateDir string) {
		// The reconcile is gated on a configured pairing flow (machine identity present), since
		// the AddSole/grant.Save crash it heals can only occur under that flow.
		writeTestIdentity(t, stateDir, "reconcile-host")
		regDir := filepath.Join(stateDir, "devices")
		reg, err := device.Open(regDir)
		if err != nil {
			t.Fatalf("seed registry: %v", err)
		}
		if err := reg.Add(orphan); err != nil {
			t.Fatalf("seed orphan device: %v", err)
		}
		if err := reg.Add(withGrant); err != nil {
			t.Fatalf("seed granted device: %v", err)
		}
		if err := grant.Save(regDir, withGrant.DeviceID, &crypto.EpochGrant{}); err != nil {
			t.Fatalf("seed grant sidecar: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	if _, ok := sk.api.devices.Get(orphan.DeviceID); ok {
		t.Fatal("device with no grant sidecar survived load; Serve must reconcile it away (Finding 5)")
	}
	if _, ok := sk.api.devices.Get(withGrant.DeviceID); !ok {
		t.Fatal("device WITH a grant sidecar was wrongly reconciled away; reconcile must be sidecar-scoped")
	}
}
