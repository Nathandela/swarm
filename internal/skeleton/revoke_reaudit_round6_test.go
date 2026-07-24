package skeleton

// FAILING-FIRST (TDD RED, GG-5) tests for the ROUND-6 re-audit findings the round-5 fix
// (commit 2a1a761) introduced/left on the revoke/reconcile path (ADR-007, epoch-key coherence).
//
//   Finding 1 (codex#1 HIGH + opus#1) -- the round-5 fix moved grant.Delete OUTSIDE lifecycleMu.
//        A concurrent BeginPairing commit of the SAME device id (a re-pair of the same phone),
//        serialized on lifecycleMu, runs AddSole + grant.Save the instant RevokeDevice unlocks;
//        THEN RevokeDevice's grant.Delete (now outside the lock) deletes that freshly-saved grant.
//        The re-paired phone is left registered with NO deliverable grant -- a silent dead device.
//        grant.Delete must move back INSIDE lifecycleMu (it is the transaction's atomic core);
//        only the slow network sever (severRemoteControl -> sendDetach) stays OUTSIDE the lock.
//
//   Finding 2 (codex#2 HIGH) -- the startup epoch-mismatch reconcile discards Registry.Remove's
//        (removed, err), so a Remove persistence failure that RESTORES the stale device in memory
//        goes unnoticed and Serve opens remote.sock with the confirmed-stale device registered.
//        reconcilePairedDevices must fail CLOSED: return an error when a stale-epoch device cannot
//        be removed, and Serve must abort assembly rather than serve it.
//
// Reused (same package): assemble (serve_test.go), writeTestIdentity (pairing_config_test.go),
// validDeviceRecord (pairing_findings_test.go).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/machineid"
)

// TestRevokeDevice_ConcurrentRepairGrantSurvives (Finding 1, round-6 REGRESSION): a RevokeDevice(D)
// races a BeginPairing COMMIT that re-enrolls the SAME device id D and saves its grant. Because the
// round-5 fix moved grant.Delete OUTSIDE lifecycleMu, the revoke's grant.Delete lands AFTER the
// commit's grant.Save and deletes the freshly-sealed sidecar, leaving the re-paired phone a dead
// device (registered, no deliverable grant). The commit must run under lifecycleMu (mirroring
// BeginPairing) exactly when the revoke has released the lock -- the last-device sever observer,
// which fires on THIS goroutine in the vulnerable post-lock window, drives that interleave
// deterministically. Fixed (grant.Delete back inside the lock) the fresh sidecar SURVIVES.
func TestRevokeDevice_ConcurrentRepairGrantSurvives(t *testing.T) {
	sk := assemble(t)
	// A machine identity so rotateEpoch (which runs before Remove) succeeds and the transaction
	// reaches Remove + the sever + grant.Delete.
	writeTestIdentity(t, sk.api.stateDir, "round6-repair-host")

	// Seed EXACTLY ONE device (its removal drops Count to 0 -> the last-device sever fires) with its
	// original grant sidecar.
	rec := validDeviceRecord(t)
	if err := sk.api.devices.Add(rec); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if err := grant.Save(sk.api.registryDir(), rec.DeviceID, &crypto.EpochGrant{}); err != nil {
		t.Fatalf("seed grant sidecar: %v", err)
	}

	// The concurrent re-pair commit mirrors BeginPairing's commit section: AddSole + grant.Save for
	// the SAME device id, UNDER lifecycleMu. It is released into that critical section only once the
	// revoke has passed its own lock section -- i.e. from the last-device sever observer, which the
	// round-5 design fires with lifecycleMu already RELEASED (so the commit's Lock never deadlocks).
	commitStart := make(chan struct{})
	commitDone := make(chan struct{})
	sk.api.SetRemoteControlObserver(func() {
		close(commitStart)
		<-commitDone
	})
	go func() {
		<-commitStart
		sk.api.lifecycleMu.Lock()
		_ = sk.api.devices.AddSole(rec)                                          // re-enroll the same phone
		_ = grant.Save(sk.api.registryDir(), rec.DeviceID, &crypto.EpochGrant{}) // fresh sealed grant
		sk.api.lifecycleMu.Unlock()
		close(commitDone)
	}()

	if _, err := sk.api.RevokeDevice(rec.DeviceID); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}

	// The re-paired phone's freshly-saved grant sidecar MUST survive the revoke.
	if _, statErr := os.Stat(grant.Path(sk.api.registryDir(), rec.DeviceID)); statErr != nil {
		t.Fatalf("Finding 1 (round-6): a concurrent re-pair's freshly-saved grant sidecar was deleted by "+
			"RevokeDevice's grant.Delete running OUTSIDE lifecycleMu (after the commit's grant.Save); the "+
			"re-paired phone is left a dead device with no deliverable grant: %v", statErr)
	}
}

