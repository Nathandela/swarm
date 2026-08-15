package remotegw

// WakeV1 (ADR-015 P8, docs/specifications/push-gateway-api.md §5): the wake shape
// swarm-remote submits to the push gateway. It is added BESIDE the frozen mailbox
// envelope (internal/remote/crypto), never by editing it -- that package stays
// read-only from this wave.

import (
	"crypto/rand"
	"encoding/binary"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"golang.org/x/crypto/chacha20poly1305"
)

// PushAddress is the 16 opaque, gateway-minted bytes that route a WakeV1 (PG-ALLOC-1).
// It carries no structure: no installation, machine, pairing-order or allocation-time
// information is encoded in it.
type PushAddress [16]byte

// WakeV1Size is the pinned wire size of a WakeV1 envelope (PG-WAKE-2): version(1) +
// type(1) + push_address(16) + wake_seq(8) + issued_at(8) + nonce(24) + tag(16).
const WakeV1Size = 1 + 1 + 16 + 8 + 8 + 24 + 16 // 74

// WakeV1Type is the wake envelope's type byte (PG-WAKE-3, spec §5.1 offset 1): a value
// distinct from crypto.TypeMailbox (0x01) and the legacy crypto.TypePushWake (0x02), so
// the three shapes are separable before any AEAD is touched.
const WakeV1Type uint8 = 0x03

// WakeV1Expiry is the wake's DERIVED, non-carried expiry (PG-WAKE-6/7): issued_at plus
// five minutes, narrowed from the legacy 78-byte wake's ten. It matches the five-minute
// FCM TTL, so nothing outlives the delivery window it bounds.
const WakeV1Expiry = 5 * time.Minute

// wakeV1Domain is the domain-separation prefix of the canonical AAD (spec §5.3,
// PG-WAKE-8/9): "swarm-wake-v1" || version || push_address || wake_seq || issued_at ||
// expires_at || nonce. It is deliberately NOT crypto.EnvelopeHeader.aad() with fields
// renamed -- that shape excludes recipient_key_id and never binds the nonce, both of
// which this AAD does, so a WakeV1 tag never opens under the mailbox/legacy-wake AAD.
const wakeV1Domain = "swarm-wake-v1"

// SealWakeV1 seals one WakeV1 envelope: an EMPTY plaintext under key, with a fresh
// random nonce (PG-WAKE-11: uniqueness by construction, not derived from wake_seq),
// authenticated by the canonical AAD tuple above.
//
// It seals EXACTLY ONCE per call. The caller (the wake-obligation machine) durably
// persists the returned bytes and replays them verbatim on every retry (PG-WAKE-12) --
// calling SealWakeV1 twice for one logical wake mints a second nonce and a second
// authenticator for what must be one obligation, which is a caller bug this function
// does not protect against.
func SealWakeV1(key crypto.WakeKey, addr PushAddress, seq uint64, issuedAt time.Time) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	issuedMS := issuedAt.UnixMilli()
	expiresMS := issuedMS + WakeV1Expiry.Milliseconds()
	tag := aead.Seal(nil, nonce[:], nil, sealWakeV1AAD(crypto.VersionV1, addr, seq, issuedMS, expiresMS, nonce[:]))

	env := make([]byte, 0, WakeV1Size)
	env = append(env, crypto.VersionV1, WakeV1Type)
	env = append(env, addr[:]...)
	env = binary.BigEndian.AppendUint64(env, seq)
	env = binary.BigEndian.AppendUint64(env, uint64(issuedMS))
	env = append(env, nonce[:]...)
	env = append(env, tag...)
	return env, nil
}

// sealWakeV1AAD builds the canonical AAD tuple (spec §5.3) from its seven elements.
func sealWakeV1AAD(version uint8, addr PushAddress, seq uint64, issuedMS, expiresMS int64, nonce []byte) []byte {
	b := make([]byte, 0, 78)
	b = append(b, []byte(wakeV1Domain)...)
	b = append(b, version)
	b = append(b, addr[:]...)
	b = binary.BigEndian.AppendUint64(b, seq)
	b = binary.BigEndian.AppendUint64(b, uint64(issuedMS))
	b = binary.BigEndian.AppendUint64(b, uint64(expiresMS))
	b = append(b, nonce...)
	return b
}
