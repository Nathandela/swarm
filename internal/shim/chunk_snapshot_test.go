package shim

// Item 1.2 (agents-tracker-mlm) — the shim->daemon snapshot hop chunks an oversized
// snapshot instead of losing it to wire.WriteFrame's MaxFrame-1 limit. The shim
// chunks ONLY when the peer advertised snapshot chunking in its hello (R1.2.2); a
// non-advertising peer (an old daemon) gets today's single-frame path and an
// oversized grid still fails after that peer's own timeout (G-D: no worse than today).
//
// The writer is exercised two ways: directly against hub.attach over a pipe (unit),
// and end to end through a real shim process serving a styled 200x50 grid.

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/Nathandela/swarm/internal/wire"
)

// heavyStyled paints every cell of a rows x cols grid with a fully-styled run so the
// emulator snapshot exceeds wire.MaxFrame and must be chunked.
func heavyStyled(rows, cols int) []byte {
	var b bytes.Buffer
	for y := 0; y < rows; y++ {
		fg := (y * 5) % 256
		bg := (y*7 + 40) % 256
		fmt.Fprintf(&b, "\x1b[1;3;4;7;38;2;%d;%d;%d;48;2;%d;%d;%dm", fg, 128, 255-fg, bg, 64, 120)
		for x := 0; x < cols; x++ {
			b.WriteByte(byte('A' + (x+y)%26))
		}
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

// reassembleFromSink concatenates the chunk frames following a snapshot_info preamble
// and checks their total against the declared length.
func reassembleFromSink(t *testing.T, sink *frameSink) []byte {
	t.Helper()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	declared, sawPreamble := 0, false
	var data []byte
	for _, f := range sink.frames {
		switch f.typ {
		case wire.TControl:
			if c, err := shimwire.Decode(f.payload); err == nil && c.Type == shimwire.TypeSnapshotInfo {
				declared, sawPreamble = c.SnapshotLen, true
			}
		case wire.TSnapshot:
			data = append(data, f.payload...)
		}
	}
	if !sawPreamble {
		t.Fatalf("no snapshot_info preamble among %d frames (chunking did not engage)", len(sink.frames))
	}
	if len(data) != declared {
		t.Fatalf("reassembled %d chunk bytes, preamble declared %d", len(data), declared)
	}
	return data
}

// TestHubAttach_ChunksOversizedSnapshot is T1.2.a (writer half): hub.attach with a
// chunking peer delivers a >1 MiB snapshot as a preamble + multiple chunks that
// reassemble byte-identical to the emulator's snapshot.
func TestHubAttach_ChunksOversizedSnapshot(t *testing.T) {
	emu := vt.NewEmulator(200, 50)
	defer emu.Close()
	emu.Feed(heavyStyled(50, 200))
	want, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("emu.Snapshot: %v", err)
	}
	if len(want) <= wire.MaxFrame-1 {
		t.Fatalf("test setup: styled snapshot is %d bytes, not > MaxFrame-1 (%d)", len(want), wire.MaxFrame-1)
	}

	h := &hub{emu: emu, tr: newHubTranscript(t), metrics: &Metrics{}}
	conn, sink := newDrainedConn(t)
	cw := &connWriter{conn: conn, chunkSnapshot: true}
	sub := h.attach(cw)
	t.Cleanup(func() { h.detach(sub); <-sub.done })

	waitFrameCount(t, sink, wire.TSnapshot, 2) // an oversized snapshot spans >1 chunk
	got := reassembleFromSink(t, sink)
	if !bytes.Equal(got, want) {
		t.Fatalf("reassembled snapshot (%d bytes) not byte-identical to the emulator snapshot (%d bytes)", len(got), len(want))
	}
}

// TestHubAttach_SingleFrameWhenNotNegotiated pins the degradation path: a peer that
// did not advertise chunking gets exactly one bare TSnapshot frame, no preamble.
func TestHubAttach_SingleFrameWhenNotNegotiated(t *testing.T) {
	emu := vt.NewEmulator(80, 24)
	defer emu.Close()
	h := &hub{emu: emu, tr: newHubTranscript(t), metrics: &Metrics{}}
	conn, sink := newDrainedConn(t)
	cw := &connWriter{conn: conn} // chunkSnapshot defaults false
	sub := h.attach(cw)
	t.Cleanup(func() { h.detach(sub); <-sub.done })

	waitFrameCount(t, sink, wire.TSnapshot, 1)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, f := range sink.frames {
		if f.typ == wire.TControl {
			if c, err := shimwire.Decode(f.payload); err == nil && c.Type == shimwire.TypeSnapshotInfo {
				t.Fatal("non-negotiated attach sent a snapshot_info preamble; want a single-frame snapshot")
			}
		}
	}
	if n := sink.count(wire.TSnapshot); n != 1 {
		t.Fatalf("non-negotiated attach sent %d TSnapshot frames, want exactly 1", n)
	}
}

// styledFakeAgentConfig runs the fake agent painting a cols x rows styled grid.
func styledFakeAgentConfig(t *testing.T, cols, rows int) Config {
	t.Helper()
	var b bytes.Buffer
	for y := 0; y < rows; y++ {
		fg := (y * 5) % 256
		bg := (y*7 + 40) % 256
		b.WriteString("print ")
		fmt.Fprintf(&b, "\x1b[1;3;4;7;38;2;%d;%d;%d;48;2;%d;%d;%dm", fg, 128, 255-fg, bg, 64, 120)
		for x := 0; x < cols; x++ {
			b.WriteByte(byte('A' + (x+y)%26))
		}
		b.WriteByte('\n')
	}
	b.WriteString("idle 60s\n")
	cfg := fakeAgentConfig(t, b.String())
	cfg.Cols, cfg.Rows = cols, rows
	return cfg
}

// helloChunking performs the hello handshake advertising snapshot chunking (or not)
// and returns the shim's reply.
func (c *shimClient) helloChunking(chunking bool) shimwire.Control {
	c.t.Helper()
	c.writeControl(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version, SnapshotChunking: chunking})
	return c.waitControl(shimwire.TypeHello, 3*time.Second)
}

