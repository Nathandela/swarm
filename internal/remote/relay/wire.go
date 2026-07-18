// Package relay is the untrusted rendezvous/mailbox/push relay (R-REL.*,
// ADR-007 D9/D11). It stores and forwards ONLY ciphertext, relay-auth public
// keys, and routing metadata — never plaintext, identity keys, or the pairing
// secret. E2EE confidentiality does not depend on TLS (the relay is a blind
// forwarder); TLS is a metadata-only defense.
//
// The relay wire envelope is a SEPARATE structure modeled on internal/wire
// framing (4-byte big-endian length + 1-byte tag, MaxFrame 1 MiB) with its own
// tag set — it is NOT an extension of the frozen client<->daemon wire.Type enum
// (plan D.0-A13). Control payloads are JSON, snake_case, unknown-field-tolerant,
// version/capability negotiated on r_hello.
package relay

import (
	"encoding/binary"
	"errors"
	"io"
)

// MaxFrame is the largest legal declared frame length L (payload+1 tag byte),
// the anti-DoS cap enforced before any body allocation or read.
const MaxFrame = 1 << 20 // 1 MiB

// ProtocolVersion is the current relay wire protocol version. r_hello negotiates
// it; a client offering an incompatible version is refused, not downgraded.
const ProtocolVersion = 1

// MsgType is a relay frame tag. It is a distinct tag space from wire.Type.
type MsgType byte

// Relay frame tags. Only a subset carries dedicated tags; the remaining control
// operations ride MsgRelay with a JSON "op" discriminator.
const (
	// MsgError is a server->client error reply: JSON {code, message}.
	MsgError MsgType = 0x00
	// MsgOK is a server->client success reply: op-specific JSON body.
	MsgOK MsgType = 0x01
	// MsgRelay is a generic control request: JSON {op, ...fields}.
	MsgRelay MsgType = 0x02
	// MsgMailboxAppend is the dedicated tag for the hot mailbox-append path.
	MsgMailboxAppend MsgType = 0x03
)

// ErrFrameTooLarge is returned when a payload (WriteFrame) or a declared length
// (ReadFrame) exceeds MaxFrame.
var ErrFrameTooLarge = errors.New("relay: frame exceeds MaxFrame")

// errZeroLength is returned when a declared length is 0; every frame carries at
// least its 1 tag byte.
var errZeroLength = errors.New("relay: declared frame length is 0, want at least 1 for the tag byte")

// WriteFrame encodes tag and payload as one frame and writes it to w. It rejects
// a payload larger than MaxFrame-1 without writing any bytes.
func WriteFrame(w io.Writer, tag MsgType, payload []byte) error {
	if len(payload) > MaxFrame-1 {
		return ErrFrameTooLarge
	}
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload))+1)
	frame[4] = byte(tag)
	copy(frame[5:], payload)
	_, err := w.Write(frame)
	return err
}

// ReadFrame reads one frame from r and returns its tag and payload.
//
// The declared length is validated against MaxFrame BEFORE any body allocation
// or read (the anti-DoS cap): an oversized length is refused on the 4-byte
// header alone. A reader exhausted exactly at a frame boundary yields io.EOF; a
// truncated header or body yields io.ErrUnexpectedEOF. The tag is not validated
// here — the dispatcher answers an unknown op with a clean r_error.
func ReadFrame(r io.Reader) (MsgType, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	l := binary.BigEndian.Uint32(hdr[:])
	if l == 0 {
		return 0, nil, errZeroLength
	}
	if l > MaxFrame {
		return 0, nil, ErrFrameTooLarge
	}
	body := make([]byte, l)
	if _, err := io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, nil, io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return MsgType(body[0]), body[1:], nil
}
