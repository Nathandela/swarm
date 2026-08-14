package pairing

// The short pairing code, ADR-007 B140 (agents-tracker-tr0n): ten Crockford base32 characters a
// human reads off one screen and types into another, from which BOTH of the values the QR
// carries are derived -- the 16-byte rendezvous id from the public three-character tag, the
// 32-byte XXpsk0 PSK from the secret seven. One ceremony, one secret, two spellings; everything
// downstream of the mint is byte-identical to the QR path.
//
// THE SPLIT DERIVATION IS THE SECURITY ARGUMENT, not a layout choice. The relay sees hex(id).
// An id derived from the whole code would be an offline oracle -- grind candidate codes through
// the KDF until the observed id matches, and the PSK falls without touching the network. With
// the id a function of the tag alone, a guessed secret can only be tested against a live
// handshake, which burns the single-use ceremony (R-PAIR.1): one guess per invocation, 2^-35
// each. B140 runs the full arithmetic.
//
// The phone runs this same construction (via the gomobile bridge); the golden vectors in
// shortcode_test.go are what keep the two sides honest.

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"crypto/sha256"

	"golang.org/x/crypto/hkdf"
)

// shortCodeAlphabet is Crockford base32: no I, L, O (folded from what humans type) and no U.
const shortCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	shortCodeTagLen    = 3 // public: names the rendezvous, 15 bits
	shortCodeSecretLen = 7 // secret: never leaves the two screens, 35 bits
	shortCodeLen       = shortCodeTagLen + shortCodeSecretLen
	shortCodeIDSalt    = "swarm-remote/1 short-code-id"
	shortCodePSKSalt   = "swarm-remote/1 short-code-psk"
)

// ErrShortCodeMalformed is the refusal for anything that is not ten Crockford characters after
// normalisation. It is deliberately one error: which character was wrong is the typist's to see
// on their own screen, and a more specific message adds nothing a retry does not.
var ErrShortCodeMalformed = errors.New("pairing: short code is not ten Crockford base32 characters")

// MintShortCode draws a fresh code from r and returns it in display form (XXX-XXXX-XXX)
// together with the rendezvous id and PSK it derives to. The caller treats the id and PSK
// exactly as it treats the QR's: the code is not a second protocol.
func MintShortCode(r io.Reader) (code string, id [16]byte, psk [32]byte, err error) {
	// 5 bits per character, 50 bits for 10: seven random bytes carry 56, consumed as a bit
	// stream so no character is biased.
	raw := make([]byte, 7)
	if _, err = io.ReadFull(r, raw); err != nil {
		return "", id, psk, fmt.Errorf("pairing: minting short code: %w", err)
	}
	var chars [shortCodeLen]byte
	var acc, bits uint
	next := 0
	for i := 0; i < shortCodeLen; i++ {
		for bits < 5 {
			acc = acc<<8 | uint(raw[next])
			next++
			bits += 8
		}
		bits -= 5
		chars[i] = shortCodeAlphabet[(acc>>bits)&31]
	}
	canonical := string(chars[:])
	id, psk = deriveShortCode(canonical)
	display := canonical[:3] + "-" + canonical[3:7] + "-" + canonical[7:]
	return display, id, psk, nil
}

// DeriveShortCode parses what a person typed -- case, hyphens, spaces and the I/L/O slips all
// forgiven -- and derives the rendezvous id and PSK.
func DeriveShortCode(typed string) (id [16]byte, psk [32]byte, err error) {
	canonical, err := normalizeShortCode(typed)
	if err != nil {
		return id, psk, err
	}
	id, psk = deriveShortCode(canonical)
	return id, psk, nil
}

func normalizeShortCode(typed string) (string, error) {
	var b strings.Builder
	for _, r := range strings.ToUpper(typed) {
		switch r {
		case '-', ' ':
			continue
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		}
		if !strings.ContainsRune(shortCodeAlphabet, r) {
			return "", ErrShortCodeMalformed
		}
		b.WriteRune(r)
	}
	if b.Len() != shortCodeLen {
		return "", ErrShortCodeMalformed
	}
	return b.String(), nil
}

func deriveShortCode(canonical string) (id [16]byte, psk [32]byte) {
	tag, secret := canonical[:shortCodeTagLen], canonical[shortCodeTagLen:]
	// The id first, from the tag ALONE; it then salts the PSK's info so the secret binds to
	// the ceremony it was minted for.
	mustHKDF(id[:], []byte(tag), shortCodeIDSalt, nil)
	mustHKDF(psk[:], []byte(secret), shortCodePSKSalt, id[:])
	return id, psk
}

func mustHKDF(out, ikm []byte, salt string, info []byte) {
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, []byte(salt), info), out); err != nil {
		// HKDF-SHA256 cannot fail for these lengths; a failure here is a corrupted runtime.
		panic(fmt.Sprintf("pairing: hkdf: %v", err))
	}
}
