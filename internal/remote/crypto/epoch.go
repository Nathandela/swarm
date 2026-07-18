package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"sync"
)

// NewEpochKeys generates a fresh, independent wake key and content key with
// crypto/rand (A15/F10). The content key is never derived from the wake key, so
// an NSE holding only the wake key cannot recover session content.
func NewEpochKeys() (EpochKeys, error) {
	var k EpochKeys
	if _, err := rand.Read(k.WakeKey[:]); err != nil {
		return EpochKeys{}, err
	}
	if _, err := rand.Read(k.ContentKey[:]); err != nil {
		return EpochKeys{}, err
	}
	return k, nil
}

// Epoch-grant errors (F3).
var (
	// ErrBadGrant is returned when an opened grant is not the expected size.
	ErrBadGrant = errors.New("crypto: malformed epoch grant")
	// ErrGrantSig is returned when a grant is not signed by the pinned machine key.
	ErrGrantSig = errors.New("crypto: epoch grant signature invalid")
	// ErrGrantCoord is returned when a grant's outer routing coordinates do not
	// match the coordinates authenticated inside the sealed plaintext.
	ErrGrantCoord = errors.New("crypto: epoch grant coordinates mismatch")
	// ErrGrantReplay is returned when a grant is a replay of, or older than, the
	// highest (epoch_id, grant_seq) this device has already accepted.
	ErrGrantReplay = errors.New("crypto: epoch grant replay or stale")
)

const (
	grantVersion = 0x01
	// inner sealed plaintext: version(1) | recipient_key_id(8) | epoch_id(4) |
	// grant_seq(8) | wake(32) | content(32).
	grantInnerLen  = 1 + 8 + 4 + 8 + 32 + 32
	grantSigDomain = "swarm-remote/1 grant"
)

// EpochKeys is the wake/content key split delivered per epoch (A15). The wake
// key is after-first-unlock / NSE-readable and opens only content-free push
// wakes; the content key is biometric-gated, not NSE-readable, and not
// derivable from the wake key.
type EpochKeys struct {
	WakeKey    WakeKey
	ContentKey ContentKey
}

// WakeKey decrypts only content-free push wakes (type 0x02); it is
// after-first-unlock / NSE-readable. The distinct type makes it impossible to
// seal or open session content under it via the typed envelope API (A15 / F10).
type WakeKey [32]byte

// ContentKey decrypts only session content (type 0x01); it is biometric-gated,
// not NSE-readable, and not derivable from the wake key (A15 / F10).
type ContentKey [32]byte

// EpochGrant is the sealed, signed delivery of an epoch's keys (F3). Sealed is a
// crypto_box_seal artifact addressed to a device's recipient key that carries
// the epoch/grant coordinates AND both keys inside its authenticated plaintext;
// EpochID/GrantSeq are outer routing copies that must match the sealed inner
// values on open. Sig is the machine's Ed25519 signature so a relay cannot forge
// a grant (SealAnonymous alone gives no sender authentication).
type EpochGrant struct {
	EpochID  uint32
	GrantSeq uint64
	Sealed   []byte
	Sig      []byte
}

func grantSigMessage(sealed []byte, epochID uint32, grantSeq uint64) []byte {
	msg := make([]byte, 0, len(grantSigDomain)+len(sealed)+12)
	msg = append(msg, grantSigDomain...)
	msg = append(msg, sealed...)
	msg = binary.BigEndian.AppendUint32(msg, epochID)
	msg = binary.BigEndian.AppendUint64(msg, grantSeq)
	return msg
}

// SealEpochGrant seals the epoch keys and coordinates to a device's recipient
// X25519 key (A14) and signs the result with the machine's Ed25519 signing key
// (F3). The coordinates travel authenticated both inside the sealed plaintext
// and under the signature; SealAnonymous alone would let anyone holding the
// recipient public key forge a grant, so the signature is what pins the sender.
func SealEpochGrant(machinePriv ed25519.PrivateKey, recipientPub []byte, epochID uint32, grantSeq uint64, keys EpochKeys) (*EpochGrant, error) {
	var inner [grantInnerLen]byte
	inner[0] = grantVersion
	copy(inner[1:9], keyID8(recipientPub))
	binary.BigEndian.PutUint32(inner[9:13], epochID)
	binary.BigEndian.PutUint64(inner[13:21], grantSeq)
	copy(inner[21:53], keys.WakeKey[:])
	copy(inner[53:85], keys.ContentKey[:])

	sealed, err := SealToRecipient(recipientPub, inner[:])
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(machinePriv, grantSigMessage(sealed, epochID, grantSeq))
	return &EpochGrant{EpochID: epochID, GrantSeq: grantSeq, Sealed: sealed, Sig: sig}, nil
}