// snapshotInfo returns the snapshot_info preamble control the client has seen, if any.
func (c *shimClient) snapshotInfo() (shimwire.Control, bool) {
	for _, f := range c.frames() {
		if f.typ != wire.TControl {
			continue
		}
		if ctrl, err := shimwire.Decode(f.payload); err == nil && ctrl.Type == shimwire.TypeSnapshotInfo {
			return ctrl, true
		}
	}
	return shimwire.Control{}, false
}

// reassembleChunked concatenates the client's chunk frames and returns them once the
// declared length has arrived (mirrors the daemon reader), or fails after timeout.
func (c *shimClient) reassembleChunked(timeout time.Duration) []byte {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, ok := c.snapshotInfo(); ok {
			var data []byte
			for _, f := range c.frames() {
				if f.typ == wire.TSnapshot {
					data = append(data, f.payload...)
				}
			}
			if len(data) >= info.SnapshotLen {
				return data[:info.SnapshotLen]
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.t.Fatalf("chunked snapshot did not complete within %s", timeout)
	return nil
}

// TestShim_SnapshotChunkingNegotiation is the capability matrix at the shim boundary
// (T1.2.d): the hello reply advertises chunking; an advertising peer gets an oversized
// grid chunked and reassemblable; a non-advertising peer gets today's single-frame
// path and an oversized grid degrades to no snapshot delivered.
func TestShim_SnapshotChunkingNegotiation(t *testing.T) {
	t.Run("reply advertises chunking", func(t *testing.T) {
		cfg := helperConfig(t, modeIdle, nil, nil)
		ch := runShimAsync(cfg)
		t.Cleanup(func() { waitRun(t, ch, 10*time.Second) })
		c := dialShim(t, cfg.SocketPath)
		c.startReader()
		reply := c.helloChunking(true)
		if reply.WireVersion != shimwire.Version {
			t.Fatalf("hello reply wire version = %d, want %d", reply.WireVersion, shimwire.Version)
		}
		if !reply.SnapshotChunking {
			t.Fatal("hello reply did not advertise snapshot_chunking; the daemon cannot know the shim will chunk (R1.2.2)")
		}
		c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	})

	t.Run("oversized grid: advertised chunks, not-advertised degrades", func(t *testing.T) {
		cfg := styledFakeAgentConfig(t, 200, 50)
		ch := runShimAsync(cfg)
		t.Cleanup(func() { waitRun(t, ch, 15*time.Second) })

		// Advertising peer: poll fresh connections until the grid is fully painted,
		// then confirm the snapshot is chunked and reassembles to a valid 200x50 grid.
		var snap []byte
		deadline := time.Now().Add(10 * time.Second)
		for {
			c := dialShim(t, cfg.SocketPath)
			c.startReader()
			c.helloChunking(true)
			c.attach()
			if _, ok := c.snapshotInfo(); ok {
				snap = c.reassembleChunked(3 * time.Second)
			}
			_ = c.conn.Close()
			if len(snap) > wire.MaxFrame-1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("advertised attach never produced an oversized chunked snapshot (last %d bytes)", len(snap))
			}
			time.Sleep(100 * time.Millisecond)
		}
		if sn, err := vt.DecodeSnapshot(snap); err != nil {
			t.Fatalf("reassembled chunked snapshot does not decode: %v", err)
		} else if sn.Cols != 200 || sn.Rows != 50 {
			t.Errorf("reassembled grid %dx%d, want 200x50", sn.Cols, sn.Rows)
		}

		// Non-advertising peer against the same (now full) oversized grid: the shim's
		// single-frame write is rejected, so NO snapshot is delivered (degrades to
		// exactly today's behavior — the daemon reader would time out).
		c := dialShim(t, cfg.SocketPath)
		c.startReader()
		c.helloChunking(false)
		c.attach()
		time.Sleep(1500 * time.Millisecond)
		if info, ok := c.snapshotInfo(); ok {
			t.Fatalf("non-advertising peer received a chunking preamble (len=%d); it must use single-frame", info.SnapshotLen)
		}
		if n := c.snapshotFrameCount(); n != 0 {
			t.Fatalf("non-advertising peer received %d TSnapshot frames for an oversized grid; want 0 (single-frame write rejected, degrades as today)", n)
		}
	})

	t.Run("small grid: not-advertised single frame", func(t *testing.T) {
		cfg := helperConfig(t, modeIdle, nil, nil)
		ch := runShimAsync(cfg)
		t.Cleanup(func() { waitRun(t, ch, 10*time.Second) })
		c := dialShim(t, cfg.SocketPath)
		c.startReader()
		c.helloChunking(false)
		c.attach()
		snap := c.firstSnapshot(3 * time.Second)
		if _, err := vt.DecodeSnapshot(snap); err != nil {
			t.Fatalf("single-frame snapshot does not decode: %v", err)
		}
		if _, ok := c.snapshotInfo(); ok {
			t.Fatal("non-advertising small-grid attach sent a chunking preamble; want a bare TSnapshot")
		}
		c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	})
}

// small helper: number of TSnapshot frames seen.
func (c *shimClient) snapshotFrameCount() int {
	n := 0
	for _, f := range c.frames() {
		if f.typ == wire.TSnapshot {
			n++
		}
	}
	return n
}
