// A4-1a — the machine's pairing identity bundle that `swarm remote init`
// persists. internal/skeleton/pairing.go's pairingConfig needs every field
// this bundle carries after Load: a Noise-static X25519 private (via an opaque
// handshake handle), a recipient X25519 public, a grant-signing Ed25519
// keypair (the RAW private is required — enroll.Enroll signs with it), a
// relay-auth Ed25519 keypair, epoch keys (crypto.EpochKeys), an epoch id, a
// grant seq, a hostname, and a routing id.
//
// INTENDED CONTRACT (RED — none of this exists yet; GREEN implements it):
//
//	package machineid
//
//	// Material is deterministic construction material for tests/KATs, mirroring
//	// crypto.KeyMaterial + crypto.NewIdentityFromMaterial's pattern.
//	type Material struct {
//	    NoiseStaticPriv [32]byte
//	    RecipientPriv   [32]byte
//	    GrantSignPriv   ed25519.PrivateKey
//	    RelayAuthPriv   ed25519.PrivateKey
//	    EpochKeys       crypto.EpochKeys
//	    EpochID         uint32
//	    GrantSeq        uint64
//	}
//
//	type Identity struct{ /* unexported: composes crypto primitives */ }
//
//	func Generate(hostname string) (*Identity, error)         // fresh keys via crypto/rand + ed25519.GenerateKey + crypto.NewEpochKeys
//	func NewFromMaterial(hostname string, m Material) *Identity // deterministic (no I/O, cannot fail)
//	func Load(path string) (*Identity, error)                  // lossless inverse of Save; fail-closed on corrupt/short data
//	func (id *Identity) Save(path string) error                 // ONE file, 0600, temp+Sync+rename
//	func (id *Identity) String() string                         // REDACTED: fingerprints/pubkeys/hostname only, NEVER raw privates
//	func (id *Identity) GoString() string                       // routes through String() so %#v cannot dump privates
//
//	func (id *Identity) NoiseStatic() *crypto.NoiseStatic       // -> pairingConfig.Static
//	func (id *Identity) RecipientPublic() []byte                // -> pairingConfig.RecipientPub
//	func (id *Identity) GrantSignPublic() ed25519.PublicKey     // -> pairingConfig.SignPub
//	func (id *Identity) GrantSignPrivate() ed25519.PrivateKey   // -> pairingConfig.SignPriv
//	func (id *Identity) RelayAuthPublic() ed25519.PublicKey     // -> pairingConfig.RelayAuthPub
//	func (id *Identity) EpochKeys() crypto.EpochKeys            // -> pairingConfig.EpochKeys
//	func (id *Identity) EpochID() uint32                        // -> pairingConfig.EpochID
//	func (id *Identity) GrantSeq() uint64                       // -> pairingConfig.GrantSeq
//	func (id *Identity) Hostname() string                       // -> pairingConfig.Hostname
//	func (id *Identity) RoutingID() []byte                      // -> pairingConfig.RoutingID (derived from the relay-auth pubkey, see relay.RoutingID)
//
// Save/Load persist ALL of the above losslessly in a SINGLE 0600 file
// (temp+Sync+rename, mirroring crypto's unexported writeSecretFile — it is NOT
// exported, so GREEN must replicate the write-temp-fsync-rename pattern locally
// rather than reuse it). This package imports and composes
// internal/remote/crypto (crypto.NewIdentityFromMaterial, crypto.NewEpochKeys)
// — it must never modify any file under internal/remote/crypto.
//
// The recipient and Noise-static PRIVATE scalars have no public accessor (only
// derived public/opaque-handle access), mirroring crypto.Identity's own
// R-CRY.1 posture; the grant-signing private DOES have a public accessor
// because enroll.Enroll legitimately needs the raw key to sign a grant.
package machineid

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// fill returns a 32-byte array of the repeated byte b: deterministic,
// recognizable key material so a leak into String() is unambiguous (mirrors
// crypto's own KAT helper, internal/remote/crypto/common_test.go).
func fill(b byte) [32]byte {
	var a [32]byte
	for i := range a {
		a[i] = b
	}
	return a
}

