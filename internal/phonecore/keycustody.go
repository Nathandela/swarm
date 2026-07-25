package phonecore

// PB-KEY-9(b) / PB-SEC-1: the phone core's key material at rest, sealed under KEKs the Go
// core never holds.
//
// <stateDir>/device.key used to be 128 RAW bytes -- the four device private scalars
// concatenated, one file, one tier, in the clear. 0600 is a filesystem permission, not a
// seal: it is worth nothing against ADB backup or a restored image. The Android side cannot
// fix that, because Go opens this file for itself at Resume; so the KEK is injected here
// instead, as a Sealer per PB-KEY-2 TIER.
//
// ONE SEALER WOULD NOT BE ENOUGH. Under the wake KEK -- which must open with no user
// present, because a push arrives with nobody there -- the content material is reachable
// without the biometric, which is exactly the collapse the plaintext file already had.
// Under the content KEK the wake path cannot start at all. So the roles split by PB-KEY-5:
// NoiseStatic, Recipient and CommandSign are content tier; RelayAuth is wake tier.

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// ErrNoSealer refuses a Resume that would write key material at rest with no KEK over it
// (ADR-007 B18(c)). The alternative -- sealing anyway under a KEK derived from something on
// the same disk -- satisfies the letter of PB-SEC-1 while the property stays unmet, so
// unsealed custody must be a NAMED choice at the call site (InsecureCleartextSealer), never
// something reached by omitting a field.
var ErrNoSealer = errors.New("phonecore: no key-custody sealer supplied")

// ErrPublicKeyMismatch refuses a device.key whose CLEARTEXT public half disagrees with the
// material actually sealed inside it. It is a distinct diagnosis from a KEK that will not
// open the container: the seals are intact and only the unauthenticated claim about them
// changed, which means the app's private data directory was written to.
var ErrPublicKeyMismatch = errors.New("phonecore: device.key public keys disagree with the sealed material")

// Sealer wraps key material under a KEK held OUTSIDE the Go core. It is the single inbound
// crossing ADR-007 B8 pins: the KEK comes in behind this interface and key material never
// goes out. There is deliberately no accessor for the KEK -- an outbound crossing is what
// B8 forbids and B17 declined to widen.
type Sealer interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

const (
	// deviceKeyFileName is the device key blob inside the state directory. The name is
	// unchanged across the seam; only its contents are now sealed.
	deviceKeyFileName = "device.key"
	// deviceKeyVersion stamps the sealed container so a pre-seam blob is recognisable.
	deviceKeyVersion = 1
	// legacyDeviceKeyLen is the pre-seam layout: four raw 32-byte scalars.
	legacyDeviceKeyLen = 128
	// contentTierLen is the content tier's plaintext: noise-static || recipient ||
	// command-sign seed.
	contentTierLen = 96
)

// sealedDeviceKeys is the on-disk shape of device.key once custody is in force.
//
// The four PUBLIC keys travel in the clear. They are public by definition, and
// crypto.KeyStore's public accessors are errorless, so they must answer while a tier is
// locked -- a phone that cannot state its own relay routing id cannot receive the push that
// asks the user to unlock.
type sealedDeviceKeys struct {
	Version        int    `json:"v"`
	NoiseStaticPub []byte `json:"noise_static_pub"`
	RecipientPub   []byte `json:"recipient_pub"`
	CommandPub     []byte `json:"command_pub"`
	RelayAuthPub   []byte `json:"relay_auth_pub"`
	Content        []byte `json:"content"` // sealed: noise-static || recipient || command seed
	Wake           []byte `json:"wake"`    // sealed: relay-auth seed
}

