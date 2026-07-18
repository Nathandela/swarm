package crypto

import "errors"

// ErrBadGrant is returned when an opened grant is not the expected size.
var ErrBadGrant = errors.New("crypto: malformed epoch grant")

// EpochKeys is the wake/content key split delivered per epoch (A15). The wake
// key is after-first-unlock / NSE-readable and opens only content-free push
// wakes; the content key is biometric-gated, not NSE-readable, and not
// derivable from the wake key.
type EpochKeys struct {
	WakeKey    [32]byte
	ContentKey [32]byte
}

// EpochGrant is the sealed delivery of an epoch's keys. Sealed is a crypto_box
// _seal artifact addressed to a device's recipient key; epoch/grant coordinates
// travel in the clear as routing metadata.
type EpochGrant struct {
	EpochID  uint32
	GrantSeq uint64
	Sealed   []byte
}

// SealEpochGrant seals the two epoch keys to a device's recipient X25519 key
// (A14). Because SealAnonymous is used, an offline-at-rotation device can open
// the opaque sealed bytes later, on reconnect (A5).
func SealEpochGrant(recipientPub []byte, epochID uint32, grantSeq uint64, keys EpochKeys) (*EpochGrant, error) {
	var plain [64]byte
	copy(plain[:32], keys.WakeKey[:])
	copy(plain[32:], keys.ContentKey[:])
	sealed, err := SealToRecipient(recipientPub, plain[:])
	if err != nil {
		return nil, err
	}
	return &EpochGrant{EpochID: epochID, GrantSeq: grantSeq, Sealed: sealed}, nil
}

// OpenEpochGrant opens a grant via the device KeyStore, recovering both epoch
// keys and the epoch/grant coordinates. Only the sealed-box recipient can open
// it.
func OpenEpochGrant(ks KeyStore, g *EpochGrant) (uint32, uint64, EpochKeys, error) {
	plain, err := ks.OpenSealedBox(g.Sealed)
	if err != nil {
		return 0, 0, EpochKeys{}, err
	}
	if len(plain) != 64 {
		return 0, 0, EpochKeys{}, ErrBadGrant
	}
	var keys EpochKeys
	copy(keys.WakeKey[:], plain[:32])
	copy(keys.ContentKey[:], plain[32:])
	return g.EpochID, g.GrantSeq, keys, nil
}
