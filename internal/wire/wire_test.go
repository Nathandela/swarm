// Package wire is the shared G1 frame envelope (build-plan.md gap resolution
// G1): one length-prefixed, type-tagged frame carries every message on both the
// client<->daemon and daemon<->shim sockets. These are FAILING-FIRST tests
// (Epic 4 test-design pass): they exercise the frozen production API a separate
// implementer will build, so until that code exists this package does not
// compile and the only errors must be "undefined" for the new symbols.
//
// FROZEN CONTRACT (orchestrator brief, Epic 4):
//
//	const MaxFrame = 1 << 20 // 1 MiB
//	type Type byte
//	const (TControl Type = 0x01; TDataOut = 0x02; TDataIn = 0x03; TSnapshot = 0x04)
//	func WriteFrame(w io.Writer, t Type, payload []byte) error
//	func ReadFrame(r io.Reader) (Type, []byte, error)
//
// Wire format (design pin): a 4-byte big-endian length L, then 1 type byte,
// then L-1 payload bytes. L == len(payload)+1, so an empty payload has L == 1
// and a maximal payload (MaxFrame-1 bytes) has L == MaxFrame. WriteFrame rejects
// payloads larger than MaxFrame-1; ReadFrame rejects a declared L > MaxFrame
// BEFORE it allocates or reads the body (the anti-DoS cap, G1/S/E6.1).
//
// ERROR-CLASS PINS (design pins beyond the brief — the codec must distinguish
// error classes so callers can react, so these are asserted via errors.Is):
//   - oversized declared length / oversized write  -> ErrFrameTooLarge
//   - unknown type tag                             -> ErrUnknownType
//   - truncated body or partial 4-byte header      -> io.ErrUnexpectedEOF
//   - reader exhausted exactly at a frame boundary -> io.EOF (clean stop)
//
// These two sentinels (ErrFrameTooLarge, ErrUnknownType) are the only symbols
// this suite pins beyond the brief's signatures; they are the minimal surface a
// frame-reader loop needs to tell "stop cleanly" / "peer is hostile" apart.
package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// allTypes is the frozen set of valid frame type tags.
var allTypes = []Type{TControl, TDataOut, TDataIn, TSnapshot}

// --- round-trip: every type, including an empty payload --------------------

func TestRoundTrip_AllTypes(t *testing.T) {
	// E6.1/G1: encode then decode reproduces the exact type and payload for
	// every frame type, and for both an empty and a non-empty payload.
	payloads := map[string][]byte{
		"empty":  {},
		"short":  []byte("hello"),
		"binary": {0x00, 0x1b, 0x07, 0x7f, 0xff, 0x80, 0x9f}, // control/8-bit bytes survive verbatim
	}
	for _, typ := range allTypes {
		for name, pl := range payloads {
			t.Run(typeName(typ)+"/"+name, func(t *testing.T) {
				var buf bytes.Buffer
				if err := WriteFrame(&buf, typ, pl); err != nil {
					t.Fatalf("WriteFrame(%v): %v", typ, err)
				}
				gotType, gotPayload, err := ReadFrame(&buf)
				if err != nil {
					t.Fatalf("ReadFrame: %v", err)
				}
				if gotType != typ {
					t.Errorf("type = %#x, want %#x", gotType, typ)
				}
				if !bytes.Equal(gotPayload, pl) {
					t.Errorf("payload = %x, want %x", gotPayload, pl)
				}
				if buf.Len() != 0 {
					t.Errorf("%d bytes left after a single frame, want 0", buf.Len())
				}
			})
		}
	}
}

func TestRoundTrip_SequentialFrames(t *testing.T) {
	// Framing self-delimits: several frames written back to back read back in
	// order, each with its own type and payload boundary intact.
	var buf bytes.Buffer
	want := []struct {
		typ Type
		pl  []byte
	}{
		{TControl, []byte(`{"type":"hello"}`)},
		{TDataOut, []byte{0x1b, '[', '2', 'J'}},
		{TDataIn, []byte("q")},
		{TSnapshot, bytes.Repeat([]byte{0xab}, 4096)},
		{TDataOut, nil}, // empty in the middle must not desync the stream
		{TControl, []byte(`{"type":"exit_report"}`)},
	}
	for _, w := range want {
		if err := WriteFrame(&buf, w.typ, w.pl); err != nil {
			t.Fatalf("WriteFrame(%v): %v", w.typ, err)
		}
	}
	for i, w := range want {
		gotType, gotPayload, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("frame %d ReadFrame: %v", i, err)
		}
		if gotType != w.typ || !bytes.Equal(gotPayload, w.pl) {
			t.Errorf("frame %d = (%#x,%x), want (%#x,%x)", i, gotType, gotPayload, w.typ, w.pl)
		}
	}
	if _, _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("after draining all frames, err = %v, want io.EOF", err)
	}
}

// --- size limits -----------------------------------------------------------

func TestWriteFrame_RejectsOversizedPayload(t *testing.T) {
	// The largest legal payload is MaxFrame-1 (so the length field L=payload+1
	// tops out at exactly MaxFrame). One byte more must be refused, and must
	// not emit a partial frame.
	var buf bytes.Buffer
	if err := WriteFrame(&buf, TDataOut, make([]byte, MaxFrame-1)); err != nil {
		t.Fatalf("WriteFrame of the maximum payload (MaxFrame-1) failed: %v", err)
	}
	buf.Reset()
	err := WriteFrame(&buf, TDataOut, make([]byte, MaxFrame))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("WriteFrame(MaxFrame bytes) err = %v, want ErrFrameTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Errorf("WriteFrame emitted %d bytes on an oversized-payload rejection, want 0", buf.Len())
	}
}

