package shim

// E4.6 / invariants S1 (shim half) and S9 — the shim always drains the PTY,
// regardless of any client. With no consumer it runs the agent to completion;
// with a wedged consumer it drops frames from a bounded queue rather than
// blocking the drain, and the grid stays authoritative.

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/vt"
)

// E4.6 / S1 — with NO client ever connecting, the shim drains the PTY and the
// agent runs to completion; all its output is captured in the grid + transcript.
func TestSurvival_DrainsWithNoConsumer(t *testing.T) {
	cfg := helperConfig(t, modeBurstExit, nil, nil)
	r := waitRun(t, runShimAsync(cfg), 20*time.Second)
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	tr := readTranscript(t, cfg.SessionDir)
	if !strings.Contains(tr, "BURST_DONE") {
		t.Errorf("transcript missing BURST_DONE — the shim did not drain to completion without a consumer")
	}
	// The last burst line reached the grid too.
	snap := decodeFinalSnapshot(t, cfg.SessionDir)
	if !strings.Contains(gridText(snap), "BURST_DONE") {
		t.Errorf("final grid missing BURST_DONE:\n%s", gridText(snap))
	}
}

// E4.6 / S9 — a wedged consumer (attaches, then never reads) must not stall the
// PTY drain: the agent keeps producing, frames are dropped from the shim's
// bounded per-conn queue (FramesDropped increments), and a fresh reconnect sees
// the output produced WHILE the consumer was wedged (FLOOD_DONE in the grid).
func TestSurvival_WedgedConsumerDropsFramesGridAuthoritative(t *testing.T) {
	cfg := helperConfig(t, modeFloodIdle, nil, nil)
	ch := runShimAsync(cfg)

	// Wedged client: hello + attach, then never read a single frame.
	wedged := dialShim(t, cfg.SocketPath)
	wedged.writeControl(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	wedged.writeControl(shimwire.Control{Type: shimwire.TypeAttach})

	// Let the agent flood while the consumer is wedged.
	time.Sleep(2 * time.Second)

	// The PTY drain must have continued: a fresh connection's snapshot shows the
	// tail the agent produced while the wedged client was stuck.
	wedged.conn.Close()
	fresh := dialShim(t, cfg.SocketPath)
	fresh.startReader()
	fresh.hello(shimwire.Version)
	fresh.attach()
	snap, err := vt.DecodeSnapshot(fresh.firstSnapshot(5 * time.Second))
	if err != nil {
		t.Fatalf("decode reconnect snapshot: %v", err)
	}
	if !strings.Contains(gridText(snap), "FLOOD_DONE") {
		t.Errorf("reconnect grid missing FLOOD_DONE — the PTY drain stalled behind the wedged consumer (S9 violated):\n%s", gridText(snap))
	}

	// Frames were dropped (bounded queue), proving memory stays bounded rather
	// than buffering the whole flood for a stuck consumer.
	if cfg.Metrics.FramesDropped.Load() == 0 {
		t.Errorf("FramesDropped = 0, want > 0 — a wedged consumer must cause bounded-queue drops, not unbounded buffering")
	}

	fresh.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	waitRun(t, ch, 10*time.Second)
}
