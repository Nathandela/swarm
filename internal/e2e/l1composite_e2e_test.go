// Epic 14 T10 — the L1 COMPOSITE (E14.8, contracts row 14, invariant L1, EARS
// V-2). Today the ≤1 s status pipeline is proven in HALVES: the server half
// (protocol/fanout_test.go TestFanout_StatusChangeReachesLiveSubscriberWithin1s)
// and the client half (tui/liveness_test.go TestLiveness_EventMovesRowGroup) each
// stand alone against a stub. Nothing chains a REAL status signal through the WHOLE
// assembled pipeline — engine normalize -> protocol fan-out -> a real TUI's render —
// and nothing does it UNDER OUTPUT LOAD.
//
// This test closes that gap (and finishes scenario 4's ≤1 s-through-to-TUI leg): a
// real `swarm daemon` runs a fake session whose PTY is kept BUSY (a steady stream of
// output the daemon taps into the status engine on every line); a REAL bubbletea TUI
// (internal/tui, driven through teatest/v2) subscribes to that daemon over a REAL
// protocol.Client; an authenticated `swarm hook` callback bearing the session's live
// token flips its status to needs-input; and the assertion is that the session's new
// status appears in the RENDERED TUI view within 1 s of the signal — while the output
// load is still flowing. It fails CLOSED on >1 s (the L1 bound), and reports the
// measured signal->render latency. COST: fake agent only; no billable real-CLI run.
//
// Shared harness (buildBinaries, newDaemonEnv, startDaemon, dial, waitOneView,
// localOf) lives in skeleton_e2e_test.go; the hook helpers (readHookToken) in
// engine_wiring_e2e_test.go — all the same package.
package e2e

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/tui"
)

// The real protocol.Client is exactly the narrow surface the TUI consumes, so the
// composite drives the SAME tui.Model the product runs — no shim, no adapter type.
var _ tui.Client = (*protocol.Client)(nil)

// l1RenderBound is the hard ≤1 s L1 ceiling the composite asserts fail-closed: the
// hook-driven status must reach the rendered TUI within this window (spec L1 / V-2).
const l1RenderBound = time.Second

// ansiSeqRe matches a CSI escape sequence so the (control-laden) teatest program
// stream can be reduced to plain text before substring assertions — the same
// normalization the tui suite applies to its renders.
var ansiSeqRe = regexp.MustCompile("\x1b\\[[0-9:;<=>?]*[ -/]*[@-~]")

func stripANSIBytes(b []byte) []byte { return ansiSeqRe.ReplaceAll(b, nil) }

// TestE2E_L1Composite_SignalReachesRenderedTUIWithin1sUnderLoad proves the FULL L1
// pipeline end-to-end under output load: status signal -> engine normalize ->
// protocol fan-out -> rendered TUI, within 1 s (fail-closed).
func TestE2E_L1Composite_SignalReachesRenderedTUIWithin1sUnderLoad(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)
	startDaemon(t, env)

	// A launcher client starts the busy session; a SEPARATE client backs the TUI, so
	// the TUI's subscribe stream is a genuine second connection fanning out to it.
	// loadStart is captured BEFORE launch, so it is a conservative lower bound on when
	// the agent begins printing — the under-load certification below leans on it.
	launcher := dial(t, env.sock)
	script, guaranteedBusy := busyLoadScript()
	loadStart := time.Now()
	id := launchFakeSession(t, launcher, script)
	waitOneView(t, launcher)
	local := localOf(t, id)
	token := readHookToken(t, env.stateDir, local)

	// Build the REAL TUI over a REAL client. New eagerly lists (so the busy session is
	// on the board at first paint) and subscribes (so the hook's status change flows in
	// live). detect is unused here — the launch form is never opened.
	tc := dial(t, env.sock)
	model := tui.New(tc, func() []tui.AgentInfo { return nil })
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() { _ = tm.Quit() })

	// The busy session must be rendered (under WORKING) BEFORE the signal, so the move
	// to NEEDS INPUT is an observable transition and the latency clock starts from a
	// settled board — not from first paint.
	waitRendered(t, tm, "WORKING", time.Now(), 5*time.Second)

	// Inject the authenticated status signal: a `swarm hook` callback with the
	// session's REAL token flips it to needs-input (turn=idle + interaction=permission,
	// the same posture the engine-wiring suite drives). Sequence 1 is fresh.
	signal := engine.Callback{
		SessionID: local,
		Token:     token,
		Sequence:  1,
		Event:     "Notification",
		Payload: map[string]string{
			engine.PayloadKeyTurn:        string(status.TurnIdle),
			engine.PayloadKeyInteraction: string(status.InteractionPermission),
		},
	}
	start := time.Now()
	if err := hookclient.Post(env.sock, signal); err != nil {
		t.Fatalf("post authenticated status signal: %v", err)
	}

	// The rendered TUI must show the new status within 1 s of the signal. This measures
	// the WHOLE pipeline (engine -> fan-out -> client -> model -> View) from the moment
	// the signal is injected, and fails closed past the L1 bound, WHILE the busy PTY
	// keeps loading the daemon's output path.
	latency := waitRendered(t, tm, "NEEDS INPUT", start, l1RenderBound)

	// Certify the propagation was proven UNDER load: the fake agent's idle steps are
	// REAL sleeps, so the script guarantees at least guaranteedBusy of steady printing
	// from loadStart. If less than that has elapsed, the agent is still mid-print — the
	// daemon's output path was actively loaded across the whole measured window. (The
	// session is also confirmed still running, so its status was live, not terminal.)
	if elapsed := time.Since(loadStart); elapsed >= guaranteedBusy {
		t.Fatalf("cannot certify under-load: %s elapsed since load start >= guaranteed busy window %s "+
			"(the print stream may have ended before the ≤1 s window closed); lengthen busyLoadScript", elapsed, guaranteedBusy)
	}
	if !loadStillFlowing(t, launcher, id) {
		t.Fatal("session no longer running when the ≤1 s window closed — its status would be terminal, " +
			"not the live needs-input the composite asserts")
	}
	t.Logf("L1 composite: signal->rendered-TUI latency = %s (bound %s), under output load "+
		"(%s into a guaranteed %s print stream)", latency, l1RenderBound, time.Since(loadStart), guaranteedBusy)

	// Final render (a fresh View() of the finished model) confirms the move is real and
	// stable: the row is under NEEDS INPUT and its old WORKING placement is gone.
	_ = tm.Quit()
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	final := ansiSeqRe.ReplaceAllString(fm.View().Content, "")
	if !strings.Contains(final, "NEEDS INPUT") {
		t.Fatalf("final render does not show the hook-driven status:\n%s", final)
	}
	if strings.Contains(final, "WORKING") {
		t.Fatalf("final render still shows the stale WORKING placement after the move:\n%s", final)
	}
}