// OpenEpochGrant verifies a grant against the pinned machine signing key, opens
// it via the device KeyStore, and checks the outer routing coordinates against
// the values authenticated inside the sealed plaintext (F3). It does NOT track
// replay — use a GrantReceiver for that.
func OpenEpochGrant(ks KeyStore, machinePub ed25519.PublicKey, g *EpochGrant) (uint32, uint64, EpochKeys, error) {
	if len(machinePub) != ed25519.PublicKeySize || len(g.Sig) != ed25519.SignatureSize {
		return 0, 0, EpochKeys{}, ErrGrantSig
	}
	if !ed25519.Verify(machinePub, grantSigMessage(g.Sealed, g.EpochID, g.GrantSeq), g.Sig) {
		return 0, 0, EpochKeys{}, ErrGrantSig
	}
	plain, err := ks.OpenSealedBox(g.Sealed)
	if err != nil {
		return 0, 0, EpochKeys{}, err
	}
	if len(plain) != grantInnerLen || plain[0] != grantVersion {
		return 0, 0, EpochKeys{}, ErrBadGrant
	}
	// Bind to this device's recipient key and to the outer coordinates, all in
	// constant time so a mismatch reveals nothing beyond rejection.
	wantRecip := keyID8(ks.RecipientPublic())
	innerEpoch := binary.BigEndian.Uint32(plain[9:13])
	innerSeq := binary.BigEndian.Uint64(plain[13:21])
	var epochBuf, seqBuf, outEpoch, outSeq [8]byte
	binary.BigEndian.PutUint32(epochBuf[:4], innerEpoch)
	binary.BigEndian.PutUint32(outEpoch[:4], g.EpochID)
	binary.BigEndian.PutUint64(seqBuf[:], innerSeq)
	binary.BigEndian.PutUint64(outSeq[:], g.GrantSeq)
	ok := subtle.ConstantTimeCompare(plain[1:9], wantRecip)
	ok &= subtle.ConstantTimeCompare(epochBuf[:], outEpoch[:])
	ok &= subtle.ConstantTimeCompare(seqBuf[:], outSeq[:])
	if ok != 1 {
		return 0, 0, EpochKeys{}, ErrGrantCoord
	}
	var keys EpochKeys
	copy(keys.WakeKey[:], plain[21:53])
	copy(keys.ContentKey[:], plain[53:85])
	return g.EpochID, g.GrantSeq, keys, nil
}

func keyID8(pub []byte) []byte {
	id := KeyID(pub)
	return id[:]
}

// GrantReceiver tracks the highest (epoch_id, grant_seq) a device has accepted,
// so a replayed or older grant is rejected (F3). It is safe for concurrent use.
type GrantReceiver struct {
	mu      sync.Mutex
	hiEpoch uint32
	hiSeq   uint64
	seen    bool
}

// NewGrantReceiver returns a receiver with no accepted grants yet.
func NewGrantReceiver() *GrantReceiver { return &GrantReceiver{} }

// Accept verifies and opens a grant (OpenEpochGrant) and then enforces strict
// (epoch_id, grant_seq) monotonicity, rejecting replays and stale grants.
func (r *GrantReceiver) Accept(ks KeyStore, machinePub ed25519.PublicKey, g *EpochGrant) (uint32, uint64, EpochKeys, error) {
	epochID, grantSeq, keys, err := OpenEpochGrant(ks, machinePub, g)
	if err != nil {
		return 0, 0, EpochKeys{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen && !(epochID > r.hiEpoch || (epochID == r.hiEpoch && grantSeq > r.hiSeq)) {
		return 0, 0, EpochKeys{}, ErrGrantReplay
	}
	r.hiEpoch, r.hiSeq, r.seen = epochID, grantSeq, true
	return epochID, grantSeq, keys, nil
}
