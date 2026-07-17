package protocol

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/wire"
)

// E6.1 — frame-level protocol codec. The control ops ride as JSON inside
// wire.TControl frames; the data plane rides as opaque binary in TDataIn/
// TDataOut/TSnapshot. Every control op must round-trip, malformed control
// payloads must be rejected (not panic), an unknown op must draw an error
// RESPONSE (never crash the server), and the two planes must demux by frame
// type. These assert the reused G1 envelope (internal/wire) carries the client
// socket exactly as it carries the shim socket (G1).

// TestCodec_EveryControlOpRoundTrips encodes one representative Control per op
// and asserts EncodeControl->DecodeControl is lossless.
func TestCodec_EveryControlOpRoundTrips(t *testing.T) {
	cases := []Control{
		{Op: OpHello, EndpointID: "ep1", ProtocolVersion: Version, Capabilities: []string{"attach", "subscribe"}},
		{Op: OpList, EndpointID: "ep1"},
		{Op: OpLaunch, EndpointID: "ep1", Launch: &LaunchReq{Agent: "claude", Cwd: "/tmp", Options: map[string]string{"model": "opus"}, Env: []string{"PATH=/bin"}, Cols: 80, Rows: 24, InitialPrompt: "hi"}},
		{Op: OpKill, EndpointID: "ep1", SessionID: "ep1/sess1"},
		{Op: OpDelete, EndpointID: "ep1", SessionID: "ep1/sess1"},
		{Op: OpAttach, EndpointID: "ep1", SessionID: "ep1/sess1"},
		{Op: OpDetach, EndpointID: "ep1", SessionID: "ep1/sess1"},
		{Op: OpResize, EndpointID: "ep1", SessionID: "ep1/sess1", Generation: 7, Cols: 120, Rows: 40},
		{Op: OpSubscribe, EndpointID: "ep1"},
		{Op: OpLease, EndpointID: "ep1", SessionID: "ep1/sess1", Generation: 3},
		{Op: OpOK, EndpointID: "ep1", SessionID: "ep1/sess1"},
		{Op: OpError, EndpointID: "ep1", Error: "bad session id"},
		{Op: OpEvent, EndpointID: "ep1", Session: &SessionView{EndpointID: "ep1", ID: "ep1/sess1", Agent: "claude", Group: "working"}},
	}
	for _, in := range cases {
		body, err := EncodeControl(in)
		if err != nil {
			t.Fatalf("EncodeControl(%s): %v", in.Op, err)
		}
		got, err := DecodeControl(body)
		if err != nil {
			t.Fatalf("DecodeControl(%s): %v", in.Op, err)
		}
		if !jsonEqual(in, got) {
			t.Errorf("round-trip mismatch for op %q:\n in = %+v\nout = %+v", in.Op, in, got)
		}
	}
}

// TestCodec_ControlIsSnakeCaseJSON pins the on-the-wire key casing (the schema
// is versioned/low-reversibility; snake_case is the frozen convention).
func TestCodec_ControlIsSnakeCaseJSON(t *testing.T) {
	body, err := EncodeControl(Control{Op: OpResize, EndpointID: "ep1", SessionID: "ep1/s", Generation: 2, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("EncodeControl: %v", err)
	}
	for _, key := range []string{`"op"`, `"endpoint_id"`, `"session_id"`, `"generation"`} {
		if !strings.Contains(string(body), key) {
			t.Errorf("encoded control %s missing snake_case key %s", body, key)
		}
	}
}

// TestCodec_DecodeRejectsMalformedJSON asserts a non-JSON control payload is a
// clean error, not a panic.
func TestCodec_DecodeRejectsMalformedJSON(t *testing.T) {
	if _, err := DecodeControl([]byte("{not json")); err == nil {
		t.Fatalf("DecodeControl on malformed JSON: err = nil, want error")
	}
}

// TestCodec_UnknownOpDrawsErrorResponseNotCrash sends a control frame with an
// unrecognized op and asserts the server replies with an OpError control and
// stays alive to serve the next request (no crash, no dropped connection).
func TestCodec_UnknownOpDrawsErrorResponseNotCrash(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	r := rawDial(t, sock)
	hello := r.hello(Version, nil)
	if hello.Op != OpHello {
		t.Fatalf("handshake reply op = %q, want hello", hello.Op)
	}

	r.writeControl(Control{Op: "definitely-not-an-op", EndpointID: hello.EndpointID})
	resp := r.readControl()
	if resp.Op != OpError {
		t.Fatalf("unknown-op reply op = %q, want %q", resp.Op, OpError)
	}
	if resp.Error == "" {
		t.Errorf("unknown-op error message is empty")
	}

	// The server must still serve a subsequent, valid request on the same conn.
	r.writeControl(Control{Op: OpList, EndpointID: hello.EndpointID})
	if got := r.readControl(); got.Op != OpList {
		t.Fatalf("post-error List reply op = %q, want %q (server did not survive)", got.Op, OpList)
	}
}

// TestCodec_OversizedControlRejectedBeforeAlloc asserts the shared G1 cap
// applies to the client socket: a declared control frame larger than
// wire.MaxFrame is refused by the envelope before any body allocation (the
// codec never hands the protocol layer a giant payload).
func TestCodec_OversizedControlRejectedBeforeAlloc(t *testing.T) {
	// WriteFrame enforces the cap symmetrically with ReadFrame; a payload one
	// byte past the max is rejected without writing.
	huge := make([]byte, wire.MaxFrame) // payload+type would exceed MaxFrame
	if err := wire.WriteFrame(discard{}, wire.TControl, huge); err == nil {
		t.Fatalf("WriteFrame of oversized control payload: err = nil, want ErrFrameTooLarge")
	}
}

// TestCodec_DataPlaneDemuxesByType asserts the server treats a TDataIn frame as
// opaque binary (never JSON-decoded) and does not crash when one arrives outside
// an attach — the two planes share one connection and demux purely by type tag.
func TestCodec_DataPlaneDemuxesByType(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	r := rawDial(t, sock)
	hello := r.hello(Version, nil)

	// A stray data-in frame (not valid JSON) before any attach must be ignored,
	// not decoded as control and not fatal.
	r.writeFrame(wire.TDataIn, []byte{0x00, 0xff, 0x1b, 0x5b}) // arbitrary binary
	// The control plane is still live afterward.
	r.writeControl(Control{Op: OpList, EndpointID: hello.EndpointID})
	if got := r.readControl(); got.Op != OpList {
		t.Fatalf("List after stray data frame op = %q, want %q", got.Op, OpList)
	}
}

// discard is an io.Writer that swallows writes, for the oversize-before-alloc
// assertion (the frame is rejected before any Write happens anyway).
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
