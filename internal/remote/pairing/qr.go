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

import "errors"

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
func EncodeQR(p QRPayload) (string, error) { return "", ErrUnimplemented }

// DecodeQR parses a QRPrefix-scheme pairing string back into a QRPayload,
// rejecting an unknown version, a bad length, or a flags/trailer mismatch with
// ErrQRMalformed.
func DecodeQR(s string) (QRPayload, error) { return QRPayload{}, ErrUnimplemented }