// TestReconcilePairedDevices_FailsClosedOnStaleRemoveError (Finding 2, round-6): the startup
// epoch-mismatch reconcile must fail CLOSED. A stale-epoch device whose Remove FAILS (an unwritable
// devices dir makes the atomic persist fail pre-rename, so the registry RESTORES the stale device in
// memory) must make reconcilePairedDevices return an error, so its Serve caller aborts assembly
// rather than open remote.sock still serving the confirmed-stale record. A machine-identity read
// error or a current-epoch device must NOT abort -- covered by the existing TestServe_Reconciles*
// tests, which still expect Serve to come up.
//
// SCOPE (round-7, opus#1/sonnet#2): this is a UNIT test of reconcilePairedDevices -- it corrupts the
// registry AFTER opening it, so it exercises the helper's fail-closed error return, NOT Serve's full
// abort. At the Serve level a chmod-based fault is self-healed because device.Open rehardens the dir
// to 0700 immediately before reconcile runs (registry.go); the Serve abort remains correct + is
// defer-covered, but its real trigger set is non-permission Remove failures (EROFS/ENOSPC/dir
// removed). A Serve-level integration test would need such a fault; not added here.
func TestReconcilePairedDevices_FailsClosedOnStaleRemoveError(t *testing.T) {
	stateDir, err := os.MkdirTemp("/tmp", "swrec")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })

	// Machine identity rotated to epoch 2, so the seeded device (epoch 1) is a CONFIRMED stale
	// mismatch and currentMachineEpoch reports ok=true.
	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	id, err := machineid.Generate("round6-reconcile-host")
	if err != nil {
		t.Fatalf("machineid.Generate: %v", err)
	}
	if err := id.RotateEpoch(); err != nil {
		t.Fatalf("RotateEpoch: %v", err)
	}
	if err := id.Save(filepath.Join(remoteDir, remoteIdentityFile)); err != nil {
		t.Fatalf("save machine identity: %v", err)
	}

	regDir := filepath.Join(stateDir, "devices")
	reg, err := device.Open(regDir)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	stale := validDeviceRecord(t) // GrantedEpoch defaults to 1 -> stale vs the machine epoch 2
	if err := reg.Add(stale); err != nil {
		t.Fatalf("seed stale device: %v", err)
	}
	if err := grant.Save(regDir, stale.DeviceID, &crypto.EpochGrant{}); err != nil {
		t.Fatalf("seed grant sidecar: %v", err)
	}

	// Make the devices dir read-only so Remove's atomic persist (os.CreateTemp) fails pre-rename,
	// restoring the stale device in memory -- the exact fail-open the reconcile must catch.
	if err := os.Chmod(regDir, 0o500); err != nil {
		t.Fatalf("chmod devices dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(regDir, 0o700) })

	rerr := reconcilePairedDevices(reg, stateDir)

	// Restore perms so the assertions/cleanup can act on the dir.
	_ = os.Chmod(regDir, 0o700)

	if rerr == nil {
		t.Fatal("Finding 2 (round-6): reconcilePairedDevices returned nil after a stale-epoch device's Remove " +
			"failed; it must fail CLOSED so Serve aborts rather than serve the confirmed-stale record")
	}
	if _, ok := reg.Get(stale.DeviceID); !ok {
		t.Fatal("test premise broken: the failed Remove should have RESTORED the stale device in memory")
	}
}
