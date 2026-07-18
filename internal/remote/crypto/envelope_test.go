// R-CRY.9/.11/.12 (+ D.0-A5) — async epoch-key envelope: byte-exact wire
// format, XChaCha20-Poly1305 AEAD with the header (minus recipient_key_id) as
// AAD, and authenticated per-epoch sequence numbers with replay/reorder/gap
// detection.
//
// Header (big-endian), 54 bytes before ciphertext:
//
//	version:u8=0x01 | type:u8 (0x01 mailbox / 0x02 push-wake) | epoch_id:u32 |
//	seq:u64 | recipient_key_id:8 | sender_key_id:8 | nonce:24 | ciphertext:N
//
// AAD = the header EXCLUDING recipient_key_id, so the ciphertext under a shared
// K_epoch is identical for every recipient (A5). key_id = SHA-256(pubkey)[:8].
//
// FROZEN CONTRACT (subset):
//
//	const ( VersionV1 uint8 = 0x01; TypeMailbox uint8 = 0x01; TypePushWake uint8 = 0x02 )
//	type EnvelopeHeader struct { Version, Type uint8; EpochID uint32; Seq uint64; RecipientKeyID, SenderKeyID [8]byte }
//	type Envelope struct { Header EnvelopeHeader; Nonce [24]byte; Ciphertext []byte }
//	func Seal(key [32]byte, h EnvelopeHeader, plaintext []byte) (*Envelope, error)
//	func SealDeterministic(key [32]byte, h EnvelopeHeader, nonce [24]byte, plaintext []byte) (*Envelope, error)
//	func (*Envelope) Open(key [32]byte) ([]byte, error)
//	func (*Envelope) Marshal() []byte
//	func ParseEnvelope(b []byte) (*Envelope, error)
//	func KeyID(pub []byte) [8]byte
//	type MailboxReceiver; func NewMailboxReceiver() *MailboxReceiver
//	type MailboxResult struct { Plaintext []byte; Gap bool }
//	func (*MailboxReceiver) Accept(key [32]byte, e *Envelope) (*MailboxResult, error)
//	var ErrUnknownVersion, ErrTruncated, ErrStaleSeq error
package crypto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func testHeader() EnvelopeHeader {
	return EnvelopeHeader{
		Version:        VersionV1,
		Type:           TypeMailbox,
		EpochID:        7,
		Seq:            42,
		RecipientKeyID: KeyID([]byte("recipient-pub")),
		SenderKeyID:    KeyID([]byte("sender-pub")),
	}
}

// TestEnvelope_RoundTripKAT pins the byte-exact header layout and a seal/open
// round-trip under a fixed key + nonce. The exact ciphertext bytes are
// derive-and-pin (XChaCha20-Poly1305 KAT cannot be computed by hand); the
// implementer records envelopeKATCiphertext at first green.
func TestEnvelope_RoundTripKAT(t *testing.T) {
	key := fill(0x9c)
	var nonce [24]byte
	for i := range nonce {
		nonce[i] = byte(i)
	}
	h := testHeader()
	plaintext := []byte("session-event-payload")

	env, err := SealDeterministic(key, h, nonce, plaintext)
	if err != nil {
		t.Fatalf("SealDeterministic: %v", err)
	}
	raw := env.Marshal()

	// Byte-exact header offsets.
	if raw[0] != 0x01 {
		t.Errorf("version byte = %#x, want 0x01", raw[0])
	}
	if raw[1] != TypeMailbox {
		t.Errorf("type byte = %#x, want %#x", raw[1], TypeMailbox)
	}
	if got := binary.BigEndian.Uint32(raw[2:6]); got != 7 {
		t.Errorf("epoch_id = %d, want 7", got)
	}
	if got := binary.BigEndian.Uint64(raw[6:14]); got != 42 {
		t.Errorf("seq = %d, want 42", got)
	}
	if !bytes.Equal(raw[14:22], h.RecipientKeyID[:]) {
		t.Error("recipient_key_id offset wrong")
	}
	if !bytes.Equal(raw[22:30], h.SenderKeyID[:]) {
		t.Error("sender_key_id offset wrong")
	}
	if !bytes.Equal(raw[30:54], nonce[:]) {
		t.Error("nonce offset wrong")
	}
	// XChaCha20-Poly1305 appends a 16-byte tag.
	if len(raw)-54 != len(plaintext)+16 {
		t.Errorf("ciphertext length = %d, want %d (plaintext+tag)", len(raw)-54, len(plaintext)+16)
	}

	parsed, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	got, err := parsed.Open(key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round-trip plaintext = %q, want %q", got, plaintext)
	}

	if len(envelopeKATCiphertext) == 0 {
		t.Log("TODO(impl): pin envelopeKATCiphertext (XChaCha20-Poly1305 KAT) at first green")
	} else if !bytes.Equal(env.Ciphertext, envelopeKATCiphertext) {
		t.Errorf("KAT ciphertext = %x, want %x", env.Ciphertext, envelopeKATCiphertext)
	}
}

