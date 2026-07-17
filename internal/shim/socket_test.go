package shim

// E4.2 — the per-session UDS serves the G2 message set (hello/attach/resize/
// stream), and E4.3 / invariant S10 — attach delivers exactly one snapshot then
// live frames with no gap or overlap at the boundary.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/Nathandela/swarm/internal/wire"
)

// E4.2 — the handshake: a client hello is answered with the shim's hello
// carrying its wire version, and that version is the frozen shimwire.Version.
func TestSocket_HelloHandshake(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	ch := runShimAsync(cfg)
	t.Cleanup(func() { waitRun(t, ch, 10*time.Second) })

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	reply := c.hello(shimwire.Version)
	if reply.Type != shimwire.TypeHello {
		t.Errorf("reply type = %q, want hello", reply.Type)
	}
	if reply.WireVersion != shimwire.Version {
		t.Errorf("reply wire_version = %d, want %d", reply.WireVersion, shimwire.Version)
	}

	// End the idling agent so Run returns.
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
}

// E4.2 — a version-mismatched hello is answered with the shim's own version so
// the client can report the skew (D-8 groundwork), then the shim closes the
// connection without serving attach/stream.
func TestSocket_HelloVersionMismatch(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	ch := runShimAsync(cfg)
	t.Cleanup(func() { waitRun(t, ch, 10*time.Second) })

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	reply := c.hello(shimwire.Version + 99) // incompatible
	if reply.WireVersion != shimwire.Version {
		t.Errorf("on mismatch, reply wire_version = %d, want the shim's own %d", reply.WireVersion, shimwire.Version)
	}

	// The shim must close the connection after the mismatch reply.
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.mu.Lock()
		err := c.readErr
		c.mu.Unlock()
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shim did not close the connection after a version-mismatch hello")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// A fresh, compatible connection still works (the mismatch closed only that
	// one connection).
	c2 := dialShim(t, cfg.SocketPath)
	c2.startReader()
	c2.hello(shimwire.Version)
	c2.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
}

// E4.2 — attach yields a decodable grid snapshot reflecting the agent's output.
func TestSocket_AttachDeliversSnapshot(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	ch := runShimAsync(cfg)
	t.Cleanup(func() { waitRun(t, ch, 10*time.Second) })

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	// Wait until the agent's marker has reached the grid, then attach.
	time.Sleep(150 * time.Millisecond)
	c.attach()

	snap := c.firstSnapshot(3 * time.Second)
	s, err := vt.DecodeSnapshot(snap)
	if err != nil {
		t.Fatalf("attach snapshot did not decode: %v", err)
	}
	if s.Cols != 80 || s.Rows != 24 {
		t.Errorf("snapshot dims = %dx%d, want 80x24", s.Cols, s.Rows)
	}
	if !strings.Contains(gridText(s), "IDLING") {
		t.Errorf("snapshot grid missing agent output %q:\n%s", "IDLING", gridText(s))
	}

	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
}

// E4.2 — a resize control resizes the emulator grid: a follow-up snapshot on a
// fresh connection reports the new dimensions (the brief's emulator-dims proof).
func TestSocket_ResizeUpdatesEmulatorDims(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	ch := runShimAsync(cfg)
	t.Cleanup(func() { waitRun(t, ch, 10*time.Second) })

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.writeControl(shimwire.Control{Type: shimwire.TypeResize, Cols: 120, Rows: 40})
	// Reconnect for a fresh snapshot (single connection at a time — v1 pin).
	c.conn.Close()

	c2 := dialShim(t, cfg.SocketPath)
	c2.startReader()
	c2.hello(shimwire.Version)
	c2.attach()
	s, err := vt.DecodeSnapshot(c2.firstSnapshot(3 * time.Second))
	if err != nil {
		t.Fatalf("decode snapshot after resize: %v", err)
	}
	if s.Cols != 120 || s.Rows != 40 {
		t.Errorf("post-resize snapshot dims = %dx%d, want 120x40", s.Cols, s.Rows)
	}

	c2.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
}

// E4.2 — a resize control reaches the PTY kernel winsize: the winsize helper
// reports 80x24 at start (initial size from cfg) and the new size after the
// resize (proving pty.Setsize on the master delivered SIGWINCH to the agent).
func TestSocket_ResizePropagatesToPTYWinsize(t *testing.T) {
	cfg := helperConfig(t, modeWinsize, nil, nil)
	ch := runShimAsync(cfg)
	t.Cleanup(func() { waitRun(t, ch, 10*time.Second) })

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	// Initial PTY winsize comes from cfg.Cols/Rows.
	c.waitOutput("WINSIZE\t24x80", 3*time.Second)
	c.waitOutput("WINSIZE_READY", 3*time.Second)

	c.writeControl(shimwire.Control{Type: shimwire.TypeResize, Cols: 100, Rows: 30})
	c.waitOutput("WINSIZE\t30x100", 3*time.Second)

	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
}

