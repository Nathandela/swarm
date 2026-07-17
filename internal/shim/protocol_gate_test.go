package shim

// Epic 4 review-fix round (audit-003), F5 — resize validation + handshake gate.
// NEW tests only; the frozen designer files are not touched.
//
//	F5a  resize cols/rows outside [1,1000] are ignored (no panic, no OOM)
//	F5b  resize/input/signal are ignored until a hello frame arrives

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/vt"
)

// F5a — a resize carrying negative or absurdly large dimensions must not reach
// the emulator: it is ignored, leaving the grid at its last valid size. Pre-fix
// the raw values are passed straight to the emulator (panic / OOM risk).
func TestF5_OutOfRangeResizeIgnored(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	ch := runShimAsync(cfg)
	t.Cleanup(func() { waitRun(t, ch, 10*time.Second) })

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	// A valid resize takes effect...
	c.writeControl(shimwire.Control{Type: shimwire.TypeResize, Cols: 120, Rows: 40})
	// ...then out-of-range resizes must be ignored, not applied nor crashy.
	c.writeControl(shimwire.Control{Type: shimwire.TypeResize, Cols: -5, Rows: -5})
	c.writeControl(shimwire.Control{Type: shimwire.TypeResize, Cols: 100000, Rows: 100000})
	c.writeControl(shimwire.Control{Type: shimwire.TypeResize, Cols: 0, Rows: 0})
	c.conn.Close()

	c2 := dialShim(t, cfg.SocketPath)
	c2.startReader()
	c2.hello(shimwire.Version)
	c2.attach()
	s, err := vt.DecodeSnapshot(c2.firstSnapshot(3 * time.Second))
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if s.Cols != 120 || s.Rows != 40 {
		t.Errorf("post-resize dims = %dx%d, want 120x40 (out-of-range resizes must be ignored)", s.Cols, s.Rows)
	}

	c2.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
}

// F5b — frames sent before a hello must be ignored: a pre-hello signal must NOT
// terminate the agent, and a pre-hello resize must NOT take effect. Only after
// the hello handshake are operations honored. Pre-fix the shim acts on frames
// from an un-greeted connection.
func TestF5_OperationsGatedBehindHello(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	ch := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	// Kill BEFORE hello — must be ignored.
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	c.writeControl(shimwire.Control{Type: shimwire.TypeResize, Cols: 120, Rows: 40})

	// The agent must still be running: Run has not returned.
	select {
	case r := <-ch:
		t.Fatalf("Run returned (exit=%d err=%v) after a pre-hello signal — the handshake gate did not hold", r.exit, r.err)
	case <-time.After(1 * time.Second):
	}

	// Now handshake and confirm the pre-hello resize never took effect.
	c.hello(shimwire.Version)
	c.attach()
	s, err := vt.DecodeSnapshot(c.firstSnapshot(3 * time.Second))
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if s.Cols != 80 || s.Rows != 24 {
		t.Errorf("dims = %dx%d, want 80x24 (a pre-hello resize must be ignored)", s.Cols, s.Rows)
	}

	// A post-hello signal is honored, so Run finishes.
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	waitRun(t, ch, 10*time.Second)
}
