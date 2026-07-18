// R-CRY.1 — machine identity keypair generation + storage.
//
// FROZEN CONTRACT (subset):
//
//	func GenerateIdentity() (*Identity, error)
//	func NewIdentityFromMaterial(noiseStaticPriv, recipientPriv [32]byte) *Identity
//	func LoadIdentity(dir string) (*Identity, error)
//	func (*Identity) Save(dir string) error
//	func (*Identity) NoiseStaticPublic() []byte
//	func (*Identity) RecipientPublic() []byte
//
// Per D.0-A14 a party holds TWO X25519 keys (Noise-static + sealed-box
// recipient); the machine identity persists both. The private scalars are
// written 0600 and never logged/printed/transmitted (R-CRY.1).
package crypto

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestIdentity_KeyfilePerms0600 pins that the persisted private key material is
// written with 0600 permissions (owner read/write only).
func TestIdentity_KeyfilePerms0600(t *testing.T) {
	dir := t.TempDir()
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := id.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var found int
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		found++
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("key file %s has perms %o, want 0600", path, perm)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if found == 0 {
		t.Fatal("Save wrote no files")
	}
}

// TestIdentity_NoPrivateKeyInLogs pins that formatting/printing an Identity
// (the realistic accidental-leak path, e.g. log.Printf("%v", id)) never emits
// the private scalar under any fmt verb. The keyfile legitimately holds the
// private key; a logged representation must not.
func TestIdentity_NoPrivateKeyInLogs(t *testing.T) {
	noise := fill(0xA1)
	recip := fill(0xB2)
	id := NewIdentityFromMaterial(noise, recip)

	reps := []string{
		fmt.Sprintf("%v", id),
		fmt.Sprintf("%+v", id),
		fmt.Sprintf("%#v", id),
		fmt.Sprintf("%s", id),
		fmt.Sprint(id),
	}
	// Search for the private scalars in raw, hex, and a long recognizable run.
	needles := [][]byte{
		noise[:], recip[:],
		[]byte(hex.EncodeToString(noise[:])),
		[]byte(hex.EncodeToString(recip[:])),
		bytes.Repeat([]byte{0xA1}, 16),
		bytes.Repeat([]byte{0xB2}, 16),
	}
	for _, rep := range reps {
		for _, n := range needles {
			if bytes.Contains([]byte(rep), n) {
				t.Errorf("formatted Identity leaks private material: %q", rep)
			}
		}
	}
}

// TestIdentity_RestartReloadsSamePubkey pins that Save then LoadIdentity yields
// the identical public keys (persistence round-trips, R-CRY.1 "restart reloads
// same pubkey").
func TestIdentity_RestartReloadsSamePubkey(t *testing.T) {
	dir := t.TempDir()
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := id.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := LoadIdentity(dir)
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if !bytes.Equal(id.NoiseStaticPublic(), reloaded.NoiseStaticPublic()) {
		t.Error("Noise-static public key changed across reload")
	}
	if !bytes.Equal(id.RecipientPublic(), reloaded.RecipientPublic()) {
		t.Error("recipient public key changed across reload")
	}
	if len(id.NoiseStaticPublic()) != 32 || len(id.RecipientPublic()) != 32 {
		t.Errorf("X25519 public keys must be 32 bytes, got %d/%d",
			len(id.NoiseStaticPublic()), len(id.RecipientPublic()))
	}
	// The two keys are distinct (A14: not one key reused for both roles).
	if bytes.Equal(id.NoiseStaticPublic(), id.RecipientPublic()) {
		t.Error("Noise-static and recipient keys must be distinct (A14)")
	}
}

// TestIdentity_LoadMissingFails pins that loading from a dir with no identity
// is an error, not a silent fresh key.
func TestIdentity_LoadMissingFails(t *testing.T) {
	if _, err := LoadIdentity(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("LoadIdentity of a missing identity must error, not fabricate a key")
	}
}
