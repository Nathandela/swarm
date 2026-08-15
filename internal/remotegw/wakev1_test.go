package remotegw

// Bead agents-tracker-hggx.4 (Wave R3, machine side) -- FAILING-FIRST (TDD RED, GG-5)
// tests for `WakeV1` (docs/specifications/push-gateway-api.md §5, ADR-015 P8).
//
// WHAT THIS FILE PINS. The 74-byte wire shape, its canonical AAD tuple, and the
// producer-side seal. It does NOT pin the receiver (`internal/phonecore`, READ-ONLY,
// a later slice) and does NOT touch `internal/remote/crypto` (READ-ONLY per the task:
// the mailbox envelope and the legacy 78-byte type-0x02 wake are frozen there and stay
// frozen -- P8 delta 1 explicitly adds WakeV1 BESIDE them, never by editing them). The
// new shape therefore lives in THIS package, sealed directly against
// golang.org/x/crypto/chacha20poly1305 the same way crypto.seal does, under the
// already-exported crypto.WakeKey.
//
// THE SEAM these tests pin, so GREEN has one shape to build against:
//
//	type PushAddress [16]byte                    // PG-ALLOC-1: 16 opaque gateway-minted bytes
//	const WakeV1Size = 74                         // PG-WAKE-2
//	const WakeV1Type uint8 = 0x03                 // PG-WAKE-3: distinct from 0x01 (mailbox), 0x02 (legacy wake)
//	const WakeV1Expiry = 5 * time.Minute          // PG-WAKE-7: derived, not carried
//	func SealWakeV1(key crypto.WakeKey, addr PushAddress, seq uint64, issuedAt time.Time) ([]byte, error)
//
// SealWakeV1 seals EXACTLY ONCE per call, with a fresh random nonce (PG-WAKE-11). It is
// the obligation store's job (obligation_test.go), not this function's, to persist the
// result and replay the identical bytes on retry (PG-WAKE-12) -- calling SealWakeV1
// twice for one logical wake is a caller bug, and TestWakeV1_EachSealGetsAFreshNonce
// below is what makes that bug visible if committed by accident.
//
// This file contains NO implementation.

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"golang.org/x/crypto/chacha20poly1305"
)

// testPushAddress builds a deterministic, recognisable 16-byte address for tests. Not
// a golden fixture (PG-FIX-1 owns those, unrun per §15) -- just an address a test
// failure can echo back distinctly from another test's.
func testPushAddress(seed byte) PushAddress {
	var a PushAddress
	for i := range a {
		a[i] = seed + byte(i)
	}
	return a
}

// wakeV1AAD reconstructs the canonical AAD tuple (spec §5.3) BYTE FOR BYTE from
// scratch, independently of SealWakeV1's own internals, so these tests prove the
// producer used THIS tuple rather than merely being self-consistent with whatever it
// happened to compute.
//
//	"swarm-wake-v1" || version || push_address || wake_seq || issued_at || expires_at || nonce
func wakeV1AAD(version uint8, addr PushAddress, seq uint64, issuedAtMS, expiresAtMS int64, nonce []byte) []byte {
	b := make([]byte, 0, 78)
	b = append(b, []byte("swarm-wake-v1")...)
	b = append(b, version)
	b = append(b, addr[:]...)
	b = binary.BigEndian.AppendUint64(b, seq)
	b = binary.BigEndian.AppendUint64(b, uint64(issuedAtMS))
	b = binary.BigEndian.AppendUint64(b, uint64(expiresAtMS))
	b = append(b, nonce...)
	return b
}

// wakeV1ManualOpen is the independent receiver half these tests need without touching
// phonecore: PG-WAKE-3's pre-AEAD shape check (version, type) refuses before any AEAD
// is touched, then the AEAD is opened under the AAD wakeV1AAD reconstructs. A field
// mutated on the wire either fails the shape check (version, type) or fails the tag
// (everything else, because everything else is AAD-covered or is the AEAD's own
// nonce/tag parameter).
func wakeV1ManualOpen(t *testing.T, key crypto.WakeKey, env []byte) ([]byte, error) {
	t.Helper()
	if len(env) != WakeV1Size {
		t.Fatalf("envelope is %d bytes, want the pinned WakeV1Size %d", len(env), WakeV1Size)
	}
	if env[0] != crypto.VersionV1 || env[1] != WakeV1Type {
		return nil, errWakeV1ShapeRefused
	}
	var addr PushAddress
	copy(addr[:], env[2:18])
	seq := binary.BigEndian.Uint64(env[18:26])
	issuedAt := int64(binary.BigEndian.Uint64(env[26:34]))
	expiresAt := issuedAt + WakeV1Expiry.Milliseconds()
	nonce := env[34:58]
	tag := env[58:74]
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		t.Fatalf("chacha20poly1305.NewX: %v", err)
	}
	return aead.Open(nil, nonce, tag, wakeV1AAD(env[0], addr, seq, issuedAt, expiresAt, nonce))
}

