package relay

// R-REL.1 — relay wire protocol. The relay envelope is a SEPARATE structure
// modeled on internal/wire framing (4-byte BE length + 1-byte tag, MaxFrame
// 1 MiB) with its own relay tag set — NOT an extension of the frozen
// client<->daemon wire.Type enum (plan D.0-A13). Control payloads are JSON,
// snake_case, unknown-field-tolerant, version/capability negotiated on r_hello.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// TestRelay_FrameCapEnforced asserts the anti-DoS cap: a declared length larger
// than MaxFrame is rejected BEFORE any body allocation or read, and a payload
// over the cap is refused on write.
func TestRelay_FrameCapEnforced(t *testing.T) {
	// A header declaring a body far larger than MaxFrame must be rejected on the
	// length check alone, without attempting to read (or allocate) the body.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxFrame+1)
	// No body follows: a reader that respected the cap never blocks on the body.
	_, _, err := ReadFrame(bytes.NewReader(hdr[:]))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("ReadFrame oversized length: got %v, want ErrFrameTooLarge", err)
	}

	// WriteFrame rejects an oversized payload without emitting bytes.
	var buf bytes.Buffer
	over := make([]byte, MaxFrame) // payload+tag would exceed MaxFrame
	if err := WriteFrame(&buf, MsgRelay, over); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("WriteFrame oversized payload: got %v, want ErrFrameTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("WriteFrame wrote %d bytes for a rejected frame, want 0", buf.Len())
	}
}

// TestRelay_FrameRoundTrip locks the tag+length framing in both directions so a
// relay tag is preserved distinctly from any wire.Type value.
func TestRelay_FrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{"k":"v"}`)
	if err := WriteFrame(&buf, MsgMailboxAppend, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	typ, got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if typ != MsgMailboxAppend {
		t.Fatalf("tag round-trip: got %v, want MsgMailboxAppend", typ)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload round-trip: got %q, want %q", got, payload)
	}
	if _, _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame at clean boundary: got %v, want io.EOF", err)
	}
}

// TestRelay_MalformedControlSurvives sends a control frame the relay cannot
// parse and asserts it answers r_error and stays alive for the next valid op.
func TestRelay_MalformedControlSurvives(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	conn := dialRaw(t, srv.URL())

	if err := conn.WriteMsg(MsgMailboxAppend, []byte("this is not json{{{")); err != nil {
		t.Fatalf("WriteMsg(malformed): %v", err)
	}
	typ, _, err := conn.ReadMsg()
	if err != nil {
		t.Fatalf("ReadMsg after malformed control: %v", err)
	}
	if typ != MsgError {
		t.Fatalf("malformed control reply: got %v, want MsgError", typ)
	}

	// The relay must not have died: a fresh valid hello still negotiates.
	if _, _, err := conn.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
		t.Fatalf("Hello after malformed control (relay should survive): %v", err)
	}
}

// TestRelay_VersionCapabilityNegotiation exercises r_hello: a compatible version
// negotiates a shared capability set; an unsupported version is refused, not
// silently downgraded.
func TestRelay_VersionCapabilityNegotiation(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	conn := dialRaw(t, srv.URL())
	ver, caps, err := conn.Hello(testCtx(t), ProtocolVersion, []string{"mailbox", "push", "made-up-cap"})
	if err != nil {
		t.Fatalf("Hello(compatible): %v", err)
	}
	if ver != ProtocolVersion {
		t.Fatalf("negotiated version: got %d, want %d", ver, ProtocolVersion)
	}
	// Negotiation is an intersection: an unknown client capability is dropped,
	// not echoed back as agreed.
	for _, c := range caps {
		if c == "made-up-cap" {
			t.Fatalf("negotiated caps leaked an unsupported capability: %v", caps)
		}
	}

	// An incompatible (future) major version is refused.
	conn2 := dialRaw(t, srv.URL())
	if _, _, err := conn2.Hello(testCtx(t), ProtocolVersion+1000, nil); err == nil {
		t.Fatalf("Hello(incompatible version) succeeded, want refusal")
	}
}
