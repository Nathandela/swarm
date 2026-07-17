// Epic 14 N-1 — the REAL perf gate. firstpaint_test.go's
// TestFirstPaint_FiftySessionsUnderBudget measures the render path against a
// fakeClient (no daemon, single-shot, no p95); its own comment says the real
// <=100ms p95 @50 sessions is this gate. This file is that gate:
//
//   - Stands up a REAL daemon assembly (internal/skeleton.Serve) in-process and
//     launches 50 REAL fake-agent sessions through it via the REAL
//     protocol.Client.Launch path (composeLaunchSpec resolves agent "fake" to the
//     swarm-fake-agent binary) — 50 real shim + real fake-agent OS processes
//     behind one real daemon, matching N-1's "daemon running, <=50 sessions
//     listed". We block until List() reports all 50 before measuring, so every
//     measured run's eager List() does the real 50-row round trip.
//   - Each of gateRuns runs dials a FRESH protocol.Client (untimed setup — Dial
//     is not part of first paint) and then times ONLY tui.New(client, detect)
//     through the first Update(WindowSizeMsg) + View() — the eager List() plus
//     first render, exactly what a user experiences launching the TUI.
//   - The p95 across the runs (nearest-rank) is asserted fail-closed against the
//     production budget.
//
// RACE-BUILD METHODOLOGY (documented, not a weakening): this repo's CI runs
// `go test -race ./...` for the whole module, and the race detector's
// instrumentation is well documented to add large, non-linear overhead to
// goroutine/channel-heavy code — exactly what List/Subscribe/Attach are.
// Asserting the production 100ms budget under -race would measure the detector,
// not the system. So under a race build (raceEnabled, see race_on_test.go /
// race_off_test.go) this test still performs the FULL measurement against the
// real daemon — it is never skipped — and logs the p95, but asserts only a
// generous sanity ceiling (gateRaceSanityBudget) that still catches catastrophic
// regressions (hangs, deadlocks, O(n^2) blowups). The AUTHORITATIVE N-1 gate is
// the non-race run — `go test ./internal/tui/ -count=1` (no -race), which is
// also this project's local dev command (CLAUDE.md) and how this test is meant
// to be verified.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/skeleton"
)

const (
	gateSessionCount = 50
	gateRuns         = 25

	// gateRaceSanityBudget is NOT the N-1 production number — see the race-build
	// methodology in the package doc comment above. It exists so this test never
	// degenerates into a no-op under -race, without conflating detector overhead
	// with a real regression.
	gateRaceSanityBudget = 2 * time.Second
)

// ---------------------------------------------------------------------------
// Build harness: 50 REAL sessions need the REAL swarm (shim) and
// swarm-fake-agent binaries, mirroring internal/skeleton and internal/e2e's own
// buildBinaries harnesses.
// ---------------------------------------------------------------------------

var (
	gateBuildOnce sync.Once
	gateSwarmBin  string
	gateFakeBin   string
	gateBuildErr  error
)

func gateBuildBinaries(t *testing.T) {
	t.Helper()
	gateBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tui-perfgate-bin")
		if err != nil {
			gateBuildErr = err
			return
		}
		gateSwarmBin = filepath.Join(dir, "swarm")
		gateFakeBin = filepath.Join(dir, "swarm-fake-agent")
		for _, b := range []struct{ out, pkg string }{
			{gateSwarmBin, "github.com/Nathandela/swarm/cmd/swarm"},
			{gateFakeBin, "github.com/Nathandela/swarm/cmd/swarm-fake-agent"},
		} {
			cmd := exec.Command("go", "build", "-o", b.out, b.pkg)
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				gateBuildErr = err
				return
			}
		}
	})
	// Fatal, not Skip: this is an enforced gate (must run in CI), so a broken
	// build fails closed rather than silently disappearing.
	if gateBuildErr != nil {
		t.Fatalf("cannot build perf-gate binaries: %v", gateBuildErr)
	}
}

