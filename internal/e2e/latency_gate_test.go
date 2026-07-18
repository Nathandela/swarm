package e2e

// Epic 14 N-2 — the REAL live-shim end-to-end keystroke->PTY echo latency gate.
//
// internal/attach/latency_test.go's TestPassthrough_KeystrokeEchoLatencyP95
// measures ONLY the in-memory passthrough loop's own overhead (fake session,
// no network, no PTY); its own comment states the true end-to-end keystroke
// budget over a live shim "is not asserted here or anywhere yet". This file is
// that assertion:
//
//   - Stands up a REAL assembled `swarm daemon` subprocess (buildBinaries /
//     startDrainDaemon) and launches a REAL agent session over a REAL PTY through
//     the REAL client protocol (launchFakeSession -> a real shim + agent OS process
//     pair behind the daemon).
//   - ATTACHES a real protocol.Client and measures the round trip from a client
//     keystroke write, through the protocol -> daemon -> shim -> PTY, back out as
//     the kernel line-discipline ECHO of that keystroke, delivered to the client
//     as a TDataOut frame. This is exactly N-2's "attach echo ... measured
//     keystroke->PTY write".
//   - Asserts the p95 over >= 1000 samples FAIL-CLOSED against the production
//     10ms budget.
//
// ECHO SOURCE + BUFFER SAFETY: the agent is a minimal drain agent (buildDrainAgent)
// that reads its PTY stdin and discards it. It emits NO output of its own, so the
// ONLY bytes on the attach frame stream are the PTY's own canonical-mode echoes of
// our keystrokes (ECHO is on by default; the same echo the walking-skeleton and
// mid-write kill tests observe). Each keystroke is a single printable byte followed
// by a newline: the byte echoes back verbatim (what we time) and the newline
// completes the canonical line so the drain agent reads it. That read is essential
// — an agent that never reads stdin lets keystrokes pile up in the PTY's canonical
// input buffer, which caps a single un-terminated line at MAX_CANON (1024 on macOS)
// and then rings the bell instead of echoing. Draining keeps the buffer clear, so
// the gate scales to any sample count on both macOS and Linux.
//
// RACE-BUILD METHODOLOGY (documented, not a weakening): this repo's CI runs
// `go test -race ./...`. Only the test process (the protocol.Client side) is
// race-instrumented here — the daemon and shim are separate, un-instrumented
// `go build` subprocesses — but that instrumentation still perturbs sub-10ms
// timing on the client encode/decode/channel path. So under a race build
// (raceEnabled, see race_on_test.go / race_off_test.go) the gate performs the
// FULL measurement against the real daemon — it is NEVER skipped — logs the p95,
// and asserts only a generous sanity ceiling (latRaceSanityBudget) that still
// catches catastrophic regressions (hangs, deadlocks, O(n^2) blowups). The
// AUTHORITATIVE N-2 gate is the non-race run — `go test ./internal/e2e/ -count=1`
// (no -race), which is also this project's local dev command (CLAUDE.md).

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

const (
	latSamples = 1000 // N-2 requires >= 1000 samples.
	latWarmup  = 64   // untimed warm-up (also absorbs one-time session spin-up).

	// latBudget is the N-2 production number: attach echo p95 < 10ms, measured
	// end-to-end as a keystroke -> PTY echo round trip over a live shim.
	latBudget = 10 * time.Millisecond

	// latRaceSanityBudget is NOT the N-2 number — see the race-build methodology
	// in the file doc comment. It exists so the gate never degenerates into a
	// no-op under -race without conflating detector overhead with a real regression.
	latRaceSanityBudget = 250 * time.Millisecond

	// latFirstEchoTimeout absorbs one-time session spin-up on the first keystroke;
	// latEchoTimeout bounds every subsequent round trip.
	latFirstEchoTimeout = 10 * time.Second
	latEchoTimeout      = 2 * time.Second
)

// latAlphabet cycles the keystroke byte across distinct printable, non-special
// ASCII (never an ERASE/KILL/INTR/EOF control char). The drain agent never prints
// and the newline echoes as control bytes only, so any letter/digit in a frame is
// unambiguously our echo.
var latAlphabet = []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

