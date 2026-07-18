package protocol

// Item 1.2 (agents-tracker-mlm) — daemon-side reassembly of a CHUNKED shim->daemon
// snapshot. readSnapshot must accept a snapshot_info preamble declaring the total
// length up front, then reassemble exactly that many bytes from TSnapshot chunk
// frames — byte-identical to the shim's snapshot — while a bare TSnapshot frame
// (old shim / small snapshot) still takes today's single-frame path (R1.2.1/R1.2.2).
// Bounds + corruption (R1.2.4) are protocol errors, not hangs or OOMs.
//
// These drive the real reader (readSnapshot) over a net.Pipe; the writer half is a
// hand-rolled encoder mirroring the shim's on-wire format so the reader is tested in
// isolation. The shim's real WRITER is covered in internal/shim; the full real
// writer<->reader pair end to end is TestIntegration_ChunkedOversizedSnapshot.

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

const chunkMax = wire.MaxFrame - 1 // largest snapshot bytes carried in one TSnapshot frame

// writePreamble sends the snapshot_info control announcing a total length of n.
func writePreamble(w net.Conn, n int) {
	b, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeSnapshotInfo, SnapshotLen: n})
	_ = wire.WriteFrame(w, wire.TControl, b)
}

// writeChunks slices snap into <=chunkMax TSnapshot frames (empty snap => none).
func writeChunks(w net.Conn, snap []byte) {
	for off := 0; off < len(snap); off += chunkMax {
		end := off + chunkMax
		if end > len(snap) {
			end = len(snap)
		}
		_ = wire.WriteFrame(w, wire.TSnapshot, snap[off:end])
	}
}

// runReadSnapshot writes frames via write on one pipe end and reassembles them via
// the real readSnapshot on the other, under a short TOTAL deadline so a short/stalled
// stream fails fast instead of hanging (the R1.2.4 "total snapshot deadline" seam).
func runReadSnapshot(t *testing.T, write func(w net.Conn)) ([]byte, error) {
	t.Helper()
	cl, sv := net.Pipe()
	t.Cleanup(func() { _ = cl.Close(); _ = sv.Close() })
	go write(cl) // frames flow only as readSnapshot consumes them (net.Pipe is synchronous)
	_ = sv.SetReadDeadline(time.Now().Add(2 * time.Second))
	return readSnapshot(sv)
}

