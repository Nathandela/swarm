package skeleton

// FAILING-FIRST (TDD RED, GG-5) tests for the ROUND-4 re-audit epoch-lifecycle
// serialization findings (ADR-007, epoch-key coherence). The round-3 point fixes
// (pairingMu for the pointer, a commit-point epoch re-check) NARROWED but did not close
// two residual races; both are serialized by the coreAPI lifecycleMu introduced here.
//
//   Finding 1 (codex#2/sonnet#1/opus#4, UNANIMOUS) -- epoch TOCTOU RESIDUAL: BeginPairing's
//        commit re-reads a.pairing, then UNLOCKS, and only AFTER that runs enroll.Enroll (which
//        seals under the ENTRY cfg epoch) + AddSole + grant.Save. A concurrent RevokeDevice can
//        rotate (N->N+1) AND remove the just-enrolled device in that window, so AddSole finds an
//        empty registry and commits the new device under the STALE epoch N -- a self-bricked dead
//        device whose grant seals N while the machine is N+1, sharing N's still-live content key
//        with the just-revoked phone.
//   Finding 4 (codex#4/sonnet#4) -- two concurrent RevokeDevice of the SAME device race an
//        UNLOCKED load-modify-save of machine.key in rotateEpoch: both pass the presence check
//        and BOTH rotate, so the epoch is advanced twice (a wasted rotation / lost update) when a
//        correct serial revoke rotates exactly once.
//
// Determinism: the coreAPI lifecycleGate seam (nil in production) parks a transaction at the
// exact window each finding lives in, so the RED interleaving is reproduced every run.
//
// Reused (same package): assemble (serve_test.go), injectPairing / runDeviceLeg /
// recvDeviceEnd (pairing_integration_test.go), validDeviceRecord (pairing_findings_test.go),
// writeTestIdentity + loadPairingConfig (pairing_config*.go).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// TestBeginPairing_CommitRaceRevoke_NoStaleEpochEnroll (Finding 1, round-4): a pairing parked
// in its COMMIT window (past the epoch re-check, before AddSole) races a RevokeDevice that
// rotates the epoch and empties the registry. The invariant: once everything settles, NO
// device may remain enrolled under an epoch other than the current machine epoch -- either the
// pairing enrolls under the live epoch, or it aborts and enrolls nothing (fail closed). A
// stale-epoch enrollment shares a live content key with the just-revoked device.
//
// The gate deterministically parks the victim in exactly the re-check -> AddSole window the
// residual TOCTOU lives in, standing in for the second of the two concurrent pairings the
// finding describes; the seeded device stands in for the first pairing's committed enrollment.
func TestBeginPairing_CommitRaceRevoke_NoStaleEpochEnroll(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk) // a.pairing at epoch 1, in-memory rendezvous
	// RevokeDevice's rotation reads/rewrites <stateDir>/remote/machine.key; provision one so
	// the rotation actually advances the on-disk (and reloaded a.pairing) epoch.
	writeTestIdentity(t, sk.api.stateDir, "round4-commit-host")
	staleEpoch := sk.api.pairing.EpochID // the epoch the parked handshake would seal under

	// Park the victim pairing INSIDE its commit window (after the epoch re-check passes,
	// before enroll/AddSole). Ignore the revoke's own "revoke-rotated" point in this test.
	gateReached := make(chan struct{})
	releaseGate := make(chan struct{})
	sk.api.lifecycleGate = func(point string) {
		if point != "pair-commit" {
			return
		}
		close(gateReached)
		<-releaseGate
	}

	resultCh := make(chan protocol.PairResult, 1)
	confirm := func([]string, string) (bool, error) { return true, nil }

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	view, err := sk.api.BeginPairing(ctx, protocol.PairStartReq{Capability: "full"}, confirm,
		func(r protocol.PairResult) { resultCh <- r })
	if err != nil {
		t.Fatalf("BeginPairing: %v", err)
	}

	// Drive the phone leg so the machine handshake completes and reaches the commit gate.
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
	case <-gateReached:
	case <-time.After(5 * time.Second):
		t.Fatal("victim pairing never reached the commit gate")
	}

	// Seed the device a concurrent pairing would already have committed. It becomes present
	// only NOW -- after the victim passed the entry C6 gate -- so the revoke below has a device
	// to rotate+remove, exactly as in the two-pairings-plus-revoke interleaving.
	d1 := validDeviceRecord(t)
	if err := sk.api.devices.Add(d1); err != nil {
		t.Fatalf("seed the concurrently-enrolled device: %v", err)
	}

	// THE RACE: revoke that device while the victim is parked in its commit window. lifecycleMu
	// must force this to WAIT for the victim's commit to finish; without it, the revoke rotates
	// (1->2) and removes d1 here, and the released victim then AddSoles into the empty registry
	// under the stale epoch 1.
	revokeDone := make(chan struct{})
	go func() {
		_, _ = sk.api.RevokeDevice(d1.DeviceID)
		close(revokeDone)
	}()

	// Give the revoke room to act. Unfixed: it completes (revokeDone closes) before we release
	// the gate. Fixed: it blocks on lifecycleMu (revokeDone stays open) until the commit ends.
	select {
	case <-revokeDone:
	case <-time.After(time.Second):
	}
	close(releaseGate)

	select {
	case <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("victim pairing never reported a result")
	}
	select {
	case <-revokeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("revoke never completed")
	}
	select {
	case r := <-devDone:
		if r.err != nil {
			t.Fatalf("device leg failed: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("device leg never completed")
	}

	// The rotation must have advanced the epoch, or the race was never exercised.
	cur := sk.api.pairing.EpochID
	if cur == staleEpoch {
		t.Fatalf("revoke did not rotate the epoch (still %d); the test cannot exercise the race", staleEpoch)
	}
	// INVARIANT (epoch-key coherence): no registered device may sit under a stale epoch.
	for _, rec := range sk.api.devices.List() {
		if rec.GrantedEpoch != cur {
			t.Fatalf("finding 1 (round-4): device %q enrolled at epoch %d while the machine is at epoch %d "+
				"(a stale-epoch enrollment shares the revoked device's still-live content key)",
				rec.DeviceID, rec.GrantedEpoch, cur)
		}
	}
}

// TestRevokeDevice_ConcurrentSameDevice_RotatesOnce (Finding 4, round-4): two concurrent
// RevokeDevice of the SAME device must rotate the machine epoch EXACTLY ONCE -- a correct serial
// revoke rotates once, so the concurrent pair must too. The gate parks the first revoke just
// after it rotated + persisted (device not yet removed); the second revoke is then released, so
// without lifecycleMu it passes the presence check and rotates AGAIN off the first's new image
// (epoch advances twice, a lost update), leaving a.pairing / machine.key two epochs ahead.
// lifecycleMu forces the second revoke to wait for the first's whole transaction, find the device
// already gone, and NOT rotate, so the epoch advances by exactly one.
func TestRevokeDevice_ConcurrentSameDevice_RotatesOnce(t *testing.T) {
	sk := assemble(t)
	// a.pairing must be derived from the SAME machine.key rotateEpoch rewrites, so comparing
	// memory against disk is meaningful.
	writeTestIdentity(t, sk.api.stateDir, "round4-revoke-host")
	pc, err := loadPairingConfig(sk.api.stateDir)
	if err != nil || pc == nil {
		t.Fatalf("loadPairingConfig: cfg=%v err=%v", pc, err)
	}
	sk.api.pairing = pc
	keyPath := filepath.Join(sk.api.stateDir, "remote", "machine.key")
	preEpoch := sk.api.pairing.EpochID

	// Park each revoke that reaches the post-rotate window. gateReached is buffered so a second
	// (unfixed) arrival never blocks before the test drains it.
	gateReached := make(chan struct{}, 2)
	releaseGate := make(chan struct{})
	sk.api.lifecycleGate = func(point string) {
		if point != "revoke-rotated" {
			return
		}
		gateReached <- struct{}{}
		<-releaseGate
	}

	rec := validDeviceRecord(t)
	if err := sk.api.devices.Add(rec); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	// Launch revoke A and wait for it to rotate + persist + park (device NOT yet removed). Only
	// THEN launch revoke B, so B necessarily observes A's already-rotated machine.key -- the
	// interleaving that produces the double rotation when the transaction is not serialized.
	doneA := make(chan struct{})
	go func() {
		_, _ = sk.api.RevokeDevice(rec.DeviceID)
		close(doneA)
	}()
	select {
	case <-gateReached:
	case <-time.After(5 * time.Second):
		t.Fatal("revoke A never reached the post-rotate gate")
	}

	doneB := make(chan struct{})
	go func() {
		_, _ = sk.api.RevokeDevice(rec.DeviceID)
		close(doneB)
	}()
	// Give B room to ALSO rotate. Unfixed: B passes the presence check (A has not removed the
	// device) and rotates again (second arrival). Fixed: B blocks on lifecycleMu, and once A
	// finishes + removes the device B finds it gone and never rotates -- so no second arrival.
	select {
	case <-gateReached:
	case <-time.After(time.Second):
	}
	close(releaseGate)

	for _, done := range []chan struct{}{doneA, doneB} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a revoke never completed")
		}
	}

	// The epoch must advance by EXACTLY ONE across the two concurrent revokes.
	final := sk.api.pairing.EpochID
	if final != preEpoch+1 {
		t.Fatalf("finding 4 (round-4): epoch advanced from %d to %d (want exactly one rotation, %d); "+
			"two concurrent revokes of the same device both rotated (lost update)", preEpoch, final, preEpoch+1)
	}
	// And a.pairing must stay coherent with machine.key on disk.
	disk, err := machineid.Load(keyPath)
	if err != nil {
		t.Fatalf("reload machine.key: %v", err)
	}
	if sk.api.pairing.EpochID != disk.EpochID() || sk.api.pairing.EpochKeys != disk.EpochKeys() {
		t.Fatalf("finding 4 (round-4): in-memory pairing diverged from on-disk machine.key after concurrent revoke")
	}
}