// errWakeV1ShapeRefused marks wakeV1ManualOpen's own pre-AEAD refusal (PG-WAKE-3),
// distinct from an AEAD tag failure -- this file's own receiver-shaped double, not a
// production error type.
var errWakeV1ShapeRefused = errWakeV1ShapeRefusedT{}

type errWakeV1ShapeRefusedT struct{}

func (errWakeV1ShapeRefusedT) Error() string {
	return "wakev1: version/type refused before any AEAD was touched"
}

// TestWakeV1_TypeByteIsDistinctFromMailboxAndLegacyWake pins PG-WAKE-3's precondition:
// the three envelope shapes must be separable by type byte alone, before any key is
// even selected.
//
// NOT A RED TEST beyond the compile step (same shape as
// TestPBPUSH0_PushConfigCarriesNoContentKey, push_trigger_test.go:465-477): it is a
// FENCE on the constant's VALUE and passes as soon as WakeV1Type exists at 0x03.
func TestWakeV1_TypeByteIsDistinctFromMailboxAndLegacyWake(t *testing.T) {
	if WakeV1Type == crypto.TypeMailbox || WakeV1Type == crypto.TypePushWake {
		t.Fatalf("WakeV1Type = %#x collides with an existing type byte (mailbox=%#x, legacy wake=%#x)",
			WakeV1Type, crypto.TypeMailbox, crypto.TypePushWake)
	}
}

// TestWakeV1_ShapeAndFieldsMatchTheWireTable pins spec §5.1's layout byte for byte:
// version(1) type(1) push_address(16) wake_seq(8) issued_at(8) nonce(24) tag(16) = 74.
func TestWakeV1_ShapeAndFieldsMatchTheWireTable(t *testing.T) {
	key := testWakeKey()
	addr := testPushAddress(0x40)
	issuedAt := time.UnixMilli(1_755_000_000_123)
	const seq = uint64(7)

	env, err := SealWakeV1(key, addr, seq, issuedAt)
	if err != nil {
		t.Fatalf("SealWakeV1: %v", err)
	}
	if len(env) != WakeV1Size {
		t.Fatalf("len(env) = %d, want the pinned WakeV1Size %d (PG-WAKE-2)", len(env), WakeV1Size)
	}
	if env[0] != crypto.VersionV1 {
		t.Fatalf("version byte = %#x, want %#x", env[0], crypto.VersionV1)
	}
	if env[1] != WakeV1Type {
		t.Fatalf("type byte = %#x, want the WakeV1 constant %#x", env[1], WakeV1Type)
	}
	if !bytes.Equal(env[2:18], addr[:]) {
		t.Fatalf("push_address bytes = %x, want %x", env[2:18], addr[:])
	}
	if got := binary.BigEndian.Uint64(env[18:26]); got != seq {
		t.Fatalf("wake_seq = %d, want %d", got, seq)
	}
	if got := int64(binary.BigEndian.Uint64(env[26:34])); got != issuedAt.UnixMilli() {
		t.Fatalf("issued_at = %d, want %d ms", got, issuedAt.UnixMilli())
	}
	// expires_at is DERIVED, not carried (PG-WAKE-6): there is no 82nd/83rd byte for it.
	// The wire is exactly 74 bytes -- the len(env) assertion above is the whole test for
	// that half of the claim; this comment exists so a reader does not go looking for an
	// expires_at field on the wire and conclude one was forgotten.
}

// TestWakeV1_SealsAndOpensUnderTheCanonicalAAD is the round-trip proof that SealWakeV1
// used spec §5.3's exact seven-element tuple: an INDEPENDENTLY reconstructed AAD (never
// touching SealWakeV1's internals) must authenticate the sealed bytes.
func TestWakeV1_SealsAndOpensUnderTheCanonicalAAD(t *testing.T) {
	key := testWakeKey()
	addr := testPushAddress(0x50)
	issuedAt := time.UnixMilli(1_755_000_100_000)

	env, err := SealWakeV1(key, addr, 11, issuedAt)
	if err != nil {
		t.Fatalf("SealWakeV1: %v", err)
	}
	pt, err := wakeV1ManualOpen(t, key, env)
	if err != nil {
		t.Fatalf("independently reconstructed AAD failed to open the sealed envelope: %v -- "+
			"SealWakeV1 is not using the canonical AAD tuple of spec §5.3", err)
	}
	if len(pt) != 0 {
		t.Fatalf("opened plaintext is %d bytes, want 0 (PG-WAKE-1: empty plaintext)", len(pt))
	}

	// The WRONG wake key must not open it.
	var wrong crypto.WakeKey
	copy(wrong[:], key[:])
	wrong[0] ^= 0xFF
	if _, err := wakeV1ManualOpen(t, wrong, env); err == nil {
		t.Fatal("a wake opened under a DIFFERENT wake key -- the AEAD is not actually keyed by it")
	}
}

