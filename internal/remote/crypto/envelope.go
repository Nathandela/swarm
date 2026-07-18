package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

// Wire constants (R-CRY.9).
const (
	VersionV1    uint8 = 0x01
	TypeMailbox  uint8 = 0x01 // session content, under the content key
	TypePushWake uint8 = 0x02 // content-free wake, under the wake key
)

// headerLen is the fixed byte length of a marshalled header (fields + nonce)
// before the ciphertext: version(1) type(1) epoch(4) seq(8) recipient(8)
// sender(8) nonce(24).
const headerLen = 54

var (
	// ErrUnknownVersion rejects a future/unknown wire version.
	ErrUnknownVersion = errors.New("crypto: unknown envelope version")
	// ErrTruncated rejects a buffer too short to hold a full header + tag.
	ErrTruncated = errors.New("crypto: truncated envelope")
	// ErrStaleSeq rejects a replayed or reordered mailbox sequence number.
	ErrStaleSeq = errors.New("crypto: stale or reordered sequence number")
)

// EnvelopeHeader is the cleartext routing/authentication header. Every field
// except recipient_key_id is bound as AAD; recipient_key_id is routing-only so
// one ciphertext fans out byte-identically to every recipient (A5).
type EnvelopeHeader struct {
	Version        uint8
	Type           uint8
	EpochID        uint32
	Seq            uint64
	RecipientKeyID [8]byte
	SenderKeyID    [8]byte
}

// aad is the associated data: the header EXCLUDING recipient_key_id and the
// nonce (the nonce is the AEAD's own nonce parameter).
func (h EnvelopeHeader) aad() []byte {
	b := make([]byte, 0, 22)
	b = append(b, h.Version, h.Type)
	b = binary.BigEndian.AppendUint32(b, h.EpochID)
	b = binary.BigEndian.AppendUint64(b, h.Seq)
	b = append(b, h.SenderKeyID[:]...)
	return b
}

// Envelope is a sealed async message: header, 24-byte XChaCha nonce, ciphertext.
type Envelope struct {
	Header     EnvelopeHeader
	Nonce      [24]byte
	Ciphertext []byte
}

// KeyID is the routing key id: the first 8 bytes of SHA-256 of a public key.
func KeyID(pub []byte) [8]byte {
	sum := sha256.Sum256(pub)
	var id [8]byte
	copy(id[:], sum[:8])
	return id
}

// Seal encrypts plaintext under key with a fresh random 24-byte nonce
// (XChaCha20-Poly1305 — mandatory because K_epoch is reused across events).
func Seal(key [32]byte, h EnvelopeHeader, plaintext []byte) (*Envelope, error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return SealDeterministic(key, h, nonce, plaintext)
}

// SealDeterministic encrypts under an explicit nonce (KATs and fan-out where a
// single ciphertext is reused across recipients).
func SealDeterministic(key [32]byte, h EnvelopeHeader, nonce [24]byte, plaintext []byte) (*Envelope, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce[:], plaintext, h.aad())
	return &Envelope{Header: h, Nonce: nonce, Ciphertext: ct}, nil
}

// Open authenticates and decrypts under key. Any tamper to an AAD-covered
// header field or to the ciphertext fails the tag; rewriting recipient_key_id
// (outside the AAD) does not.
func (e *Envelope) Open(key [32]byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, e.Nonce[:], e.Ciphertext, e.Header.aad())
}

// Marshal serialises the envelope to its byte-exact wire form.
func (e *Envelope) Marshal() []byte {
	b := make([]byte, 0, headerLen+len(e.Ciphertext))
	b = append(b, e.Header.Version, e.Header.Type)
	b = binary.BigEndian.AppendUint32(b, e.Header.EpochID)
	b = binary.BigEndian.AppendUint64(b, e.Header.Seq)
	b = append(b, e.Header.RecipientKeyID[:]...)
	b = append(b, e.Header.SenderKeyID[:]...)
	b = append(b, e.Nonce[:]...)
	b = append(b, e.Ciphertext...)
	return b
}

// ParseEnvelope parses the wire form. It rejects a short header, an unknown
// version, and a ciphertext too short to carry content plus the AEAD tag.
func ParseEnvelope(b []byte) (*Envelope, error) {
	if len(b) < headerLen {
		return nil, ErrTruncated
	}
	if b[0] != VersionV1 {
		return nil, ErrUnknownVersion
	}
	if len(b) <= headerLen+chacha20poly1305.Overhead {
		return nil, ErrTruncated
	}
	var e Envelope
	e.Header.Version = b[0]
	e.Header.Type = b[1]
	e.Header.EpochID = binary.BigEndian.Uint32(b[2:6])
	e.Header.Seq = binary.BigEndian.Uint64(b[6:14])
	copy(e.Header.RecipientKeyID[:], b[14:22])
	copy(e.Header.SenderKeyID[:], b[22:30])
	copy(e.Nonce[:], b[30:54])
	e.Ciphertext = append([]byte(nil), b[54:]...)
	return &e, nil
}

// MailboxResult is the outcome of accepting a mailbox envelope. Gap is set when
// a sequence number was skipped, so the client can resync-from-snapshot rather
// than silently lose events.
type MailboxResult struct {
	Plaintext []byte
	Gap       bool
}

type mailboxKey struct {
	sender [8]byte
	epoch  uint32
}

// MailboxReceiver enforces per-epoch sequence integrity end to end: it tracks
// the highest seq per (sender_key_id, epoch_id), rejects replay/reorder, and
// surfaces gaps (R-CRY.12).
type MailboxReceiver struct {
	mu      sync.Mutex
	highest map[mailboxKey]uint64
}

// NewMailboxReceiver returns an empty receiver.
func NewMailboxReceiver() *MailboxReceiver {
	return &MailboxReceiver{highest: make(map[mailboxKey]uint64)}
}

// Accept authenticates an envelope and advances the sequence tracker. A seq at
// or below the highest seen is ErrStaleSeq (replay/reorder); a valid seq beyond
// highest+1 sets Gap. An envelope that fails the AEAD (e.g. a relay forgery)
// returns the AEAD error and does not advance the tracker.
func (r *MailboxReceiver) Accept(key [32]byte, e *Envelope) (*MailboxResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	mk := mailboxKey{sender: e.Header.SenderKeyID, epoch: e.Header.EpochID}
	hi, seen := r.highest[mk]
	if seen && e.Header.Seq <= hi {
		return nil, ErrStaleSeq
	}
	pt, err := e.Open(key)
	if err != nil {
		return nil, err
	}
	gap := seen && e.Header.Seq > hi+1
	r.highest[mk] = e.Header.Seq
	return &MailboxResult{Plaintext: pt, Gap: gap}, nil
}