// deterministicSnap builds n bytes of non-trivial, position-dependent content so a
// misordered or truncated reassembly is caught byte-for-byte.
func deterministicSnap(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

// TestReadSnapshot_ChunkedByteIdentical is T1.2.a (reader half) + T1.2.e: a >1 MiB
// snapshot delivered as preamble + multiple chunks reassembles byte-identical, and a
// small snapshot yields the SAME bytes whether sent chunked (preamble + one chunk) or
// as a legacy single TSnapshot frame.
func TestReadSnapshot_ChunkedByteIdentical(t *testing.T) {
	big := deterministicSnap(chunkMax*2 + 12345) // >1 MiB: forces 3 chunks
	got, err := runReadSnapshot(t, func(w net.Conn) {
		writePreamble(w, len(big))
		writeChunks(w, big)
	})
	if err != nil {
		t.Fatalf("chunked reassembly of a %d-byte snapshot: %v", len(big), err)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("chunked reassembly not byte-identical: got %d bytes, want %d", len(got), len(big))
	}

	small := deterministicSnap(4096)
	chunked, err := runReadSnapshot(t, func(w net.Conn) {
		writePreamble(w, len(small))
		writeChunks(w, small)
	})
	if err != nil {
		t.Fatalf("chunked reassembly of small snapshot: %v", err)
	}
	legacy, err := runReadSnapshot(t, func(w net.Conn) {
		_ = wire.WriteFrame(w, wire.TSnapshot, small) // old shim / not negotiated: one bare frame
	})
	if err != nil {
		t.Fatalf("legacy single-frame read: %v", err)
	}
	if !bytes.Equal(chunked, small) || !bytes.Equal(legacy, small) || !bytes.Equal(chunked, legacy) {
		t.Fatalf("chunked (%d) vs legacy (%d) vs original (%d) not byte-identical", len(chunked), len(legacy), len(small))
	}
}

// TestReadSnapshot_BoundsAndCorruption is T1.2.c: exact frame boundaries reassemble
// cleanly; every malformed stream is a protocol error (never a hang, never an OOM).
func TestReadSnapshot_BoundsAndCorruption(t *testing.T) {
	okCases := []struct {
		name string
		n    int
	}{
		{"empty", 0},
		{"one byte", 1},
		{"exact MaxFrame-1 (single full chunk)", chunkMax},
		{"MaxFrame (spills to a second chunk)", chunkMax + 1},
		{"exact chunk multiple", chunkMax * 2},
		{"final one-byte chunk", chunkMax*2 + 1},
	}
	for _, c := range okCases {
		t.Run("ok/"+c.name, func(t *testing.T) {
			snap := deterministicSnap(c.n)
			got, err := runReadSnapshot(t, func(w net.Conn) {
				writePreamble(w, c.n)
				writeChunks(w, snap)
			})
			if err != nil {
				t.Fatalf("reassembly of %d bytes: %v", c.n, err)
			}
			if !bytes.Equal(got, snap) {
				t.Fatalf("reassembly mismatch at n=%d: got %d bytes", c.n, len(got))
			}
		})
	}

	errCases := []struct {
		name  string
		write func(w net.Conn)
	}{
		{"duplicate preamble", func(w net.Conn) {
			writePreamble(w, chunkMax*2)
			writePreamble(w, chunkMax*2) // a second preamble mid-transfer is illegal
		}},
		{"data before completion", func(w net.Conn) {
			writePreamble(w, chunkMax*2)
			_ = wire.WriteFrame(w, wire.TSnapshot, deterministicSnap(chunkMax))
			_ = wire.WriteFrame(w, wire.TDataOut, []byte("live")) // live frame before the snapshot completes
		}},
		{"overshoot (chunk exceeds declared length)", func(w net.Conn) {
			writePreamble(w, 100)                                          // declares 100 bytes...
			_ = wire.WriteFrame(w, wire.TSnapshot, deterministicSnap(150)) // ...but a chunk crosses that bound
		}},
		{"short stream", func(w net.Conn) {
			writePreamble(w, chunkMax*2)
			_ = wire.WriteFrame(w, wire.TSnapshot, deterministicSnap(chunkMax)) // only half arrives, then stall
		}},
		{"negative length", func(w net.Conn) {
			writePreamble(w, -1)
		}},
		{"over cap", func(w net.Conn) {
			writePreamble(w, maxSnapshotBytes+1) // rejected before any allocation
		}},
	}
	for _, c := range errCases {
		t.Run("err/"+c.name, func(t *testing.T) {
			_, err := runReadSnapshot(t, c.write)
			if err == nil {
				t.Fatalf("%s: want a protocol error, got nil", c.name)
			}
		})
	}
}

// TestReadSnapshot_LegacyDataBeforeSnapshot pins that the non-chunked path still
// treats a live frame before the snapshot as an error (R1.1.4 carried forward).
func TestReadSnapshot_LegacyDataBeforeSnapshot(t *testing.T) {
	_, err := runReadSnapshot(t, func(w net.Conn) {
		_ = wire.WriteFrame(w, wire.TDataOut, []byte("live-first"))
	})
	if err == nil {
		t.Fatal("a TDataOut before any snapshot must be a protocol error")
	}
	// A deadline-driven failure is acceptable too, but the specific error should not
	// be a nil success.
	if errors.Is(err, nil) {
		t.Fatal("unexpected nil error")
	}
}