// TestWakeV1_MutatingAnyFieldFailsToOpen is PG-TEST-1 / the fixture's
// wakev1-mutations.json: every field of §5.1's table, flipped, must fail to open --
// version and type via PG-WAKE-3's pre-AEAD shape check, everything else via the AEAD
// tag, because everything else is AAD-covered or is the AEAD's own nonce/tag parameter.
func TestWakeV1_MutatingAnyFieldFailsToOpen(t *testing.T) {
	key := testWakeKey()
	addr := testPushAddress(0x11)
	issuedAt := time.UnixMilli(1_755_000_200_000)
	base, err := SealWakeV1(key, addr, 3, issuedAt)
	if err != nil {
		t.Fatalf("SealWakeV1: %v", err)
	}
	if _, err := wakeV1ManualOpen(t, key, base); err != nil {
		t.Fatalf("control: the UNMUTATED envelope must open cleanly: %v", err)
	}

	cases := []struct {
		name   string
		offset int
	}{
		{"version", 0},
		{"type", 1},
		{"push_address[0]", 2},
		{"push_address[15]", 17},
		{"wake_seq[0]", 18},
		{"wake_seq[7]", 25},
		{"issued_at[0]", 26},
		{"issued_at[7]", 33},
		{"nonce[0]", 34},
		{"nonce[23]", 57},
		{"tag[0]", 58},
		{"tag[15]", 73},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mutated := append([]byte(nil), base...)
			mutated[c.offset] ^= 0xFF
			if _, err := wakeV1ManualOpen(t, key, mutated); err == nil {
				t.Fatalf("mutating byte %d (%s) still opened -- that field is not authenticated", c.offset, c.name)
			}
		})
	}
}

// TestWakeV1_EachSealGetsAFreshNonce pins PG-WAKE-11 (uniqueness by construction, not
// by derivation from wake_seq) and is the reason PG-WAKE-12's "seal once, replay
// verbatim" rule has to live in the CALLER: two seals of identical logical fields are
// two DIFFERENT wire objects, so a retry that re-seals instead of replaying mints a
// second authenticator for one obligation.
func TestWakeV1_EachSealGetsAFreshNonce(t *testing.T) {
	key := testWakeKey()
	addr := testPushAddress(0x01)
	issuedAt := time.UnixMilli(1_755_000_300_000)

	e1, err := SealWakeV1(key, addr, 1, issuedAt)
	if err != nil {
		t.Fatalf("SealWakeV1 (1st): %v", err)
	}
	e2, err := SealWakeV1(key, addr, 1, issuedAt)
	if err != nil {
		t.Fatalf("SealWakeV1 (2nd): %v", err)
	}
	if bytes.Equal(e1[34:58], e2[34:58]) {
		t.Fatal("two seals of identical (address, seq, issued_at) produced the SAME nonce")
	}
	if bytes.Equal(e1, e2) {
		t.Fatal("two seals of identical fields produced byte-identical envelopes -- the nonce must be fresh per call")
	}
}

// TestWakeV1_AADIsNotTheMailboxHeaderAADRenamed is the differential PG-WAKE-9 exists to
// require: crypto.EnvelopeHeader.aad() (version, type, epoch, seq, sender_key_id,
// issued_at -- no domain string, no push_address, no expires_at, no nonce binding) must
// NOT authenticate a WakeV1 tag. aad() is unexported, so this reproduces its byte shape
// by hand from envelope.go's own field order rather than importing it.
func TestWakeV1_AADIsNotTheMailboxHeaderAADRenamed(t *testing.T) {
	key := testWakeKey()
	addr := testPushAddress(0x77)
	issuedAt := time.UnixMilli(1_755_000_400_000)

	env, err := SealWakeV1(key, addr, 5, issuedAt)
	if err != nil {
		t.Fatalf("SealWakeV1: %v", err)
	}
	nonce := env[34:58]
	tag := env[58:74]

	// crypto.EnvelopeHeader.aad(): version, type, epoch(4 BE), seq(8 BE),
	// sender_key_id(8), issued_at(8 BE) -- envelope.go:60-68, reproduced by hand because
	// the method is unexported and internal/remote/crypto is READ-ONLY for this task.
	var senderKeyID [8]byte
	legacyAAD := make([]byte, 0, 30)
	legacyAAD = append(legacyAAD, crypto.VersionV1, crypto.TypePushWake)
	legacyAAD = binary.BigEndian.AppendUint32(legacyAAD, 0) // epoch
	legacyAAD = binary.BigEndian.AppendUint64(legacyAAD, 5) // seq
	legacyAAD = append(legacyAAD, senderKeyID[:]...)
	legacyAAD = binary.BigEndian.AppendUint64(legacyAAD, uint64(issuedAt.UnixMilli()))

	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		t.Fatalf("chacha20poly1305.NewX: %v", err)
	}
	if _, err := aead.Open(nil, nonce, tag, legacyAAD); err == nil {
		t.Fatal("a WakeV1 tag opened under the LEGACY EnvelopeHeader.aad() shape -- PG-WAKE-9 requires " +
			"a structurally distinct AAD (domain string, push_address, expires_at, bound nonce), " +
			"not aad() with fields renamed")
	}
}