var envelopeKATCiphertext []byte

// TestEnvelope_TruncatedRejected pins that a buffer shorter than a full header
// (or missing the AEAD tag) is rejected, not silently accepted.
func TestEnvelope_TruncatedRejected(t *testing.T) {
	key := fill(0x9c)
	env, err := Seal(key, testHeader(), []byte("x"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw := env.Marshal()
	for _, n := range []int{0, 1, 30, 53, len(raw) - 1} {
		if _, err := ParseEnvelope(raw[:n]); err == nil {
			t.Errorf("ParseEnvelope accepted a %d-byte truncated buffer", n)
		}
	}
}

// TestEnvelope_UnknownVersionRejected pins that an unknown version byte is
// rejected with ErrUnknownVersion.
func TestEnvelope_UnknownVersionRejected(t *testing.T) {
	key := fill(0x9c)
	env, err := Seal(key, testHeader(), []byte("x"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw := env.Marshal()
	raw[0] = 0x02 // future/unknown version
	if _, err := ParseEnvelope(raw); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("ParseEnvelope(version=2) err = %v, want ErrUnknownVersion", err)
	}
}

// TestEnvelope_TamperRejected pins R-CRY.11: any tamper to an AAD-covered
// header field or to the ciphertext fails the AEAD tag on Open, while flipping
// the routing-only recipient_key_id does NOT (it is outside the AAD).
func TestEnvelope_TamperRejected(t *testing.T) {
	key := fill(0x9c)
	env, err := Seal(key, testHeader(), []byte("payload"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Tamper AAD-covered fields (seq @6, epoch_id @2, type @1) and the
	// ciphertext: all must fail Open.
	for _, off := range []int{1, 2, 6, 54} {
		raw := env.Marshal()
		raw[off] ^= 0xff
		bad, err := ParseEnvelope(raw)
		if err != nil {
			continue // a parse rejection is also acceptable
		}
		if _, err := bad.Open(key); err == nil {
			t.Errorf("tamper at offset %d accepted by Open", off)
		}
	}

	// recipient_key_id (@14..22) is OUTSIDE the AAD: rewriting it must still
	// Open cleanly (A5 fan-out property).
	raw := env.Marshal()
	raw[14] ^= 0xff
	rerouted, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope(rerouted): %v", err)
	}
	if _, err := rerouted.Open(key); err != nil {
		t.Errorf("changing recipient_key_id broke Open; it must be outside the AAD: %v", err)
	}
}

// TestEnvelope_NonceUniqueAndXChaCha pins R-CRY.11: nonces are 24-byte (XChaCha,
// mandatory because K_epoch is reused across events) and fresh per envelope.
func TestEnvelope_NonceUniqueAndXChaCha(t *testing.T) {
	key := fill(0x9c)
	seen := map[[24]byte]bool{}
	for i := 0; i < 256; i++ {
		env, err := Seal(key, testHeader(), []byte("e"))
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if len(env.Nonce) != 24 {
			t.Fatalf("nonce length = %d, want 24 (XChaCha)", len(env.Nonce))
		}
		if seen[env.Nonce] {
			t.Fatalf("duplicate nonce after %d seals", i)
		}
		seen[env.Nonce] = true
	}
}

// TestEnvelope_IdenticalCiphertextAcrossRecipients pins A5: recipient_key_id is
// outside the AAD, so encrypting one event under a shared K_epoch and fanning
// it out to different recipients yields byte-identical ciphertext. A buggy impl
// that folds recipient_key_id into the AAD would produce a different tag.
func TestEnvelope_IdenticalCiphertextAcrossRecipients(t *testing.T) {
	key := fill(0x9c)
	var nonce [24]byte
	for i := range nonce {
		nonce[i] = 0x5a
	}
	plaintext := []byte("fanned-out-event")

	hA := testHeader()
	hA.RecipientKeyID = KeyID([]byte("device-A"))
	hB := testHeader()
	hB.RecipientKeyID = KeyID([]byte("device-B"))
	if hA.RecipientKeyID == hB.RecipientKeyID {
		t.Fatal("test setup: recipient ids must differ")
	}

	envA, err := SealDeterministic(key, hA, nonce, plaintext)
	if err != nil {
		t.Fatalf("SealDeterministic(A): %v", err)
	}
	envB, err := SealDeterministic(key, hB, nonce, plaintext)
	if err != nil {
		t.Fatalf("SealDeterministic(B): %v", err)
	}
	if !bytes.Equal(envA.Ciphertext, envB.Ciphertext) {
		t.Error("ciphertext differs across recipients; recipient_key_id must be outside the AAD")
	}
}

// TestMailbox_ReplaySeqRejected pins R-CRY.12: a receiver tracks the highest seq
// per (sender_key_id, epoch_id) and rejects a duplicate seq.
func TestMailbox_ReplaySeqRejected(t *testing.T) {
	key := fill(0x9c)
	r := NewMailboxReceiver()
	acceptSeq(t, r, key, 1)
	acceptSeq(t, r, key, 2)

	// Re-deliver seq 2.
	env := sealSeq(t, key, 2)
	if _, err := r.Accept(key, env); !errors.Is(err, ErrStaleSeq) {
		t.Fatalf("replay of seq 2 err = %v, want ErrStaleSeq", err)
	}
}

// TestMailbox_ReorderDetected pins R-CRY.12: a lower-than-highest seq arriving
// late (relay reorder) is rejected.
func TestMailbox_ReorderDetected(t *testing.T) {
	key := fill(0x9c)
	r := NewMailboxReceiver()
	acceptSeq(t, r, key, 1)
	acceptSeq(t, r, key, 2)
	acceptSeq(t, r, key, 3)

	env := sealSeq(t, key, 2) // arrives after 3
	if _, err := r.Accept(key, env); !errors.Is(err, ErrStaleSeq) {
		t.Fatalf("reordered seq 2 err = %v, want ErrStaleSeq", err)
	}
}

// TestMailbox_GapSurfaced pins R-CRY.12: a skipped seq is surfaced (Gap=true)
// so the client can resync-from-snapshot, rather than silently swallowed.
func TestMailbox_GapSurfaced(t *testing.T) {
	key := fill(0x9c)
	r := NewMailboxReceiver()
	acceptSeq(t, r, key, 1)

	env := sealSeq(t, key, 3) // seq 2 skipped
	res, err := r.Accept(key, env)
	if err != nil {
		t.Fatalf("Accept(seq 3 after 1): %v", err)
	}
	if !res.Gap {
		t.Error("a seq gap (missing seq 2) was not surfaced")
	}
}

// TestRelay_CannotForgeEvent pins R-CRY.12/R-REL.6: an untrusted relay lacking
// K_epoch cannot forge an event a receiver accepts — a plausible-looking
// envelope with attacker-chosen ciphertext fails the AEAD.
func TestRelay_CannotForgeEvent(t *testing.T) {
	key := fill(0x9c)
	r := NewMailboxReceiver()

	forged := sealSeq(t, key, 1)
	// The relay does not hold K_epoch; it can only fabricate ciphertext bytes.
	forged.Ciphertext = bytes.Repeat([]byte{0xde}, len(forged.Ciphertext))
	if _, err := r.Accept(key, forged); err == nil {
		t.Fatal("receiver accepted a relay-forged event")
	}
}

func sealSeq(t *testing.T, key [32]byte, seq uint64) *Envelope {
	t.Helper()
	h := testHeader()
	h.Seq = seq
	env, err := Seal(key, h, []byte("event"))
	if err != nil {
		t.Fatalf("Seal(seq=%d): %v", seq, err)
	}
	return env
}

func acceptSeq(t *testing.T, r *MailboxReceiver, key [32]byte, seq uint64) {
	t.Helper()
	if _, err := r.Accept(key, sealSeq(t, key, seq)); err != nil {
		t.Fatalf("Accept(seq=%d): %v", seq, err)
	}
}