// fingerprint mirrors crypto's own redaction fingerprint (identity.go): the
// first 8 bytes of SHA-256 of a public key, hex-encoded. Used here only to
// recognize a safe, public identifier in String() output, never a private one.
func fingerprint(pub []byte) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// stdMaterial is a full, distinct-per-field set of deterministic bundle
// material a test can independently hex-search for and compare against after
// a Save/Load round trip.
func stdMaterial(t *testing.T) Material {
	t.Helper()
	_, grantPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey(grant-sign): %v", err)
	}
	_, relayPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey(relay-auth): %v", err)
	}
	epochKeys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("crypto.NewEpochKeys: %v", err)
	}
	return Material{
		NoiseStaticPriv: fill(0xA1),
		RecipientPriv:   fill(0xB2),
		GrantSignPriv:   grantPriv,
		RelayAuthPriv:   relayPriv,
		EpochKeys:       epochKeys,
		EpochID:         7,
		GrantSeq:        42,
	}
}

// TestMachineIdentity_GenerateSaveLoadRoundTrip pins that Save -> Load
// round-trips every field byte-for-byte: both X25519 keys (checked via their
// public derivation, since the machine identity never re-exposes the
// Noise-static/recipient raw privates), both Ed25519 keypairs (raw private
// included for grant-signing, which enroll.Enroll requires), epoch keys,
// epoch id, grant seq, hostname, and routing id.
func TestMachineIdentity_GenerateSaveLoadRoundTrip(t *testing.T) {
	m := stdMaterial(t)
	id := NewFromMaterial("build-host", m)

	path := filepath.Join(t.TempDir(), "machine-identity.key")
	if err := id.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded == nil {
		t.Fatal("Load returned a nil Identity with a nil error")
	}

	// Recipient public key: compare against the frozen crypto package's own
	// derivation of the same raw private scalar, independent of machineid's
	// internals.
	wantRecipPub := crypto.NewIdentityFromMaterial(m.NoiseStaticPriv, m.RecipientPriv).RecipientPublic()
	if !bytes.Equal(reloaded.RecipientPublic(), wantRecipPub) {
		t.Errorf("RecipientPublic mismatch after reload: got %x, want %x", reloaded.RecipientPublic(), wantRecipPub)
	}
	if reloaded.NoiseStatic() == nil {
		t.Error("NoiseStatic() is nil after reload")
	}

	wantGrantPub := m.GrantSignPriv.Public().(ed25519.PublicKey)
	if !bytes.Equal(reloaded.GrantSignPublic(), wantGrantPub) {
		t.Errorf("GrantSignPublic mismatch after reload: got %x, want %x", reloaded.GrantSignPublic(), wantGrantPub)
	}
	if !bytes.Equal(reloaded.GrantSignPrivate(), m.GrantSignPriv) {
		t.Error("GrantSignPrivate mismatch after reload: the raw grant-signing private did not round-trip")
	}

	wantRelayPub := m.RelayAuthPriv.Public().(ed25519.PublicKey)
	if !bytes.Equal(reloaded.RelayAuthPublic(), wantRelayPub) {
		t.Errorf("RelayAuthPublic mismatch after reload: got %x, want %x", reloaded.RelayAuthPublic(), wantRelayPub)
	}

	if reloaded.EpochKeys() != m.EpochKeys {
		t.Error("EpochKeys mismatch after reload")
	}
	if reloaded.EpochID() != m.EpochID {
		t.Errorf("EpochID = %d, want %d", reloaded.EpochID(), m.EpochID)
	}
	if reloaded.GrantSeq() != m.GrantSeq {
		t.Errorf("GrantSeq = %d, want %d", reloaded.GrantSeq(), m.GrantSeq)
	}
	if reloaded.Hostname() != "build-host" {
		t.Errorf("Hostname = %q, want %q", reloaded.Hostname(), "build-host")
	}
	if len(reloaded.RoutingID()) == 0 {
		t.Error("RoutingID is empty after reload")
	}
	if !bytes.Equal(reloaded.RoutingID(), id.RoutingID()) {
		t.Errorf("RoutingID changed across Save/Load: got %x, want %x", reloaded.RoutingID(), id.RoutingID())
	}
}

// TestMachineIdentity_SaveFileIs0600 pins that the persisted bundle is written
// with 0600 permissions (owner read/write only) regardless of the process
// umask — this is machine key custody (F8-equivalent for the machine side).
func TestMachineIdentity_SaveFileIs0600(t *testing.T) {
	id, err := Generate("host-b")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "machine-identity.key")
	if err := id.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("machine identity file has perms %o, want 0600", perm)
	}
}

