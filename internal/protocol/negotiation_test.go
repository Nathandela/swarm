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
