package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/nacl/box"
)

// deviceKeyFile holds the four device private scalars, 128 bytes, written 0600:
// noise-static || recipient || command-sign-seed || relay-auth-seed.
const deviceKeyFile = "device.key"

var (
	// ErrBadRecipient is returned for a malformed recipient public key.
	ErrBadRecipient = errors.New("crypto: recipient public key must be 32 bytes")
	// ErrSealedOpen is returned when a sealed box fails to open.
	ErrSealedOpen = errors.New("crypto: sealed box open failed")
)

// KeyMaterial is the raw private material for a device KeyStore. The two X25519
// keys (A14) plus the Ed25519 command-signing (D4) and relay-auth (R-CRY.3)
// seeds.
type KeyMaterial struct {
	NoiseStaticPriv [32]byte
	RecipientPriv   [32]byte
	CommandSignSeed [32]byte
	RelayAuthSeed   [32]byte
}

// String/GoString/Format redact KeyMaterial under every fmt verb — it is all
// private seeds, so there is nothing safe to print (F2). Value receiver so a
// KeyMaterial value or pointer, and any struct embedding it, are all covered.
func (KeyMaterial) String() string               { return "crypto.KeyMaterial{REDACTED}" }
func (m KeyMaterial) GoString() string           { return m.String() }
func (m KeyMaterial) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, m.String()) }

// KeyStore is the device's key custody boundary. It exposes DH (via the opaque
// Noise-static handle), sealed-box open, and detached signatures — but NEVER the
// raw private scalar (R-CRY.2). A hardware-gated impl must drop in with
// bit-identical wire output (R-CRY.15).
type KeyStore interface {
	NoiseStaticPublic() []byte
	RecipientPublic() []byte
	NoiseStatic() *NoiseStatic
	OpenSealedBox(sealed []byte) ([]byte, error)
	SignCommand(msg []byte) []byte
	CommandSigningPublic() []byte
	SignRelayAuth(challenge []byte) []byte
	RelayAuthPublic() []byte
}

// fileKeyStore is the 0600 file-backed KeyStore. Public keys and derived
// Ed25519 keys are precomputed; private scalars live only in m.
type fileKeyStore struct {
	m KeyMaterial

	noiseStaticPub [32]byte
	recipientPub   [32]byte
	commandPriv    ed25519.PrivateKey
	commandPub     ed25519.PublicKey
	relayPriv      ed25519.PrivateKey
	relayPub       ed25519.PublicKey
}

// NewFileKeyStore generates fresh material and persists it 0600 under dir.
func NewFileKeyStore(dir string) (KeyStore, error) {
	var m KeyMaterial
	for _, s := range [][]byte{m.NoiseStaticPriv[:], m.RecipientPriv[:], m.CommandSignSeed[:], m.RelayAuthSeed[:]} {
		if _, err := rand.Read(s); err != nil {
			return nil, err
		}
	}
	return NewFileKeyStoreFromMaterial(dir, m)
}

// NewFileKeyStoreFromMaterial persists explicit material 0600 and returns the
// store (deterministic construction for tests/KATs).
func NewFileKeyStoreFromMaterial(dir string, m KeyMaterial) (KeyStore, error) {
	if err := writeMaterial(dir, m); err != nil {
		return nil, err
	}
	return newFileKeyStore(m), nil
}

// OpenFileKeyStore loads persisted material; a missing file is an error.
func OpenFileKeyStore(dir string) (KeyStore, error) {
	buf, err := os.ReadFile(filepath.Join(dir, deviceKeyFile))
	if err != nil {
		return nil, err
	}
	if len(buf) != 128 {
		return nil, errors.New("crypto: device key file malformed")
	}
	var m KeyMaterial
	copy(m.NoiseStaticPriv[:], buf[0:32])
	copy(m.RecipientPriv[:], buf[32:64])
	copy(m.CommandSignSeed[:], buf[64:96])
	copy(m.RelayAuthSeed[:], buf[96:128])
	return newFileKeyStore(m), nil
}

