package protocol

// C3-committee item C (R1.2.2 letter): the daemon reassembles a chunked snapshot
// ONLY when the shim's hello reply advertised snapshot_chunking. A snapshot_info
// preamble from a shim that did NOT advertise the capability is a protocol
// error, not an accepted stream: accepting it would let the enforcement live
// entirely shim-side (latent downgrade coupling if a future daemon ever stops
// advertising unconditionally). Failing-first: against the pre-C3 code the
// reader accepted a preamble regardless of negotiation.

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

// driveReadSnapshotNegotiated mirrors the chunk_snapshot_test harness: writer
// goroutine on one net.Pipe end, the real readSnapshot on the other, with the
// negotiated flag under the caller's control.
func driveReadSnapshotNegotiated(t *testing.T, negotiated bool, write func(net.Conn)) ([]byte, error) {
	t.Helper()
	cl, sv := net.Pipe()
	t.Cleanup(func() { cl.Close(); sv.Close() })
	_ = sv.SetReadDeadline(time.Now().Add(2 * time.Second))
	go write(cl)
	return readSnapshot(sv, negotiated)
}

func TestReadSnapshot_PreambleWithoutNegotiationRejected(t *testing.T) {
	_, err := driveReadSnapshotNegotiated(t, false, func(c net.Conn) {
		body, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeSnapshotInfo, SnapshotLen: 4})
		_ = wire.WriteFrame(c, wire.TControl, body)
		_ = wire.WriteFrame(c, wire.TSnapshot, []byte("snap"))
	})
	if !errors.Is(err, errUnnegotiatedPreamble) {
		t.Fatalf("preamble without negotiated chunking: got err %v, want errUnnegotiatedPreamble", err)
	}
}

func TestReadSnapshot_SingleFrameStillAcceptedWithoutNegotiation(t *testing.T) {
	snap, err := driveReadSnapshotNegotiated(t, false, func(c net.Conn) {
		_ = wire.WriteFrame(c, wire.TSnapshot, []byte("legacy"))
	})
	if err != nil || string(snap) != "legacy" {
		t.Fatalf("legacy single-frame snapshot: got (%q, %v), want (legacy, nil)", snap, err)
	}
}

func TestReadSnapshot_PreambleWithNegotiationStillAccepted(t *testing.T) {
	snap, err := driveReadSnapshotNegotiated(t, true, func(c net.Conn) {
		body, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeSnapshotInfo, SnapshotLen: 4})
		_ = wire.WriteFrame(c, wire.TControl, body)
		_ = wire.WriteFrame(c, wire.TSnapshot, []byte("snap"))
	})
	if err != nil || string(snap) != "snap" {
		t.Fatalf("negotiated chunked snapshot: got (%q, %v), want (snap, nil)", snap, err)
	}
}

// TestShimStream_TrailingChunkAfterExactCompletionIgnored pins the v2.2 spec
// reconciliation end to end: a chunk stream whose declared length is satisfied
// EXACTLY on a frame boundary, followed by a stray trailing TSnapshot frame,
// delivers the snapshot correctly, silently ignores the stray frame, and
// continues into live TDataOut frames without error. Detecting the trailing
// frame as an error would require reading past completion — which would hang
// an idle session (R1.2.2) — so tolerance is the DESIGN, and this test is its
// positive pin (previously untested; C3 committee finding).
func TestShimStream_TrailingChunkAfterExactCompletionIgnored(t *testing.T) {
	cl, sv := net.Pipe()
	t.Cleanup(func() { cl.Close(); sv.Close() })

	want := []byte("exact-boundary-snapshot")
	go func() {
		// Fake shim peer: consume the attach request, then snapshot + stray + live.
		if _, _, err := wire.ReadFrame(cl); err != nil {
			return
		}
		body, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeSnapshotInfo, SnapshotLen: len(want)})
		_ = wire.WriteFrame(cl, wire.TControl, body)
		_ = wire.WriteFrame(cl, wire.TSnapshot, want) // exactly snapshot_len, one frame boundary
		_ = wire.WriteFrame(cl, wire.TSnapshot, []byte("stray-trailing-chunk"))
		_ = wire.WriteFrame(cl, wire.TDataOut, []byte("live"))
	}()

	st, err := newShimStream(sv, shimwire.Caps{SnapshotChunking: true})
	if err != nil {
		t.Fatalf("newShimStream: %v", err)
	}
	defer st.Close()
	if got := st.Snapshot(); string(got) != string(want) {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}
	select {
	case f := <-st.Frames():
		if string(f) != "live" {
			t.Fatalf("first live frame = %q, want %q (stray chunk leaked into the stream?)", f, "live")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live stream did not continue after the trailing chunk")
	}
}
