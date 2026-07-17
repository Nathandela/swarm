package shim

// Epic 14 carry-forward (agents-tracker-a7d): the arm->spawn ORDERING window.
//
// The existing TestShimSelfContainsAgentGroupOnSIGTERM proves STEADY-STATE
// containment: the handler is already armed and the agent already up when SIGTERM
// arrives. It does NOT exercise the ordering itself. shim.Run arms the
// self-containment handler (signal.Notify, buffered) BEFORE it spawns the agent
// (pty.StartWithSize), so a termination signal that arrives in the window between
// arming and spawn is buffered — not lost — and is acted on once the agent's pgid
// is known. This test drives a signal into exactly that window and asserts the
// buffered signal still contains the agent and the shim exits cleanly.

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// TestShimBuffersSignalArrivingInArmSpawnWindow delivers a SIGTERM inside the
// arm->spawn window (via the testHookAfterSignalArm seam, which runs after
// signal.Notify and before pty.StartWithSize) and asserts the shim does not die
// uncontained: the buffered signal terminates the agent and Run returns cleanly.
//
// Because the handler is armed first, the window SIGTERM lands in the buffered
// sigCh and is acted on once the pgid is known — the agent (which would otherwise
// park forever) is terminated by SIGTERM and Run returns. If someone reordered
// signal.Notify to AFTER the spawn, the seam would fire the SIGTERM before the
// handler is armed; the test-side backstop below keeps that stray SIGTERM from
// killing the test process (default disposition), so the shim simply never sees
// the signal, the idle agent parks forever, and Run hangs — surfacing as a clean
// waitRun timeout rather than a crashed test binary.
func TestShimBuffersSignalArrivingInArmSpawnWindow(t *testing.T) {
	// Backstop: register our own SIGTERM channel so a signal fired in the window
	// before the shim's handler is armed (the reordered-bug case) is absorbed here
	// instead of terminating this test process. In the correct code the shim's
	// handler is armed first, so the window SIGTERM reaches both channels; the
	// shim's copy drives containment and this copy is harmless.
	backstop := make(chan os.Signal, 1)
	signal.Notify(backstop, syscall.SIGTERM)
	t.Cleanup(func() { signal.Stop(backstop) })

	// Fire a SIGTERM into the arm->spawn window: the seam runs after the handler
	// is armed and before the agent exists.
	testHookAfterSignalArm = func() {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}
	t.Cleanup(func() { testHookAfterSignalArm = nil })

	// An idle agent that parks on stdin. It has no SIGTERM handler, so the buffered
	// window signal (delivered to its group once the pgid is known) terminates it
	// by SIGTERM regardless of how early it lands — and if the signal were lost, it
	// would park forever and hang Run.
	cfg := helperConfig(t, modeIdle, nil, nil)
	cfg.GraceTimeout = 2 * time.Second

	r := waitRun(t, runShimAsync(cfg), 10*time.Second)

	// The shim reached finalization and returned normally — it did NOT die
	// uncontained from the window signal (a killed agent is a normal outcome).
	if r.err != nil {
		t.Errorf("Run err = %v; want nil (the buffered window signal contained the agent)", r.err)
	}

	// The buffered window signal actually reached the agent: it was terminated by
	// SIGTERM, not left running (which would have hung Run) nor exited on its own.
	ei := readExitInfo(t, cfg.SessionDir)
	if ei.ExitSignal != "SIGTERM" {
		t.Errorf("exit_signal = %q; want SIGTERM (a signal buffered in the arm->spawn window must still terminate the agent)", ei.ExitSignal)
	}
	if ei.ExitCode != 128+int(syscall.SIGTERM) {
		t.Errorf("exit_code = %d; want %d (128+SIGTERM)", ei.ExitCode, 128+int(syscall.SIGTERM))
	}
}
