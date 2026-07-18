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

// SAS derives the 4-emoji short authentication string from a Noise channel
// binding: okm = HKDF-SHA256(salt=sasSalt, ikm=channelBinding); the first 3
// bytes are read as four big-endian 6-bit indices into the 64-entry table. A
// clean handshake yields the same SAS on both ends; a tampered transcript
// (divergent bindings) yields different SAS, which operators reject out-of-band.
func SAS(channelBinding []byte) ([4]string, error) {
	if len(channelBinding) == 0 {
		return [4]string{}, ErrEmptyBinding
	}
	r := hkdf.New(sha256.New, channelBinding, []byte(sasSalt), nil)
	var okm [3]byte
	if _, err := io.ReadFull(r, okm[:]); err != nil {
		return [4]string{}, err
	}
	n := uint32(okm[0])<<16 | uint32(okm[1])<<8 | uint32(okm[2])
	return [4]string{
		sasWords[(n>>18)&0x3f],
		sasWords[(n>>12)&0x3f],
		sasWords[(n>>6)&0x3f],
		sasWords[n&0x3f],
	}, nil
}