func TestReadFrame_MaxSizedFrameRoundTrips(t *testing.T) {
	// The boundary case on the read side: a frame whose declared length is
	// exactly MaxFrame (payload MaxFrame-1) is accepted.
	pl := bytes.Repeat([]byte{0x5a}, MaxFrame-1)
	var buf bytes.Buffer
	if err := WriteFrame(&buf, TSnapshot, pl); err != nil {
		t.Fatalf("WriteFrame(max payload): %v", err)
	}
	gotType, gotPayload, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame(max frame): %v", err)
	}
	if gotType != TSnapshot || len(gotPayload) != MaxFrame-1 {
		t.Errorf("max frame decoded to type %#x len %d, want %#x len %d",
			gotType, len(gotPayload), TSnapshot, MaxFrame-1)
	}
}

func TestReadFrame_OversizedDeclaredLength(t *testing.T) {
	// A declared length above MaxFrame is rejected with ErrFrameTooLarge.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxFrame+1)
	_, _, err := ReadFrame(bytes.NewReader(hdr[:]))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("ReadFrame(len=MaxFrame+1) err = %v, want ErrFrameTooLarge", err)
	}
}

// countingReader serves a fixed prefix, then records (and refuses) any read
// that happens after the prefix is exhausted. It is the instrument for the
// "cap check happens BEFORE allocation/read of the body" contract: if ReadFrame
// tried to read or allocate the declared (enormous) body, it would read past
// the 4-byte header and trip bodyReads.
type countingReader struct {
	prefix    []byte
	off       int
	bodyReads int
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.off >= len(c.prefix) {
		c.bodyReads++
		return 0, io.EOF
	}
	n := copy(p, c.prefix[c.off:])
	c.off += n
	return n, nil
}

func TestReadFrame_CapCheckedBeforeAllocation(t *testing.T) {
	// Regression guard for the anti-DoS property (G1/E6.1): a hostile 4 GiB
	// declared length must be rejected on the header alone, without ReadFrame
	// ever attempting to read or allocate the body.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 0xFFFFFFFF)
	cr := &countingReader{prefix: hdr[:]}
	_, _, err := ReadFrame(cr)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("ReadFrame(len=4GiB) err = %v, want ErrFrameTooLarge", err)
	}
	if cr.bodyReads != 0 {
		t.Errorf("ReadFrame read the body %d time(s) after an oversized header; the cap must be enforced before any body read/alloc", cr.bodyReads)
	}
}

// --- malformed frames ------------------------------------------------------

func TestReadFrame_UnknownType(t *testing.T) {
	// A well-framed message carrying a type tag outside the frozen set is
	// rejected. Hand-craft the bytes (WriteFrame is not asked to emit an
	// invalid tag): length=1 (empty payload) + type 0x09.
	for _, bad := range []byte{0x00, 0x05, 0x09, 0xff} {
		var frame bytes.Buffer
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], 1) // L = 1: just the type byte, empty payload
		frame.Write(hdr[:])
		frame.WriteByte(bad)
		_, _, err := ReadFrame(&frame)
		if !errors.Is(err, ErrUnknownType) {
			t.Errorf("ReadFrame(type=%#x) err = %v, want ErrUnknownType", bad, err)
		}
	}
}

func TestReadFrame_ZeroDeclaredLength(t *testing.T) {
	// L == 0 is malformed: a frame must carry at least its 1 type byte. It must
	// error, not silently succeed or panic.
	var hdr [4]byte // all zero => L = 0
	if _, _, err := ReadFrame(bytes.NewReader(hdr[:])); err == nil {
		t.Errorf("ReadFrame(len=0) returned nil error, want a malformed-frame error")
	}
}

func TestReadFrame_PartialHeader(t *testing.T) {
	// Fewer than the 4 length bytes available (after at least one byte) is a
	// truncated header: io.ErrUnexpectedEOF.
	for _, n := range []int{1, 2, 3} {
		_, _, err := ReadFrame(bytes.NewReader(make([]byte, n)))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("ReadFrame(%d header bytes) err = %v, want io.ErrUnexpectedEOF", n, err)
		}
	}
}

func TestReadFrame_TruncatedBody(t *testing.T) {
	// A complete header promising N body bytes but with fewer available is a
	// truncated payload: io.ErrUnexpectedEOF.
	var buf bytes.Buffer
	if err := WriteFrame(&buf, TDataOut, []byte("0123456789")); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	full := buf.Bytes()
	// Drop the last 3 payload bytes so the header over-promises.
	_, _, err := ReadFrame(bytes.NewReader(full[:len(full)-3]))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadFrame(truncated body) err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestReadFrame_CleanEOFAtBoundary(t *testing.T) {
	// A reader exhausted exactly at a frame boundary (zero bytes available)
	// yields io.EOF, distinct from io.ErrUnexpectedEOF, so a read loop can stop
	// cleanly.
	_, _, err := ReadFrame(bytes.NewReader(nil))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame(empty) err = %v, want io.EOF", err)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("clean boundary EOF must not be io.ErrUnexpectedEOF")
	}
}

// typeName gives a readable subtest label for a frame type.
func typeName(t Type) string {
	switch t {
	case TControl:
		return "control"
	case TDataOut:
		return "dataout"
	case TDataIn:
		return "datain"
	case TSnapshot:
		return "snapshot"
	default:
		return "unknown"
	}
}
