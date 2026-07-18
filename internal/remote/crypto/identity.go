package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/curve25519"
)

// identityFile holds the two X25519 private scalars (noise-static || recipient),
// 64 bytes, written 0600. The public keys are derived on load; only privates are
// persisted (R-CRY.1).
const identityFile = "identity.key"

// Identity is a machine's long-term cryptographic identity: two distinct X25519
// keys (A14) — a Noise-static key for the live handshake and a sealed-box
// recipient key for async epoch grants. The private scalars never appear in any
// formatted representation (R-CRY.1); see String/GoString.
type Identity struct {
	noiseStaticPriv [32]byte
	recipientPriv   [32]byte
	noiseStaticPub  [32]byte
	recipientPub    [32]byte
}

// pubFromPriv derives the X25519 public key for a private scalar. With the
// standard base point this cannot return the all-zero error (that requires a
// low-order point), so the error is discarded for our own generated material.
func pubFromPriv(priv [32]byte) [32]byte {
	pub, _ := curve25519.X25519(priv[:], curve25519.Basepoint)
	var out [32]byte
	copy(out[:], pub)
	return out
}

// GenerateIdentity creates a fresh machine identity from crypto/rand.
func GenerateIdentity() (*Identity, error) {
	var ns, rp [32]byte
	if _, err := rand.Read(ns[:]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(rp[:]); err != nil {
		return nil, err
	}
	return NewIdentityFromMaterial(ns, rp), nil
}

// NewIdentityFromMaterial builds an identity from explicit private scalars
// (deterministic construction for tests/KATs).
func NewIdentityFromMaterial(noiseStaticPriv, recipientPriv [32]byte) *Identity {
	return &Identity{
		noiseStaticPriv: noiseStaticPriv,
		recipientPriv:   recipientPriv,
		noiseStaticPub:  pubFromPriv(noiseStaticPriv),
		recipientPub:    pubFromPriv(recipientPriv),
	}
}

// LoadIdentity reads a persisted identity. A missing identity is an error, not a
// silently fabricated fresh key.
func LoadIdentity(dir string) (*Identity, error) {
	buf, err := os.ReadFile(filepath.Join(dir, identityFile))
	if err != nil {
		return nil, err
	}
	if len(buf) != 64 {
		return nil, fmt.Errorf("crypto: identity key file malformed (%d bytes)", len(buf))
	}
	var ns, rp [32]byte
	copy(ns[:], buf[:32])
	copy(rp[:], buf[32:])
	return NewIdentityFromMaterial(ns, rp), nil
}

// Save writes the private scalars 0600 under dir.
func (id *Identity) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var buf [64]byte
	copy(buf[:32], id.noiseStaticPriv[:])
	copy(buf[32:], id.recipientPriv[:])
	return writeSecretFile(filepath.Join(dir, identityFile), buf[:])
}

// NoiseStaticPublic returns a copy of the 32-byte Noise-static public key.
func (id *Identity) NoiseStaticPublic() []byte { return append([]byte(nil), id.noiseStaticPub[:]...) }

// RecipientPublic returns a copy of the 32-byte sealed-box recipient public key.
func (id *Identity) RecipientPublic() []byte { return append([]byte(nil), id.recipientPub[:]...) }

// NoiseStatic returns the opaque handshake handle for the Noise-static key. It
// carries the private scalar for the DH but exposes no accessor to it.
func (id *Identity) NoiseStatic() *NoiseStatic {
	return newNoiseStatic(id.noiseStaticPriv, id.noiseStaticPub)
}

// String is a redacted representation: public-key fingerprints only, never the
// private scalars (R-CRY.1). It backs %v, %s, %+v and fmt.Sprint.
func (id *Identity) String() string {
	return fmt.Sprintf("crypto.Identity{noiseStatic:%s recipient:%s}",
		fingerprint(id.noiseStaticPub[:]), fingerprint(id.recipientPub[:]))
}

// GoString backs %#v so it, too, cannot dump the private fields.
func (id *Identity) GoString() string { return id.String() }

// fingerprint is the first 8 bytes of SHA-256 of a public key, hex-encoded — a
// safe, non-secret identifier derived only from public material.
func fingerprint(pub []byte) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}