// waitRendered drains the teatest program's output stream into a local accumulator
// until its ANSI-stripped text contains want, returning the elapsed time since the
// call. It fails CLOSED if want is not rendered within bound — this is how the L1
// ≤1 s ceiling is enforced. It mirrors teatest.WaitFor's tee-and-accumulate read
// (the output buffer drains on read), but measures and reports latency.
func waitRendered(t *testing.T, tm *teatest.TestModel, want string, start time.Time, bound time.Duration) time.Duration {
	t.Helper()
	var acc bytes.Buffer
	for {
		if _, err := io.ReadAll(io.TeeReader(tm.Output(), &acc)); err != nil {
			t.Fatalf("read TUI output: %v", err)
		}
		if bytes.Contains(stripANSIBytes(acc.Bytes()), []byte(want)) {
			return time.Since(start)
		}
		if time.Since(start) > bound {
			t.Fatalf("%q not rendered within %s (fail-closed on the L1 bound); last render:\n%s",
				want, bound, stripANSIBytes(acc.Bytes()))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// loadStillFlowing reports whether the busy session is still running (its PTY still
// producing output) at the moment the ≤1 s assertion resolved, so the composite can
// certify the propagation was proven UNDER load rather than after it quiesced.
func loadStillFlowing(t *testing.T, c *protocol.Client, id string) bool {
	t.Helper()
	views, err := c.List()
	if err != nil {
		t.Fatalf("List (load-liveness check): %v", err)
	}
	for _, v := range views {
		if v.ID == id {
			return v.Status.Process == status.ProcessRunning
		}
	}
	return false
}

// busyLoadScript builds a fake-agent script that keeps the PTY BUSY: a steady stream
// of distinct printed lines with a small real sleep between each, so the daemon taps
// output into the status engine continuously. It returns the script and the MINIMUM
// duration that steady printing is guaranteed to last (lines * the per-line sleep, a
// lower bound since each idle is a real sleep and print adds more) — the caller uses
// it to certify the ≤1 s window closed WHILE printing was still ongoing. There is no
// long trailing idle: the print stream itself keeps the session alive across the
// (~1 s) measured window, and the agent self-exits once it drains, so no long-lived
// orphan is left after the daemon is torn down.
//
// The printed lines are prose (no trailing prompt sentinel), so the grid heuristic
// reads turn=unknown (GroupWorking) and the session sits under WORKING until the hook
// moves it — it never drifts into needs-input on its own.
func busyLoadScript() (script string, guaranteedBusy time.Duration) {
	const (
		lines   = 2500
		perLine = 4 * time.Millisecond
		lineFmt = "print LOAD-%04d busy pty output line\nidle 4ms\n"
	)
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, lineFmt, i)
	}
	return b.String(), lines * perLine // >= 10 s of steady prints; the window closes far sooner
}
