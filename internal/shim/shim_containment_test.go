package shim

// Audit-004 N2 containment primitive (Fix A): a catchable termination of the shim
// process must first run the agent's process-group TERM->grace->KILL, so the shim
// exiting implies the agent group was killed — no socket round-trip, no startup or
// acceptLoop window race. New file; existing shim tests are untouched.

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// TestShimSelfContainsAgentGroupOnSIGTERM asserts Fix A: an OS SIGTERM to the shim
// process TERM->grace->KILLs the agent's whole process group before the shim exits.
// The agent ignores TERM and has a same-group child (modeTermStubborn), so only the
// shim's own containment can end them; a socket signal is never sent.
//
// The shim runs in this test process (runShimAsync), and its handler is armed before
// the agent is spawned, so the SIGTERM is intercepted (the test process is NOT
// terminated) and drives the group termination.
func TestShimSelfContainsAgentGroupOnSIGTERM(t *testing.T) {
	cfg := helperConfig(t, modeTermStubborn, nil, nil)
	cfg.GraceTimeout = 800 * time.Millisecond
	ch := runShimAsync(cfg)

	// Read the agent's reported PIDs — proof the agent is up (and thus the shim's
	// signal handler, installed before the spawn, is armed) before we signal.
	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	parentPID := waitPID(t, c, "PARENT_PID", 5*time.Second)
	childPID := waitPID(t, c, "CHILD_PID", 5*time.Second)
	if parentPID == childPID {
		t.Fatalf("parent and child report the same pid %d", parentPID)
	}

	// OS SIGTERM to the shim PROCESS (not a socket signal). Notify is active, so this
	// does not terminate the process; the shim's handler contains the agent group.
	sent := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM self: %v", err)
	}

	// The shim exits (Run returns) after containing the agent.
	r := waitRun(t, ch, 10*time.Second)
	if r.err != nil {
		t.Errorf("Run err = %v; want nil (a killed agent is a normal outcome)", r.err)
	}
	// KILL only after the grace window: the agent ignored TERM, so containment
	// escalated TERM->KILL rather than default-killing.
	if elapsed := time.Since(sent); elapsed < cfg.GraceTimeout*3/4 {
		t.Errorf("shim contained the agent in %s, sooner than the %s grace — escalation misfired", elapsed, cfg.GraceTimeout)
	}

	// The whole agent group is dead (containment reached via the OS signal path).
	if !processGone(parentPID, 3*time.Second) {
		t.Errorf("agent pid %d still alive after SIGTERM to the shim; not contained (N2)", parentPID)
	}
	if !processGone(childPID, 3*time.Second) {
		t.Errorf("descendant pid %d still alive after SIGTERM to the shim; group not contained", childPID)
	}
	ei := readExitInfo(t, cfg.SessionDir)
	if ei.ExitSignal != "SIGKILL" {
		t.Errorf("exit_signal = %q; want SIGKILL (agent ignored TERM, so KILL at grace ended it)", ei.ExitSignal)
	}
}
