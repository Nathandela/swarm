// R-PAIR.2 — pairing QR payload codec (byte-exact, <=200 bytes).
//
// Encoded form: QRPrefix + base64url(no padding) of the byte layout:
//
//	version:u8=0x01 | flags:u8 | relay_url_len:u8 | relay_url:L |
//	rendezvous_id:16 | pairing_secret:32 | machine_static_pub:32?
//
// rendezvous_id and pairing_secret are INDEPENDENT random fields; the relay only
// ever sees rendezvous_id (the secret is the OOB camera channel, never on the
// wire). This file is a failing-first stub; the codec is the implementer's.
package pairing

import (
	"encoding/base64"
	"errors"
	"strings"
)

const (
	// QRPrefix is the textual scheme prefix of an encoded pairing QR.
	QRPrefix = "swarm-pair:1:"
	// QRVersion is the payload version byte (first byte of the base64url body).
	QRVersion = 0x01
	// QRFlagMachineStaticPub marks (in the flags byte) that the optional 32-byte
	// machine_static_pub trailer is present.
	QRFlagMachineStaticPub = 0x01
	// QRMaxBytes is the hard size budget for the whole encoded string (R-PAIR.2).
	QRMaxBytes = 200
	// MaxRelayURLLen is the longest relay URL whose pairing QR can still be DRAWN on a
	// standard 80x24 terminal (PB-PAIR-1(b)). Every other field of the payload is
	// fixed-width, so the URL is the one free variable in the whole size budget, and the
	// binding limit is the symbol's version, not QRMaxBytes.
	//
	// Derivation. An L-character URL encodes to 13 (QRPrefix) + base64url(3 + L + 16 + 32)
	// characters. At L=39 that is 133 characters, the most a version-6 symbol carries at
	// ECC level L (134 bytes) -- 41 modules, which at half-block density is 45x23 cells
	// with the minimum quiet zone of 2 and 47x24 with 3. At L=40 the payload is 135
	// characters, one past version 6, so the symbol steps to version 7: 45 modules, 49x25
	// cells even at the minimum quiet zone, which no 24-row terminal can show. The cliff
	// is one character wide and the symbol is REFUSED on the far side of it, so the URL
	// has to be bounded where it is written. QRMaxBytes only bites at L>=90, far past this.
	//
	// internal/remote/qrterm's TestRender_FitsAStandard80x24TerminalByRelayURLLength is
	// the proof: it is the only place that sees both this codec and the terminal renderer,
	// and it re-derives this number from both rather than trusting the arithmetic above.
	MaxRelayURLLen = 39
)

// ErrQRMalformed is returned by DecodeQR for a bad prefix, version, length, or a
// flags/trailer mismatch.
var ErrQRMalformed = errors.New("pairing: malformed pairing QR payload")

// QRPayload is the decoded content of a pairing QR (R-PAIR.2). See the byte
// layout documented on this file's package comment. MachineStaticPub is nil or
// exactly 32 bytes, its presence mirrored by QRFlagMachineStaticPub in Flags.
type QRPayload struct {
	Flags            byte
	RelayURL         string
	RendezvousID     [16]byte
	PairingSecret    [32]byte
	MachineStaticPub []byte
}

// EncodeQR renders p as QRPrefix + base64url(no padding) of the documented byte
// layout. It errors if the resulting string would exceed QRMaxBytes or if the
// relay URL is longer than a u8 length field allows.
func EncodeQR(p QRPayload) (string, error) {
	if len(p.RelayURL) > 255 {
		return "", ErrQRMalformed
	}
	if n := len(p.MachineStaticPub); n != 0 && n != 32 {
		return "", ErrQRMalformed
	}
	// The static-pub flag bit mirrors the trailer's presence exactly, so encode
	// and decode always agree; other flag bits are preserved verbatim.
	flags := p.Flags
	if len(p.MachineStaticPub) == 32 {
		flags |= QRFlagMachineStaticPub
	} else {
		flags &^= QRFlagMachineStaticPub
	}

	raw := make([]byte, 0, 3+len(p.RelayURL)+16+32+32)
	raw = append(raw, QRVersion)
	raw = append(raw, flags)
	raw = append(raw, byte(len(p.RelayURL)))
	raw = append(raw, []byte(p.RelayURL)...)
	raw = append(raw, p.RendezvousID[:]...)
	raw = append(raw, p.PairingSecret[:]...)
	if len(p.MachineStaticPub) == 32 {
		raw = append(raw, p.MachineStaticPub...)
	}

	s := QRPrefix + base64.RawURLEncoding.EncodeToString(raw)
	if len(s) > QRMaxBytes {
		return "", ErrQRMalformed
	}
	return s, nil
}

// DecodeQR parses a QRPrefix-scheme pairing string back into a QRPayload,
// rejecting an unknown version, a bad length, or a flags/trailer mismatch with
// ErrQRMalformed.
func DecodeQR(s string) (QRPayload, error) {
	body, ok := strings.CutPrefix(s, QRPrefix)
	if !ok {
		return QRPayload{}, ErrQRMalformed
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return QRPayload{}, ErrQRMalformed
	}
	// Fixed header: version | flags | relay_url_len.
	if len(raw) < 3 {
		return QRPayload{}, ErrQRMalformed
	}
	if raw[0] != QRVersion {
		return QRPayload{}, ErrQRMalformed
	}
	var p QRPayload
	p.Flags = raw[1]
	urlLen := int(raw[2])
	off := 3
	// relay_url:L | rendezvous_id:16 | pairing_secret:32.
	if len(raw) < off+urlLen+16+32 {
		return QRPayload{}, ErrQRMalformed
	}
	p.RelayURL = string(raw[off : off+urlLen])
	off += urlLen
	copy(p.RendezvousID[:], raw[off:off+16])
	off += 16
	copy(p.PairingSecret[:], raw[off:off+32])
	off += 32
	// Optional machine_static_pub:32, present iff the flag bit is set. The
	// remaining length MUST match the flag exactly (no splicing, no trailer).
	rest := len(raw) - off
	if p.Flags&QRFlagMachineStaticPub != 0 {
		if rest != 32 {
			return QRPayload{}, ErrQRMalformed
		}
		p.MachineStaticPub = append([]byte(nil), raw[off:off+32]...)
	} else if rest != 0 {
		return QRPayload{}, ErrQRMalformed
	}
	return p, nil
}