// E4.3 / S10 (deterministic boundary) — with the agent quiescent at attach time
// (blocked awaiting a trigger), the snapshot captures phase1 and the live stream
// carries exactly phase2, with the boundary marker appearing exactly once across
// snapshot+stream (no duplication = no overlap; no loss = no gap). phase2 is
// produced only after the client's trigger, which the client sends only after
// receiving the snapshot, so the split point is barrier-synchronized, not timed.
func TestContinuity_SnapshotThenStream_Boundary(t *testing.T) {
	cfg := helperConfig(t, modeStreamBlock, nil, nil)
	ch := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	// Give phase1 time to reach the grid; the agent then blocks on stdin, so no
	// output is in flight when we attach.
	time.Sleep(300 * time.Millisecond)
	c.attach()

	// Exactly one snapshot, and it is the first frame after attach — no DataOut
	// may precede it (ordering half of S10).
	snapPayload := c.firstSnapshot(3 * time.Second)
	assertSnapshotOrdering(t, c)
	snap, err := vt.DecodeSnapshot(snapPayload)
	if err != nil {
		t.Fatalf("decode attach snapshot: %v", err)
	}
	snapText := gridText(snap)
	if strings.Contains(snapText, "P2L") || strings.Contains(snapText, "PHASE2_DONE") {
		t.Errorf("snapshot contains phase2 content — it was not taken before the boundary:\n%s", snapText)
	}

	// Trigger phase2 only now, after the snapshot is in hand.
	c.writeDataIn([]byte("go\n"))

	// Drain until the shim reports the agent exited.
	c.waitControl(shimwire.TypeExitReport, 10*time.Second)
	live := string(c.dataOut())

	// phase2 is strictly post-boundary: every phase2 line and its marker must be
	// in the live stream exactly once, and nothing of phase1's marker may be
	// duplicated there.
	if strings.Count(live, "PHASE2_DONE") != 1 {
		t.Errorf("PHASE2_DONE appears %d times in the live stream, want exactly 1", strings.Count(live, "PHASE2_DONE"))
	}
	for i := 0; i < phase2Lines; i++ {
		tok := "P2L" + pad4(i)
		if n := strings.Count(live, tok); n != 1 {
			t.Errorf("phase2 line %s appears %d times in the live stream, want exactly 1 (gap or overlap)", tok, n)
		}
	}
	// The boundary marker PHASE1_DONE must appear EXACTLY ONCE across the union
	// of snapshot and live stream: 0 => it was lost at the boundary (gap),
	// 2 => it was delivered in both (overlap).
	total := strings.Count(snapText, "PHASE1_DONE") + strings.Count(live, "PHASE1_DONE")
	if total != 1 {
		t.Errorf("PHASE1_DONE appears %d times across snapshot+stream, want exactly 1 (0=gap, 2=overlap)", total)
	}

	waitRun(t, ch, 10*time.Second)
}

// E4.3 / S10 (under active output load) — attaching mid-stream still yields
// exactly one snapshot first, then only DataOut frames, and the live sequence is
// strictly increasing (no duplication/reordering) and starts strictly after the
// last number already in the snapshot (no overlap).
func TestContinuity_ActiveLoadOrdering(t *testing.T) {
	cfg := helperConfig(t, modeStreamActive, nil, nil)
	ch := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	time.Sleep(200 * time.Millisecond) // let the sequence get well underway
	c.attach()

	snap, err := vt.DecodeSnapshot(c.firstSnapshot(3 * time.Second))
	if err != nil {
		t.Fatalf("decode snapshot under load: %v", err)
	}
	assertSnapshotOrdering(t, c)
	snapMax := maxSeq(gridText(snap))

	// Collect a run of live frames, then stop the agent.
	c.waitOutput("N", 3*time.Second)
	time.Sleep(400 * time.Millisecond)
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	waitRun(t, ch, 10*time.Second)

	live := parseSeq(string(c.dataOut()))
	if len(live) < 2 {
		t.Fatalf("too few complete live sequence lines to assess continuity: %v", live)
	}
	for i := 1; i < len(live); i++ {
		if live[i] <= live[i-1] {
			t.Errorf("live sequence not strictly increasing at %d: %d then %d (duplication or reordering)", i, live[i-1], live[i])
		}
	}
	if snapMax >= 0 && live[0] <= snapMax {
		t.Errorf("first live number %d <= snapshot max %d — the boundary re-delivered already-snapshotted output (overlap)", live[0], snapMax)
	}
}

// assertSnapshotOrdering checks the frame log holds exactly one TSnapshot and
// that no TDataOut frame precedes it (S10 ordering).
func assertSnapshotOrdering(t *testing.T, c *shimClient) {
	t.Helper()
	frames := c.frames()
	snapIdx := -1
	snapCount := 0
	for i, f := range frames {
		switch f.typ {
		case wire.TSnapshot:
			snapCount++
			if snapIdx < 0 {
				snapIdx = i
			}
		case wire.TDataOut:
			if snapIdx < 0 {
				t.Errorf("a DataOut frame arrived before any snapshot (S10 ordering violated)")
			}
		}
	}
	if snapCount != 1 {
		t.Errorf("received %d snapshot frames after attach, want exactly 1", snapCount)
	}
}

var seqRE = regexp.MustCompile(`N(\d+)`)

// parseSeq extracts complete "N<int>" lines from a raw stream, dropping the
// first and last fragments (which may be partial at the attach/stop boundary).
func parseSeq(s string) []int {
	lines := strings.Split(s, "\n")
	if len(lines) <= 2 {
		return nil
	}
	var out []int
	for _, line := range lines[1 : len(lines)-1] {
		if m := seqRE.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// maxSeq returns the largest "N<int>" in text, or -1 if none.
func maxSeq(text string) int {
	max := -1
	for _, m := range seqRE.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > max {
			max = n
		}
	}
	return max
}

func pad4(i int) string {
	s := strconv.Itoa(i)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}
