package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
	"time"

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
// sender(8) issued_at(8) nonce(24).
const headerLen = 62

var (
	// ErrUnknownVersion rejects a future/unknown wire version.
	ErrUnknownVersion = errors.New("crypto: unknown envelope version")
	// ErrUnknownType rejects a type outside {mailbox, push-wake} at parse.
	ErrUnknownType = errors.New("crypto: unknown envelope type")
	// ErrTruncated rejects a buffer too short to hold a full header + tag.
	ErrTruncated = errors.New("crypto: truncated envelope")
	// ErrStaleSeq rejects a replayed or reordered mailbox sequence number.
	ErrStaleSeq = errors.New("crypto: stale or reordered sequence number")
	// ErrStaleAge rejects a mailbox event whose authenticated issued_at is
	// older than the receiver's max age (A5 bounded-age).
	ErrStaleAge = errors.New("crypto: mailbox event exceeds max age")
	// ErrWrongKeyType rejects opening an envelope whose type does not match the
	// typed key used (wake key vs content key — A15).
	ErrWrongKeyType = errors.New("crypto: envelope type does not match key type")
)

// EnvelopeHeader is the cleartext routing/authentication header. Every field
// except recipient_key_id is bound as AAD; recipient_key_id is routing-only so
// one ciphertext fans out byte-identically to every recipient (A5). issued_at is
// AAD-covered (F9), so it is authenticated and age-checkable, and is NOT part of
// the recipient_key_id exclusion.
type EnvelopeHeader struct {
	Version        uint8
	Type           uint8
	EpochID        uint32
	Seq            uint64
	RecipientKeyID [8]byte
	SenderKeyID    [8]byte
	IssuedAt       int64 // unix milliseconds; authenticated (AAD-covered)
}

// aad is the associated data: the header EXCLUDING recipient_key_id and the
// nonce (the nonce is the AEAD's own nonce parameter). issued_at is included.
func (h EnvelopeHeader) aad() []byte {
	b := make([]byte, 0, 30)
	b = append(b, h.Version, h.Type)
	b = binary.BigEndian.AppendUint32(b, h.EpochID)
	b = binary.BigEndian.AppendUint64(b, h.Seq)
	b = append(b, h.SenderKeyID[:]...)
	b = binary.BigEndian.AppendUint64(b, uint64(h.IssuedAt))
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
func seal(key [32]byte, h EnvelopeHeader, plaintext []byte) (*Envelope, error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return sealDeterministic(key, h, nonce, plaintext)
}

// sealDeterministic encrypts under an explicit nonce. It is package-private
// (F6): the deterministic nonce is a nonce-reuse footgun, so no exported API
// accepts a caller nonce; it stays available for KATs and the single-seal
// fan-out where one ciphertext is reused across recipients.
func sealDeterministic(key [32]byte, h EnvelopeHeader, nonce [24]byte, plaintext []byte) (*Envelope, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce[:], plaintext, h.aad())
	return &Envelope{Header: h, Nonce: nonce, Ciphertext: ct}, nil
}

// SealMailbox seals session content (type 0x01) under the content key. The
// distinct ContentKey type makes it impossible to seal content under a wake key
// via this API (A15 / F10).
func SealMailbox(k ContentKey, h EnvelopeHeader, plaintext []byte) (*Envelope, error) {
	h.Type = TypeMailbox
	return seal([32]byte(k), h, plaintext)
}

// OpenMailbox opens type-0x01 session content under the content key; any other
// type is refused (a wake payload cannot be opened as content).
func OpenMailbox(k ContentKey, e *Envelope) ([]byte, error) {
	if e.Header.Type != TypeMailbox {
		return nil, ErrWrongKeyType
	}
	return e.open([32]byte(k))
}

// SealWake seals a content-free wake (type 0x02) under the wake key (A15 / F10).
func SealWake(k WakeKey, h EnvelopeHeader, plaintext []byte) (*Envelope, error) {
	h.Type = TypePushWake
	return seal([32]byte(k), h, plaintext)
}

