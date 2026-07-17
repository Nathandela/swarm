package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// FuzzRoundTrip asserts the encode->decode identity holds for an arbitrary
// valid type tag and arbitrary payload: whatever WriteFrame accepts, ReadFrame
// reproduces byte-for-byte with the same type.
func FuzzRoundTrip(f *testing.F) {
	f.Add(uint8(TControl), []byte(""))
	f.Add(uint8(TDataOut), []byte("hello"))
	f.Add(uint8(TDataIn), []byte{0x00, 0xff, 0x1b})
	f.Add(uint8(TSnapshot), bytes.Repeat([]byte{0x41}, 1024))

	f.Fuzz(func(t *testing.T, typeByte uint8, payload []byte) {
		typ := allTypes[int(typeByte)%len(allTypes)]
		// Keep payloads within the legal ceiling; oversized rejection is
		// covered by the deterministic unit tests.
		if len(payload) > MaxFrame-1 {
			payload = payload[:MaxFrame-1]
		}
		var buf bytes.Buffer
		if err := WriteFrame(&buf, typ, payload); err != nil {
			t.Fatalf("WriteFrame(%#x, %d bytes): %v", typ, len(payload), err)
		}
		gotType, gotPayload, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if gotType != typ {
			t.Fatalf("type = %#x, want %#x", gotType, typ)
		}
		if !bytes.Equal(gotPayload, payload) {
			t.Fatalf("payload round-trip mismatch: got %d bytes, want %d", len(gotPayload), len(payload))
		}
		if buf.Len() != 0 {
			t.Fatalf("%d trailing bytes after one frame", buf.Len())
		}
	})
}

// FuzzReadFrame feeds arbitrary bytes to the decoder. The contract is purely
// robustness: ReadFrame must never panic and must always terminate a read loop
// (every call eventually returns an error on a finite reader), and it must not
// over-allocate on a hostile declared length — the reader here would need to
// supply MaxFrame+ body bytes for any single frame, which it never can, so an
// oversized declared length must be rejected rather than driving a huge
// allocation.
func FuzzReadFrame(f *testing.F) {
	// Seeds: a valid frame, a truncated one, an oversized declared length, and
	// pure noise.
	var valid bytes.Buffer
	_ = WriteFrame(&valid, TControl, []byte("seed"))
	f.Add(valid.Bytes())
	f.Add([]byte{0x00, 0x00, 0x00, 0x01}) // header only, promises 1 body byte
	oversize := make([]byte, 4)
	binary.BigEndian.PutUint32(oversize, 0xFFFFFFFF) // 4 GiB declared length
	f.Add(oversize)
	f.Add([]byte("not a frame at all"))

	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)
		// A finite reader must drain to an error in a bounded number of steps;
		// the cap guarantees each frame consumes at least its 5-byte minimum,
		// so len(data) frames is a hard upper bound.
		for i := 0; i <= len(data)+1; i++ {
			_, _, err := ReadFrame(r)
			if err != nil {
				if errors.Is(err, ErrFrameTooLarge) || errors.Is(err, ErrUnknownType) ||
					errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					return
				}
				// Any other error is acceptable too, as long as it terminates.
				return
			}
		}
		t.Fatalf("ReadFrame did not terminate on a %d-byte finite reader", len(data))
	})
}