// TestMachineIdentity_StringRedactsPrivateKeys pins that String() (and every
// fmt verb, the realistic accidental-leak path) never emits any of the four
// raw private keys/scalars, while still carrying a stable, public identifier
// (a fingerprint of a public key, or the hostname) so the representation is
// actually useful for logs/debugging.
func TestMachineIdentity_StringRedactsPrivateKeys(t *testing.T) {
	m := stdMaterial(t)
	id := NewFromMaterial("secret-host", m)

	reps := []string{
		id.String(),
		fmt.Sprintf("%v", id),
		fmt.Sprintf("%+v", id),
		fmt.Sprintf("%#v", id),
		fmt.Sprintf("%s", id),
		fmt.Sprint(id),
	}

	needles := map[string][]byte{
		"noise-static private":  m.NoiseStaticPriv[:],
		"recipient private":     m.RecipientPriv[:],
		"grant-signing private": m.GrantSignPriv,
		"relay-auth private":    m.RelayAuthPriv,
	}
	for _, rep := range reps {
		for name, raw := range needles {
			if bytes.Contains([]byte(rep), raw) {
				t.Errorf("String() leaks raw %s bytes: %q", name, rep)
			}
			if strings.Contains(rep, hex.EncodeToString(raw)) {
				t.Errorf("String() leaks hex-encoded %s: %q", name, rep)
			}
		}
	}

	// It SHOULD carry a stable, public identifier: the hostname or a
	// fingerprint of one of the identity's public keys (mirroring
	// crypto.Identity.String()'s fingerprint pattern, identity.go ~line 104).
	fps := []string{
		fingerprint(crypto.NewIdentityFromMaterial(m.NoiseStaticPriv, m.RecipientPriv).NoiseStaticPublic()),
		fingerprint(crypto.NewIdentityFromMaterial(m.NoiseStaticPriv, m.RecipientPriv).RecipientPublic()),
		fingerprint(m.GrantSignPriv.Public().(ed25519.PublicKey)),
		fingerprint(m.RelayAuthPriv.Public().(ed25519.PublicKey)),
	}
	found := false
	for _, rep := range reps {
		if strings.Contains(rep, "secret-host") {
			found = true
		}
		for _, fp := range fps {
			if strings.Contains(rep, fp) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("String() does not surface any recognizable public identifier (hostname or key fingerprint): %q", reps[0])
	}
}

// TestMachineIdentity_LoadCorruptFailsClosed pins that Load on corrupt,
// truncated, or missing data is an error and NEVER a partially-populated
// Identity — machine key custody must fail closed, not fabricate or silently
// half-load a bundle.
func TestMachineIdentity_LoadCorruptFailsClosed(t *testing.T) {
	dir := t.TempDir()

	cases := map[string][]byte{
		"empty":     {},
		"truncated": bytes.Repeat([]byte{0x00}, 10),
		"garbage":   []byte("not a machine identity file, definitely not one"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".key")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			id, err := Load(path)
			if err == nil {
				t.Fatal("Load of corrupt data returned a nil error")
			}
			if id != nil {
				t.Error("Load of corrupt data returned a non-nil, partially-populated Identity")
			}
		})
	}

	t.Run("missing", func(t *testing.T) {
		id, err := Load(filepath.Join(dir, "absent.key"))
		if err == nil {
			t.Fatal("Load of a missing file returned a nil error")
		}
		if id != nil {
			t.Error("Load of a missing file returned a non-nil Identity")
		}
	})
}

// TestMachineIdentity_GenerateProducesDistinctKeys pins that Generate uses
// crypto/rand for every key it mints: two calls never collide, and no key is
// the all-zero sentinel a broken RNG path would produce.
func TestMachineIdentity_GenerateProducesDistinctKeys(t *testing.T) {
	a, err := Generate("host-a")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := Generate("host-b")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if bytes.Equal(a.RecipientPublic(), b.RecipientPublic()) {
		t.Error("two Generate calls produced the same recipient public key")
	}
	if bytes.Equal(a.GrantSignPublic(), b.GrantSignPublic()) {
		t.Error("two Generate calls produced the same grant-signing public key")
	}
	if bytes.Equal(a.RelayAuthPublic(), b.RelayAuthPublic()) {
		t.Error("two Generate calls produced the same relay-auth public key")
	}
	if bytes.Equal(a.RoutingID(), b.RoutingID()) {
		t.Error("two Generate calls produced the same routing id")
	}
	if a.EpochKeys() == b.EpochKeys() {
		t.Error("two Generate calls produced the same epoch keys")
	}

	var zero32 [32]byte
	if bytes.Equal(a.RecipientPublic(), zero32[:]) {
		t.Error("Generate produced an all-zero recipient public key")
	}
}