// openKeyStore recovers the device keys from dir, generating them on first launch. They
// must be the SAME keys across a restart: the daemon registry pins the device id to the
// command-signing public key (R-DEV.1), so regenerating them would invalidate every command
// the phone signs and every grant addressed to it.
//
// An empty dir persists nothing at all, so there is nothing at rest to seal and no sealer
// is required: the material is generated per Resume and lives only in memory. That wiring is
// for a caller that injects its own Store; production always provisions a state directory.
func openKeyStore(dir string, wake, content Sealer) (crypto.KeyStore, error) {
	if dir == "" {
		m, err := newKeyMaterial()
		if err != nil {
			return nil, err
		}
		return crypto.NewKeyStoreFromMaterial(m), nil
	}
	if wake == nil || content == nil {
		return nil, fmt.Errorf("%w: %s must be sealed under both tier KEKs", ErrNoSealer, deviceKeyFileName)
	}

	path := filepath.Join(dir, deviceKeyFileName)
	buf, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		m, gerr := newKeyMaterial()
		if gerr != nil {
			return nil, gerr
		}
		return sealDeviceKeys(path, m, wake, content)
	}
	if err != nil {
		return nil, fmt.Errorf("open device keys: %w", err)
	}

	var f sealedDeviceKeys
	if json.Unmarshal(buf, &f) == nil && f.Version == deviceKeyVersion {
		return openSealedDeviceKeys(f, wake, content)
	}
	// A device.key written before the seam existed. An installed app already has one, so
	// Resume must RE-SEAL what it finds rather than only sealing what it creates:
	// generating fresh material here would silently change the device identity.
	if len(buf) != legacyDeviceKeyLen {
		return nil, fmt.Errorf("open device keys: %s is neither a sealed container nor the %d-byte pre-seam blob",
			path, legacyDeviceKeyLen)
	}
	var m crypto.KeyMaterial
	copy(m.NoiseStaticPriv[:], buf[0:32])
	copy(m.RecipientPriv[:], buf[32:64])
	copy(m.CommandSignSeed[:], buf[64:96])
	copy(m.RelayAuthSeed[:], buf[96:128])
	return sealDeviceKeys(path, m, wake, content)
}

// sealDeviceKeys splits m by tier, seals each half under its own KEK and writes the
// container atomically at 0600.
func sealDeviceKeys(path string, m crypto.KeyMaterial, wake, content Sealer) (crypto.KeyStore, error) {
	var tier [contentTierLen]byte
	copy(tier[0:32], m.NoiseStaticPriv[:])
	copy(tier[32:64], m.RecipientPriv[:])
	copy(tier[64:96], m.CommandSignSeed[:])

	contentBlob, err := content.Seal(tier[:])
	if err != nil {
		return nil, fmt.Errorf("seal content key custody: %w", err)
	}
	wakeBlob, err := wake.Seal(m.RelayAuthSeed[:])
	if err != nil {
		return nil, fmt.Errorf("seal wake key custody: %w", err)
	}

	// The publics are derived through the same construction the tier stores use, so what is
	// WRITTEN here always agrees with the sealed half. That says nothing about what is read
	// back: this file's cleartext half is unauthenticated and PB-SEC-1's adversary can write
	// it, so checkPublic re-derives and compares at every unseal.
	pub := crypto.NewKeyStoreFromMaterial(m)
	f := sealedDeviceKeys{
		Version:        deviceKeyVersion,
		NoiseStaticPub: pub.NoiseStaticPublic(),
		RecipientPub:   pub.RecipientPublic(),
		CommandPub:     pub.CommandSigningPublic(),
		RelayAuthPub:   pub.RelayAuthPublic(),
		Content:        contentBlob,
		Wake:           wakeBlob,
	}
	data, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, ".device-key-*", data); err != nil {
		return nil, err
	}
	return newSealedKeyStore(f, wake, content), nil
}

