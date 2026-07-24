package skeleton

// FAILING-FIRST (TDD RED, GG-5) test for codex#1 (re-audit CONFIDENTIALITY finding,
// ADR-007 amendment 2026-07-24): revoke must ROTATE the machine epoch key.
//
// The hole: RevokeDevice removed the device record but did NOT rotate the shared
// machine epoch key. A revoked phone retains K_content; because every device shared
// the one fixed epoch key, pairing a REPLACEMENT reused it, so the untrusted relay
// could copy the replacement's ciphertext to the revoked phone -- which still
// decrypts it. The fix: revoke rotates the epoch so the revoked device's retained
// key is dead for all FUTURE traffic.
//
// Reused (same package): assemble (serve_test.go), writeTestIdentity +
// loadPairingConfig (pairing_config*.go), validDeviceRecord (pairing_findings_test.go).

import (
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestRevokeDevice_RotatesEpochKey drives the security property through the assembled
// coreAPI: after a device is revoked, the in-memory pairing snapshot seals under a
// HIGHER epoch with a DIFFERENT content key, and a mailbox frame sealed under the NEW
// content key CANNOT be opened with the OLD (pre-revoke) key the revoked device kept.
func TestRevokeDevice_RotatesEpochKey(t *testing.T) {
	sk := assemble(t)

	// Provision a machine identity on disk at the same <stateDir>/remote/machine.key
	// path RevokeDevice's rotation reads, and load it into the live pairing snapshot.
	id := writeTestIdentity(t, sk.api.stateDir, "revoke-rotate-host")
	pc, err := loadPairingConfig(sk.api.stateDir)
	if err != nil || pc == nil {
		t.Fatalf("loadPairingConfig: cfg=%v err=%v", pc, err)
	}
	sk.api.pairing = pc

	preEpoch := id.EpochID()
	preContent := id.EpochKeys().ContentKey

	// Seed one paired device (single-device v1: revoke empties the registry) and revoke it.
	rec := validDeviceRecord(t)
	if err := sk.api.devices.Add(rec); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	removed, err := sk.api.RevokeDevice(rec.DeviceID)
	if err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	if !removed {
		t.Fatal("RevokeDevice reported no device removed; want true")
	}

	// The NEXT pairing must seal under a rotated epoch: a higher id and a fresh content key.
	if sk.api.pairing == nil {
		t.Fatal("pairing snapshot is nil after revoke; the reload dropped it")
	}
	if sk.api.pairing.EpochID <= preEpoch {
		t.Errorf("EpochID = %d after revoke; want > %d (rotated)", sk.api.pairing.EpochID, preEpoch)
	}
	newContent := sk.api.pairing.EpochKeys.ContentKey
	if newContent == preContent {
		t.Fatal("ContentKey unchanged after revoke; the revoked device's retained key is still live")
	}

	// SECURITY PROPERTY: a mailbox frame sealed under the NEW content key cannot be
	// opened with the OLD content key the revoked device retained.
	env, err := crypto.SealMailbox(newContent, crypto.EnvelopeHeader{
		Version: crypto.VersionV1,
		EpochID: sk.api.pairing.EpochID,
		Seq:     1,
	}, []byte("post-revoke session content"))
	if err != nil {
		t.Fatalf("SealMailbox under the new content key: %v", err)
	}
	if _, err := crypto.OpenMailbox(preContent, env); err == nil {
		t.Fatal("the revoked device's OLD content key opened a NEW-epoch frame; the epoch was not rotated (codex#1)")
	}
}