func writeMaterial(dir string, m KeyMaterial) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var buf [128]byte
	copy(buf[0:32], m.NoiseStaticPriv[:])
	copy(buf[32:64], m.RecipientPriv[:])
	copy(buf[64:96], m.CommandSignSeed[:])
	copy(buf[96:128], m.RelayAuthSeed[:])
	return writeSecretFile(filepath.Join(dir, deviceKeyFile), buf[:])
}

func newFileKeyStore(m KeyMaterial) *fileKeyStore {
	k := &fileKeyStore{m: m}
	k.noiseStaticPub = pubFromPriv(m.NoiseStaticPriv)
	k.recipientPub = pubFromPriv(m.RecipientPriv)
	k.commandPriv = ed25519.NewKeyFromSeed(m.CommandSignSeed[:])
	k.commandPub = k.commandPriv.Public().(ed25519.PublicKey)
	k.relayPriv = ed25519.NewKeyFromSeed(m.RelayAuthSeed[:])
	k.relayPub = k.relayPriv.Public().(ed25519.PublicKey)
	return k
}

// String/GoString/Format redact the store under every fmt verb: only the four
// public-key fingerprints, never the private material in m or the derived
// Ed25519 privates (F2).
func (k *fileKeyStore) String() string {
	return fmt.Sprintf("crypto.fileKeyStore{noiseStatic:%s recipient:%s command:%s relay:%s}",
		fingerprint(k.noiseStaticPub[:]), fingerprint(k.recipientPub[:]),
		fingerprint(k.commandPub), fingerprint(k.relayPub))
}
func (k *fileKeyStore) GoString() string           { return k.String() }
func (k *fileKeyStore) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, k.String()) }

func (k *fileKeyStore) NoiseStaticPublic() []byte { return append([]byte(nil), k.noiseStaticPub[:]...) }
func (k *fileKeyStore) RecipientPublic() []byte   { return append([]byte(nil), k.recipientPub[:]...) }
func (k *fileKeyStore) CommandSigningPublic() []byte {
	return append([]byte(nil), k.commandPub...)
}
func (k *fileKeyStore) RelayAuthPublic() []byte { return append([]byte(nil), k.relayPub...) }

// NoiseStatic returns the opaque handshake handle for the Noise-static key.
func (k *fileKeyStore) NoiseStatic() *NoiseStatic {
	return newNoiseStatic(k.m.NoiseStaticPriv, k.noiseStaticPub)
}

// SignCommand returns a detached Ed25519 signature over the canonical command
// bytes (D4). Ed25519 is deterministic: identical material -> identical sig.
func (k *fileKeyStore) SignCommand(msg []byte) []byte { return ed25519.Sign(k.commandPriv, msg) }

// SignRelayAuth signs a relay challenge with the relay-auth key (R-CRY.3).
func (k *fileKeyStore) SignRelayAuth(challenge []byte) []byte {
	return ed25519.Sign(k.relayPriv, challenge)
}

// OpenSealedBox opens a crypto_box_seal artifact addressed to this device's
// recipient key.
func (k *fileKeyStore) OpenSealedBox(sealed []byte) ([]byte, error) {
	var priv [32]byte
	copy(priv[:], k.m.RecipientPriv[:])
	out, ok := box.OpenAnonymous(nil, sealed, &k.recipientPub, &priv)
	if !ok {
		return nil, ErrSealedOpen
	}
	return out, nil
}

// SealToRecipient seals plaintext to a recipient X25519 public key using
// box.SealAnonymous (libsodium crypto_box_seal-compatible). The anonymous
// sender means no signature oracle; integrity/authenticity of the payload is
// the caller's concern (here, an authenticated Noise channel).
func SealToRecipient(recipientPub, plaintext []byte) ([]byte, error) {
	if len(recipientPub) != 32 {
		return nil, ErrBadRecipient
	}
	var pub [32]byte
	copy(pub[:], recipientPub)
	return box.SealAnonymous(nil, plaintext, &pub, rand.Reader)
}
