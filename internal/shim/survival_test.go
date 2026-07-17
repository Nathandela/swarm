package shim

// E4.6 / invariants S1 (shim half) and S9 — the shim always drains the PTY,
// regardless of any client. With no consumer it runs the agent to completion;
// with a wedged consumer it drops frames from a bounded queue rather than
// blocking the drain, and the grid stays authoritative.

import (
	"regexp"
	"strconv"
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

var floodIdxRE = regexp.MustCompile(`F(\d+)`)

// maxFloodIndex returns the largest "F<n>" line index visible in grid text, or
// -1 if none.
func maxFloodIndex(grid string) int {
	max := -1
	for _, m := range floodIdxRE.FindAllStringSubmatch(grid, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > max {
			max = n
		}
	}
	return max
}

// E4.6 / S9 (+ S1 shim half) — a wedged consumer (attaches, then never reads)
// must not stall the PTY drain. The agent floods continuously; the wedged
// client's socket buffer fills, its bounded outbound queue fills, and further
// frames are DROPPED (FramesDropped increments) rather than buffered without
// bound. A drop can only be counted from inside the drain's feed path, so drops
// prove the drain kept running while the consumer was stuck. The grid stays
// authoritative: after disconnecting the wedged client, a fresh reconnect sees
// the flood advanced far past a single screen, output produced while nothing was
// consuming.
func TestSurvival_WedgedConsumerDropsFramesGridAuthoritative(t *testing.T) {
	cfg := helperConfig(t, modeFloodIdle, nil, nil)
	ch := runShimAsync(cfg)

	// Wedged client: hello + attach, then never read a single frame.
	wedged := dialShim(t, cfg.SocketPath)
	wedged.writeControl(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	wedged.writeControl(shimwire.Control{Type: shimwire.TypeAttach})

	// Poll the drop counter directly: overflow happens once ~subQueueCap
	// read-chunks accrue behind the blocked socket writer, which is robust to
	// chunk size and emulator throughput (incl. the -race slowdown) in a way a
	// fixed sleep is not.
	deadline := time.Now().Add(20 * time.Second)
	for cfg.Metrics.FramesDropped.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no frames dropped within 20s under a wedged consumer — the bounded-queue drop path did not engage (S9)")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The grid stayed authoritative while the consumer was wedged: the emulator
	// is fed every chunk before the (dropping) subscriber enqueue, so a fresh
	// reconnect shows the flood well past one screenful. Disconnect the wedged
	// client first (single connection at a time — v1 pin).
	wedged.conn.Close()
	fresh := dialShim(t, cfg.SocketPath)
	fresh.startReader()
	fresh.hello(shimwire.Version)
	fresh.attach()
	snap, err := vt.DecodeSnapshot(fresh.firstSnapshot(5 * time.Second))
	if err != nil {
		t.Fatalf("decode reconnect snapshot: %v", err)
	}
	if idx := maxFloodIndex(gridText(snap)); idx < 100 {
		t.Errorf("reconnect grid only advanced to line F%d (want >= 100) — the drain did not stay authoritative while the consumer was wedged (S1/S9):\n%s", idx, gridText(snap))
	}

	fresh.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	waitRun(t, ch, 10*time.Second)
}