// openSealedDeviceKeys validates a sealed container against the injected KEKs.
func openSealedDeviceKeys(f sealedDeviceKeys, wake, content Sealer) (crypto.KeyStore, error) {
	ks := newSealedKeyStore(f, wake, content)
	// The WAKE tier opens with no user present -- that is its whole purpose -- so one that
	// will not open means the blob is not ours (a restored image, another device's
	// directory, a rotated Keystore key). Fail closed: starting from zero here regenerates
	// the device identity and silently unpairs the phone.
	if _, err := ks.wakeStore(); err != nil {
		return nil, fmt.Errorf("unseal wake key custody: %w", err)
	}
	// The CONTENT tier legitimately refuses: the phone comes up on a push before any
	// biometric. Only a refusal that is not a custody verdict says the blob is not ours;
	// a locked or invalidated tier is surfaced per operation, where the caller can act.
	// ErrPublicKeyMismatch is deliberately in the fatal set: the seals opened fine and the
	// container still lied about what is in them, so the directory has been written to.
	if _, err := ks.contentStore(); err != nil &&
		!errors.Is(err, crypto.ErrKeyAuthRequired) && !errors.Is(err, crypto.ErrKeyInvalidated) {
		return nil, fmt.Errorf("unseal content key custody: %w", err)
	}
	return ks, nil
}

// sealedKeyStore is the device's crypto.KeyStore with its private material at rest under
// two tier KEKs. It holds NO unwrapped material: every operation unseals its own tier, uses
// it once and drops it.
type sealedKeyStore struct {
	wake, content Sealer
	wakeBlob      []byte
	contentBlob   []byte

	noiseStaticPub []byte
	recipientPub   []byte
	commandPub     []byte
	relayAuthPub   []byte
}

func newSealedKeyStore(f sealedDeviceKeys, wake, content Sealer) *sealedKeyStore {
	return &sealedKeyStore{
		wake: wake, content: content,
		wakeBlob: f.Wake, contentBlob: f.Content,
		noiseStaticPub: f.NoiseStaticPub, recipientPub: f.RecipientPub,
		commandPub: f.CommandPub, relayAuthPub: f.RelayAuthPub,
	}
}

// contentStore unseals the content tier and builds a store over it FOR ONE OPERATION.
// Nothing is memoized: an auth-gated key re-checks authorisation on every use, and a store
// that unwrapped once would keep signing after the screen locks (PB-KEY-7) while every
// restart-based test still passed. The relay-auth seed is deliberately left zero here --
// this instance answers only content-tier operations, so its RelayAuthPublic is not the
// device's and is deliberately NOT among the publics checked below.
func (k *sealedKeyStore) contentStore() (crypto.KeyStore, error) {
	plain, err := k.content.Open(k.contentBlob)
	if err != nil {
		return nil, err
	}
	if len(plain) != contentTierLen {
		return nil, errors.New("phonecore: sealed content key tier is malformed")
	}
	var m crypto.KeyMaterial
	copy(m.NoiseStaticPriv[:], plain[0:32])
	copy(m.RecipientPriv[:], plain[32:64])
	copy(m.CommandSignSeed[:], plain[64:96])
	inner := crypto.NewKeyStoreFromMaterial(m)
	if err := checkPublic("noise_static_pub", k.noiseStaticPub, inner.NoiseStaticPublic()); err != nil {
		return nil, err
	}
	if err := checkPublic("recipient_pub", k.recipientPub, inner.RecipientPublic()); err != nil {
		return nil, err
	}
	if err := checkPublic("command_pub", k.commandPub, inner.CommandSigningPublic()); err != nil {
		return nil, err
	}
	return inner, nil
}

// wakeStore is contentStore's mirror for the wake tier: the relay-auth seed alone.
func (k *sealedKeyStore) wakeStore() (crypto.KeyStore, error) {
	plain, err := k.wake.Open(k.wakeBlob)
	if err != nil {
		return nil, err
	}
	if len(plain) != 32 {
		return nil, errors.New("phonecore: sealed wake key tier is malformed")
	}
	var m crypto.KeyMaterial
	copy(m.RelayAuthSeed[:], plain)
	inner := crypto.NewKeyStoreFromMaterial(m)
	if err := checkPublic("relay_auth_pub", k.relayAuthPub, inner.RelayAuthPublic()); err != nil {
		return nil, err
	}
	return inner, nil
}

