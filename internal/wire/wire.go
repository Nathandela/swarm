// Package wire is the shared G1 frame envelope (build-plan.md gap resolution
// G1): one length-prefixed, type-tagged frame carries every message on both
// the client<->daemon and daemon<->shim sockets.
//
// Wire format: a 4-byte big-endian length L, then 1 type byte, then L-1
// payload bytes. L == len(payload)+1, so an empty payload has L == 1 and a
// maximal payload (MaxFrame-1 bytes) has L == MaxFrame.
package wire

import (
	"encoding/binary"
	"errors"
	"io"
)

// MaxFrame is the largest legal declared frame length L (payload+1 type
// byte), the anti-DoS cap enforced before any body allocation or read.
//
// internal/skeleton's connection demux (handleConn) routes a fresh connection
// to the protocol server the instant its first byte is 0x00, relying on every
// legal frame's 4-byte big-endian length prefix having a zero most-significant
// byte — see the guard immediately below, which fails to compile if that ever
// stops holding.
const MaxFrame = 1 << 20 // 1 MiB

// Compile-time proof that MaxFrame stays small enough for the 0x00-first-byte
// demux described above: every legal length up to MaxFrame needs a zero top
// byte, which holds iff MaxFrame <= 1<<24-1 (0x00FFFFFF). An untyped negative
// constant cannot convert to uint, so if MaxFrame ever grows past that bound
// this line fails to compile instead of silently breaking the demux.
const _ uint = 1<<24 - 1 - MaxFrame

// Type identifies the payload a frame carries.
type Type byte

// The frozen set of frame types.
const (
	TControl  Type = 0x01
	TDataOut  Type = 0x02
	TDataIn   Type = 0x03
	TSnapshot Type = 0x04
)

// ErrFrameTooLarge is returned when a payload (WriteFrame) or a declared
// length (ReadFrame) exceeds MaxFrame.
var ErrFrameTooLarge = errors.New("wire: frame exceeds MaxFrame")

// ErrUnknownType is returned when a frame's type tag is outside the frozen
// set above.
var ErrUnknownType = errors.New("wire: unknown frame type")

// errZeroLength is returned when a declared length is 0; a frame must carry
// at least its 1 type byte.
var errZeroLength = errors.New("wire: declared frame length is 0, want at least 1 for the type byte")

func validType(t Type) bool {
	switch t {
	case TControl, TDataOut, TDataIn, TSnapshot:
		return true
	default:
		return false
	}
}

// WriteFrame encodes t and payload as one frame and writes it to w. It
// rejects payloads larger than MaxFrame-1 without writing anything.
func WriteFrame(w io.Writer, t Type, payload []byte) error {
	if len(payload) > MaxFrame-1 {
		return ErrFrameTooLarge
	}
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload))+1)
	frame[4] = byte(t)
	copy(frame[5:], payload)
	_, err := w.Write(frame)
	return err
}

// ReadFrame reads one frame from r and returns its type and payload.
//
// The declared length is checked against MaxFrame before any body
// allocation or read (the anti-DoS cap). A reader exhausted exactly at a
// frame boundary yields io.EOF; a truncated header or body yields
// io.ErrUnexpectedEOF.
func ReadFrame(r io.Reader) (Type, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		// io.ReadFull already yields io.EOF at a clean boundary (0 bytes read)
		// and io.ErrUnexpectedEOF for a partial header.
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
			// A complete header already committed to l body bytes, so even a
			// zero-byte body read is a truncation, not a clean boundary.
			return 0, nil, io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	t := Type(body[0])
	if !validType(t) {
		return 0, nil, ErrUnknownType
	}
	return t, body[1:], nil
}
