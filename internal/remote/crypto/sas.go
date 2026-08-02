package crypto

import (
	"crypto/sha256"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
)

// ErrEmptyBinding is returned when no channel binding is supplied.
var ErrEmptyBinding = errors.New("crypto: empty channel binding")

// sasSalt is the HKDF salt for SAS derivation (R-PAIR.4).
const sasSalt = "swarm-remote/1 sas"

// sasWords is the canonical 64-entry SAS wordlist (ADR-007 D3 / R-PAIR.4),
// indices 0..63. Emoji are permitted here as security-UI DATA only — the one
// allowed exception to the no-emoji rule. A BYTE-IDENTICAL copy of this ordered
// table MUST be mirrored in the Swift client (R-DSN / R-PAIR): the SAS the two
// operators compare out-of-band is only meaningful if both ends index the same
// table.
var sasWords = [64]string{
	// 0-7: animals
	"🐶", "🐱", "🐭", "🐰", "🦊", "🐻", "🐼", "🐨",
	// 8-15
	"🐯", "🦁", "🐮", "🐷", "🐸", "🐵", "🐔", "🐧",
	// 16-23
	"🐦", "🦆", "🦉", "🐝", "🦋", "🐢", "🐙", "🐳",
	// 24-31: fruit
	"🍎", "🍊", "🍋", "🍌", "🍉", "🍇", "🍓", "🍒",
	// 32-39
	"🍑", "🍍", "🥝", "🍅", "🥑", "🌽", "🥕", "🍄",
	// 40-47: plants / sky
	"🌵", "🌲", "🌸", "🌻", "🍀", "🍁", "⭐", "🌙",
	// 48-55: symbols / objects
	"⚡", "🔥", "💧", "🌈", "⚽", "🏀", "🎈", "🎁",
	// 56-63
	"🔔", "🔑", "🔨", "🚗", "🚀", "⛵", "✈️", "🎸",
}

// SAS derives the 6-emoji short authentication string from a Noise channel
// binding: okm = HKDF-SHA256(salt=sasSalt, ikm=channelBinding); the first 5
// bytes are read as a 40-bit big-endian integer, and six big-endian 6-bit
// indices (the top 36 bits; the low 4 bits are unused) select into the 64-entry
// table. A clean handshake yields the same SAS on both ends; a tampered
// transcript (divergent bindings) yields different SAS, which operators reject
// out-of-band.
//
// ADR-007 amendment 2026-07-23 widened this from 24 bits (four emoji) to 36 bits
// (six emoji) to close the pairing grind attack (review finding MED-1): a
// leaked-QR attacker with a live man-in-the-middle position could grind ~2^24
// candidate keypairs (seconds on commodity hardware) to force its channel
// binding to a SAS equal to the honest leg's; six emoji raise that to ~2^36,
// infeasible inside a rate-limited pairing window. This is a LENGTH EXTENSION
// ONLY — the salt, the 64-emoji wordlist, and the HKDF construction are
// unchanged.
func SAS(channelBinding []byte) ([6]string, error) {
	if len(channelBinding) == 0 {
		return [6]string{}, ErrEmptyBinding
	}
	r := hkdf.New(sha256.New, channelBinding, []byte(sasSalt), nil)
	var okm [5]byte
	if _, err := io.ReadFull(r, okm[:]); err != nil {
		return [6]string{}, err
	}
	n := uint64(okm[0])<<32 | uint64(okm[1])<<24 | uint64(okm[2])<<16 | uint64(okm[3])<<8 | uint64(okm[4])
	return [6]string{
		sasWords[(n>>34)&0x3f],
		sasWords[(n>>28)&0x3f],
		sasWords[(n>>22)&0x3f],
		sasWords[(n>>16)&0x3f],
		sasWords[(n>>10)&0x3f],
		sasWords[(n>>4)&0x3f],
	}, nil
}