// checkPublic re-derives one public key from the material just unsealed and compares it to
// the cleartext copy the container carries.
//
// It runs HERE, at the unseal, and not at the accessors, because the accessors are errorless
// by design -- they must answer with a tier locked, since a phone that cannot state its own
// relay routing id cannot receive the push that asks the user to unlock. The unseal is the
// earliest moment the private material exists, and it is a moment that can say no.
//
// That places the wake public's check at load (openSealedDeviceKeys unseals the wake tier
// unconditionally, so a forged relay_auth_pub never reaches a caller) and the three content
// publics' check at the first content-tier operation -- which is also load whenever the tier
// is unlocked. With the tier LOCKED the check cannot run at all, and nothing weaker would be
// honest: the material to check against is exactly what the lock withholds. Every content
// operation refuses in that state anyway, and mobile/pairing.go stops at NoiseStatic before
// it reads a single public, so a forged content public cannot be enrolled while locked.
func checkPublic(field string, claimed, derived []byte) error {
	if bytes.Equal(claimed, derived) {
		return nil
	}
	return fmt.Errorf("%w: %s claims %x, the sealed material derives %x", ErrPublicKeyMismatch, field, claimed, derived)
}

func (k *sealedKeyStore) NoiseStaticPublic() []byte {
	return append([]byte(nil), k.noiseStaticPub...)
}
func (k *sealedKeyStore) RecipientPublic() []byte { return append([]byte(nil), k.recipientPub...) }
func (k *sealedKeyStore) CommandSigningPublic() []byte {
	return append([]byte(nil), k.commandPub...)
}
func (k *sealedKeyStore) RelayAuthPublic() []byte { return append([]byte(nil), k.relayAuthPub...) }

func (k *sealedKeyStore) NoiseStatic() (*crypto.NoiseStatic, error) {
	inner, err := k.contentStore()
	if err != nil {
		return nil, err
	}
	return inner.NoiseStatic()
}

func (k *sealedKeyStore) OpenSealedBox(sealed []byte) ([]byte, error) {
	inner, err := k.contentStore()
	if err != nil {
		return nil, err
	}
	return inner.OpenSealedBox(sealed)
}

func (k *sealedKeyStore) SignCommand(msg []byte) ([]byte, error) {
	inner, err := k.contentStore()
	if err != nil {
		return nil, err
	}
	return inner.SignCommand(msg)
}

func (k *sealedKeyStore) SignRelayAuth(challenge []byte) ([]byte, error) {
	inner, err := k.wakeStore()
	if err != nil {
		return nil, err
	}
	return inner.SignRelayAuth(challenge)
}

// newKeyMaterial mints fresh device material. crypto.NewFileKeyStore does the same thing
// around its own file write, which is precisely the write this seam takes over.
func newKeyMaterial() (crypto.KeyMaterial, error) {
	var m crypto.KeyMaterial
	for _, s := range [][]byte{m.NoiseStaticPriv[:], m.RecipientPriv[:], m.CommandSignSeed[:], m.RelayAuthSeed[:]} {
		if _, err := rand.Read(s); err != nil {
			return crypto.KeyMaterial{}, err
		}
	}
	return m, nil
}

// InsecureCleartextSealer returns a Sealer that DOES NOT SEAL: Seal and Open are the
// identity function, so every byte handed to it is written to disk in the clear.
//
// It exists so unsealed custody is a NAMED choice visible at the call site rather than
// something reached by omitting a field (ADR-007 B18(c)). The gomobile facade uses it
// today because the Android side cannot reach phonecore.Config -- gomobile cannot set a Go
// struct field and mobile.Config is golden-pinned with no verb for a sealer. S14 adds that
// verb and replaces this call; PB-KEY-9 is not delivered until it does.
func InsecureCleartextSealer() Sealer { return cleartextSealer{} }

type cleartextSealer struct{}

func (cleartextSealer) Seal(plaintext []byte) ([]byte, error) {
	return append([]byte(nil), plaintext...), nil
}
func (cleartextSealer) Open(sealed []byte) ([]byte, error) {
	return append([]byte(nil), sealed...), nil
}