// OpenWake opens a type-0x02 wake under the wake key; any other type is refused
// (mailbox content cannot be opened with the NSE-readable wake key).
func OpenWake(k WakeKey, e *Envelope) ([]byte, error) {
	if e.Header.Type != TypePushWake {
		return nil, ErrWrongKeyType
	}
	return e.open([32]byte(k))
}

// Open authenticates and decrypts under key. Any tamper to an AAD-covered
// header field or to the ciphertext fails the tag; rewriting recipient_key_id
// (outside the AAD) does not.
func (e *Envelope) open(key [32]byte) ([]byte, error) {
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
	b = binary.BigEndian.AppendUint64(b, uint64(e.Header.IssuedAt))
	b = append(b, e.Nonce[:]...)
	b = append(b, e.Ciphertext...)
	return b
}

// ParseEnvelope parses the wire form. It rejects a short header, an unknown
// version, an unknown type, and a ciphertext too short to carry the AEAD tag. An
// empty-plaintext envelope (16-byte tag, no content) is valid (F12).
func ParseEnvelope(b []byte) (*Envelope, error) {
	if len(b) < headerLen {
		return nil, ErrTruncated
	}
	if b[0] != VersionV1 {
		return nil, ErrUnknownVersion
	}
	if b[1] != TypeMailbox && b[1] != TypePushWake {
		return nil, ErrUnknownType
	}
	if len(b) < headerLen+chacha20poly1305.Overhead {
		return nil, ErrTruncated
	}
	var e Envelope
	e.Header.Version = b[0]
	e.Header.Type = b[1]
	e.Header.EpochID = binary.BigEndian.Uint32(b[2:6])
	e.Header.Seq = binary.BigEndian.Uint64(b[6:14])
	copy(e.Header.RecipientKeyID[:], b[14:22])
	copy(e.Header.SenderKeyID[:], b[22:30])
	e.Header.IssuedAt = int64(binary.BigEndian.Uint64(b[30:38]))
	copy(e.Nonce[:], b[38:62])
	e.Ciphertext = append([]byte(nil), b[62:]...)
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
// surfaces gaps (R-CRY.12). When maxAge > 0 it also rejects events older than
// maxAge by their authenticated issued_at (A5). now is a test-only clock seam.
type MailboxReceiver struct {
	mu      sync.Mutex
	highest map[mailboxKey]uint64
	maxAge  time.Duration
	now     func() time.Time
}

// NewMailboxReceiver returns an empty receiver.
func NewMailboxReceiver() *MailboxReceiver {
	return &MailboxReceiver{highest: make(map[mailboxKey]uint64)}
}

// SeedHighWater seeds the high-water mark for a (sender, epoch) stream to a
// resume-snapshot cursor N, so the FIRST envelope after resume whose seq is not
// N+1 surfaces a gap. Without seeding, gap detection is blind on the first event
// of a fresh receiver (F4).
func (r *MailboxReceiver) SeedHighWater(sender [8]byte, epoch uint32, seq uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.highest[mailboxKey{sender: sender, epoch: epoch}] = seq
}

func (r *MailboxReceiver) clockNow() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// Accept authenticates an envelope and advances the sequence tracker. A seq at
// or below the highest seen is ErrStaleSeq (replay/reorder); a valid seq beyond
// highest+1 sets Gap. An envelope that fails the AEAD (e.g. a relay forgery)
// returns the AEAD error and does not advance the tracker. When maxAge > 0 an
// authenticated-but-too-old issued_at is ErrStaleAge.
func (r *MailboxReceiver) Accept(key [32]byte, e *Envelope) (*MailboxResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	mk := mailboxKey{sender: e.Header.SenderKeyID, epoch: e.Header.EpochID}
	hi, seen := r.highest[mk]
	if seen && e.Header.Seq <= hi {
		return nil, ErrStaleSeq
	}
	pt, err := e.open(key)
	if err != nil {
		return nil, err
	}
	if r.maxAge > 0 && r.clockNow().Sub(time.UnixMilli(e.Header.IssuedAt)) > r.maxAge {
		return nil, ErrStaleAge
	}
	gap := seen && e.Header.Seq > hi+1
	r.highest[mk] = e.Header.Seq
	return &MailboxResult{Plaintext: pt, Gap: gap}, nil
}