// TestE2E_LiveShimKeystrokeEchoLatencyP95 is the Epic 14 N-2 gate: see the file
// doc comment for the full methodology.
func TestE2E_LiveShimKeystrokeEchoLatencyP95(t *testing.T) {
	if testing.Short() {
		t.Skip("latency budget is a full-run assertion")
	}
	buildBinaries(t) // builds the real swarm (daemon+shim) binary
	drainBin := buildDrainAgent(t)
	env := newDaemonEnv(t)

	startDrainDaemon(t, env, drainBin)
	c := dial(t, env.sock)
	// The drain agent ignores the script arg; any content is fine.
	id := launchFakeSession(t, c, "drain\n")
	waitOneView(t, c)

	// The shim is setsid-detached and outlives the daemon by design (S1), and the
	// drain agent will not self-exit during the run — SIGTERM its shim at cleanup so
	// nothing leaks past the test.
	local := localOf(t, id)
	meta := readMeta(t, env.stateDir, local)
	t.Cleanup(func() {
		if meta.ShimPID > 0 {
			_ = syscall.Kill(meta.ShimPID, syscall.SIGTERM)
		}
	})

	a, err := c.Attach(id)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	frames := a.Frames()

	// roundTrip times one keystroke: drain any residual frames so the next letter we
	// read is THIS keystroke's echo, stamp, send a single printable byte plus a
	// newline (the byte echoes back; the newline flushes the line to the drain
	// agent), and time until the frame carrying the byte's echo arrives. Fail-closed
	// on timeout.
	roundTrip := func(b byte, timeout time.Duration) time.Duration {
		t.Helper()
		for { // non-blocking drain
			select {
			case <-frames:
				continue
			default:
			}
			break
		}
		start := time.Now()
		if err := a.Input([]byte{b, '\n'}); err != nil {
			t.Fatalf("Input: %v", err)
		}
		deadline := start.Add(timeout)
		for {
			select {
			case f, ok := <-frames:
				if !ok {
					t.Fatal("attach frame stream closed mid-measurement")
				}
				if bytes.IndexByte(f, b) >= 0 {
					return time.Since(start)
				}
				// Frames without our byte (e.g. the previous newline's echo) are
				// skipped; keep waiting within the deadline rather than mis-time.
			case <-time.After(time.Until(deadline)):
				t.Fatalf("keystroke %q never echoed back within %v", b, timeout)
			}
		}
	}

	for i := 0; i < latWarmup; i++ {
		to := latEchoTimeout
		if i == 0 {
			to = latFirstEchoTimeout // the first keystroke also waits out session spin-up
		}
		_ = roundTrip(latAlphabet[i%len(latAlphabet)], to)
	}

	durations := make([]time.Duration, latSamples)
	for i := 0; i < latSamples; i++ {
		durations[i] = roundTrip(latAlphabet[i%len(latAlphabet)], latEchoTimeout)
	}

	p95 := latP95(durations)
	budget := latBudget
	if raceEnabled {
		budget = latRaceSanityBudget
	}
	t.Logf("live-shim keystroke->PTY echo p95 over %d samples = %s (budget %s, raceEnabled=%v)",
		latSamples, p95, budget, raceEnabled)
	if p95 >= budget {
		t.Fatalf("live-shim keystroke echo p95 = %s, want < %s (N-2, raceEnabled=%v)",
			p95, budget, raceEnabled)
	}

	if err := a.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
}

// buildDrainAgent builds a minimal REAL agent that reads its PTY stdin and
// discards it until EOF. A drain agent (vs. an idle one that never reads stdin) is
// what lets the gate stream 1000+ keystrokes without the PTY's canonical input
// buffer overflowing at MAX_CANON — see the file doc comment. It ignores argv (the
// reserved-"fake" resolver passes a script path); kernel ECHO still echoes each
// keystroke, which is what we time.
func buildDrainAgent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	const source = `package main

import (
	"io"
	"os"
)

// Reads stdin and discards it until EOF, ignoring argv. Kernel PTY ECHO provides
// the echo the N-2 latency gate times; this read only keeps typed lines from
// piling up un-read in the PTY canonical buffer.
func main() { _, _ = io.Copy(io.Discard, os.Stdin) }
`
	if err := os.WriteFile(src, []byte(source), 0o644); err != nil {
		t.Fatalf("write drain agent source: %v", err)
	}
	bin := filepath.Join(dir, "swarm-drain-agent")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build drain agent: %v", err)
	}
	return bin
}

// startDrainDaemon spawns a real `swarm daemon` subprocess whose reserved-"fake"
// agent resolves to agentBin (the drain agent), and waits until its socket answers
// the full client protocol handshake. It mirrors startDaemon (skeleton_e2e_test.go)
// but overrides SWARM_FAKE_AGENT_BIN; the caller's t.Cleanup kills the daemon.
func startDrainDaemon(t *testing.T, env daemonEnv, agentBin string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(swarmBin, "daemon")
	cmd.Env = append(os.Environ(),
		"SWARM_DAEMON_STATE="+env.stateDir,
		"SWARM_DAEMON_SOCK="+env.sock,
		"SWARM_DAEMON_LOCK="+env.lock,
		"SWARM_DAEMON_LOG="+env.log,
		envFakeAgentBin+"="+agentBin,
	)
	logf, _ := os.Create(filepath.Join(env.stateDir, "daemon.stdio"))
	if logf != nil {
		cmd.Stdout, cmd.Stderr = logf, logf
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start swarm daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, err := protocol.Dial(env.sock, []string{"attach", "subscribe"})
		if err == nil {
			_ = c.Close()
			return cmd
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("swarm daemon never served the client protocol on %s within 10s", env.sock)
	return nil
}

// latP95 returns the nearest-rank p95 of ds (ds is not mutated).
func latP95(ds []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), ds...)
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
