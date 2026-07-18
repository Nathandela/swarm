// R-CRY.16 / R-POL.9 (crypto half) — per-command device signatures (ADR-007
// D4). Every remote mutating op carries a detached Ed25519 signature over the
// canonical tuple (action, machine=endpoint id, session, operation_id,
// expires_at, content_hash?); the daemon verifies it against the pinned device
// command-signing key before executing. A compromised relay cannot forge
// commands; a replay bound to a different operation_id/expiry does not verify.
//
// FROZEN CONTRACT (subset):
//
//	type Command struct { Action, Machine, Session, OperationID string; ExpiresAt int64; ContentHash []byte }
//	func (Command) Canonical() []byte
//	func VerifyCommandSig(commandSigningPub, msg, sig []byte) error
//	(signing is KeyStore.SignCommand(Command.Canonical()))
package crypto

import (
	"bytes"
	"testing"
)

func sampleCommand() Command {
	return Command{
		Action:      "kill",
		Machine:     "endpoint-abc",
		Session:     "sess-42",
		OperationID: "op-0001",
		ExpiresAt:   1_800_000_000,
		ContentHash: nil,
	}
}

// TestDeviceSig_SignVerifyRoundTrip pins that a signature over the canonical
// tuple verifies against the device command-signing public key.
func TestDeviceSig_SignVerifyRoundTrip(t *testing.T) {
	ks := devKeyStore(t, stdMaterial())
	c := sampleCommand()

	sig := ks.SignCommand(c.Canonical())
	if len(sig) != 64 {
		t.Errorf("Ed25519 signature length = %d, want 64", len(sig))
	}
	if err := VerifyCommandSig(ks.CommandSigningPublic(), c.Canonical(), sig); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

// TestDeviceSig_ForgedRejected pins that a tampered signature, and a signature
// from a different device key, both fail verification.
func TestDeviceSig_ForgedRejected(t *testing.T) {
	ks := devKeyStore(t, stdMaterial())
	other := devKeyStore(t, KeyMaterial{
		NoiseStaticPriv: fill(0x61), RecipientPriv: fill(0x62),
		CommandSignSeed: fill(0x63), RelayAuthSeed: fill(0x64),
	})
	c := sampleCommand()
	sig := ks.SignCommand(c.Canonical())

	// Bit-flipped signature.
	forged := append([]byte(nil), sig...)
	forged[0] ^= 0xff
	if err := VerifyCommandSig(ks.CommandSigningPublic(), c.Canonical(), forged); err == nil {
		t.Error("tampered signature accepted")
	}
	// Right message, wrong signer.
	if err := VerifyCommandSig(other.CommandSigningPublic(), c.Canonical(), sig); err == nil {
		t.Error("signature verified against a different device key")
	}
	// Right signer, wrong (unregistered) key length.
	if err := VerifyCommandSig([]byte("short"), c.Canonical(), sig); err == nil {
		t.Error("verification accepted a malformed public key")
	}
}

// TestDeviceSig_ReplayBoundToOperationIdAndExpiry pins that the signature binds
// operation_id and expires_at: replaying it under a different operation_id or a
// pushed-out expiry does not verify, so a captured command cannot be reused.
func TestDeviceSig_ReplayBoundToOperationIdAndExpiry(t *testing.T) {
	ks := devKeyStore(t, stdMaterial())
	c := sampleCommand()
	sig := ks.SignCommand(c.Canonical())
	pub := ks.CommandSigningPublic()

	replayNewOp := c
	replayNewOp.OperationID = "op-0002"
	if bytes.Equal(replayNewOp.Canonical(), c.Canonical()) {
		t.Fatal("canonical encoding ignores operation_id")
	}
	if err := VerifyCommandSig(pub, replayNewOp.Canonical(), sig); err == nil {
		t.Error("signature verified under a different operation_id (replay)")
	}

	replayNewExpiry := c
	replayNewExpiry.ExpiresAt = c.ExpiresAt + 3600
	if bytes.Equal(replayNewExpiry.Canonical(), c.Canonical()) {
		t.Fatal("canonical encoding ignores expires_at")
	}
	if err := VerifyCommandSig(pub, replayNewExpiry.Canonical(), sig); err == nil {
		t.Error("signature verified under a pushed-out expiry (replay)")
	}

	// A different bound field (action) must also break verification.
	replayNewAction := c
	replayNewAction.Action = "launch"
	if err := VerifyCommandSig(pub, replayNewAction.Canonical(), sig); err == nil {
		t.Error("signature verified under a different action")
	}
}

// TestDeviceSig_CanonicalBindsAllFields pins that every field of the tuple is
// covered by the canonical encoding (a change to any one changes the bytes),
// so the signature commits to the whole command including content_hash.
func TestDeviceSig_CanonicalBindsAllFields(t *testing.T) {
	base := sampleCommand()
	base.ContentHash = bytes.Repeat([]byte{0xAB}, 32)
	ref := base.Canonical()

	mutate := map[string]func(*Command){
		"action":       func(c *Command) { c.Action = "interrupt" },
		"machine":      func(c *Command) { c.Machine = "endpoint-xyz" },
		"session":      func(c *Command) { c.Session = "sess-99" },
		"operation_id": func(c *Command) { c.OperationID = "op-9999" },
		"expires_at":   func(c *Command) { c.ExpiresAt = base.ExpiresAt + 1 },
		"content_hash": func(c *Command) { c.ContentHash = bytes.Repeat([]byte{0xCD}, 32) },
	}
	for name, mut := range mutate {
		c := base
		mut(&c)
		if bytes.Equal(c.Canonical(), ref) {
			t.Errorf("canonical encoding does not bind %s", name)
		}
	}
}