// gateAssemble stands up one real daemon assembly over a short-pathed state dir
// (/tmp keeps the socket under the 104-byte sun_path limit).
func gateAssemble(t *testing.T) *skeleton.Daemon {
	t.Helper()
	gateBuildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "tuipg")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	sk, err := skeleton.Serve(skeleton.Config{
		StateDir:     dir,
		SocketPath:   filepath.Join(dir, "d.sock"),
		LockPath:     filepath.Join(dir, "d.lock"),
		LogPath:      filepath.Join(dir, "d.log"),
		ShimBinary:   gateSwarmBin,
		MaxSessions:  gateSessionCount + 10,
		FakeAgentBin: gateFakeBin,
	})
	if err != nil {
		t.Fatalf("skeleton.Serve: %v", err)
	}
	t.Cleanup(func() { _ = sk.Close() })
	return sk
}

// gateLaunch50 launches 50 real fake-agent sessions through the real client
// protocol (the same path a real user's launch takes) and blocks until List()
// reports all 50, so the first-paint runs measure against the fully-populated
// real roster N-1 requires.
func gateLaunch50(t *testing.T, sk *skeleton.Daemon) {
	t.Helper()
	c, err := protocol.Dial(sk.SocketPath(), []string{"attach", "subscribe"})
	if err != nil {
		t.Fatalf("protocol.Dial: %v", err)
	}
	defer c.Close()

	// Real shims are setsid-detached and independent of the daemon by design (S1
	// survival), so sk.Close() alone will NOT terminate them. SIGTERM every
	// launched session's shim (and the fake-agent it owns) directly by PID at
	// cleanup — mirrors internal/skeleton's own launchFake test harness.
	t.Cleanup(func() {
		for _, m := range sk.Core().List() {
			if m.ShimPID > 0 {
				_ = syscall.Kill(m.ShimPID, syscall.SIGTERM)
			}
		}
	})

	for i := 0; i < gateSessionCount; i++ {
		spath := filepath.Join(t.TempDir(), fmt.Sprintf("script-%02d.txt", i))
		if err := os.WriteFile(spath, []byte("idle 120s\n"), 0o600); err != nil {
			t.Fatalf("write script %d: %v", i, err)
		}
		// World-readable: the daemon spawns the shim as a separate process that
		// reads this file (mirrors internal/e2e's launchFakeSession).
		if err := os.Chmod(spath, 0o644); err != nil {
			t.Fatalf("chmod script %d: %v", i, err)
		}

		if _, err := c.Launch(protocol.LaunchReq{
			Agent:   "fake",
			Cwd:     t.TempDir(),
			Options: map[string]string{"script": spath},
			Env:     []string{"PATH=" + os.Getenv("PATH")},
			Cols:    80,
			Rows:    24,
		}); err != nil {
			t.Fatalf("Launch fake session %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(views) == gateSessionCount {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("50 real fake sessions never all appeared in List within 20s")
}

// percentile95 returns the nearest-rank p95 of d (d is not mutated).
func percentile95(d []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), d...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	rank := (95*n + 99) / 100 // ceil(0.95 * n), integer-only
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// TestFirstPaintGate_RealDaemon_FiftySessions_P95 is the Epic 14 N-1 perf gate:
// see the package doc comment above for the full methodology.
func TestFirstPaintGate_RealDaemon_FiftySessions_P95(t *testing.T) {
	sk := gateAssemble(t)
	gateLaunch50(t, sk)

	durations := make([]time.Duration, gateRuns)
	for i := 0; i < gateRuns; i++ {
		c, err := protocol.Dial(sk.SocketPath(), []string{"attach", "subscribe"})
		if err != nil {
			t.Fatalf("run %d: protocol.Dial: %v", i, err)
		}

		start := time.Now()
		m := New(c, detectMixed())
		m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 200})
		_ = m.View().Content
		durations[i] = time.Since(start)

		_ = c.Close()
	}

	p95 := percentile95(durations)
	t.Logf("first-paint p95 over %d runs @ %d real sessions: %s (raceEnabled=%v)",
		gateRuns, gateSessionCount, p95, raceEnabled)

	budget := firstPaintBudget // the real N-1 number (100ms), defined in firstpaint_test.go
	if raceEnabled {
		budget = gateRaceSanityBudget
	}
	if p95 > budget {
		t.Fatalf("first-paint p95 over %d runs @ %d real sessions = %s, budget %s (N-1, raceEnabled=%v)",
			gateRuns, gateSessionCount, p95, budget, raceEnabled)
	}
}
