package converge

// The rule-1 gateway retry (lifecycle R3; plan-audit codex finding 2): the
// daemon/gateway pair was sequential, not transactional -- a night whose daemon
// restart succeeded and whose gateway restart failed left the gateway stale
// forever, because every later night matched at rule 1 and exited before
// touching it. Rule 1's already-converged exit now ENSURES an installed
// gateway, and a gateway that cannot be ensured downgrades the night to failed.

import (
	"errors"
	"io"
	"testing"
)

func convergedDeps(ensure func() error) Deps {
	// Every dep non-nil: Run's preflight refuses a partial wiring outright
	// (deliberately -- a nil dep is an assembly bug, not a runtime state). The
	// ones after rule 1 must never fire on the already-converged path, and say
	// so loudly if they do.
	return Deps{
		Version:       "1.2.3",
		LockFree:      func() bool { return false },
		Hello:         func() (string, error) { return "1.2.3", nil },
		Sessions:      func() ([]Session, error) { panic("rule 2 reached on an already-converged night") },
		SavedEnv:      func() ([]string, error) { panic("rule 3 reached on an already-converged night") },
		RestartDaemon: func([]string) error { panic("rule 4 reached on an already-converged night") },
		RestartGateway: func() error {
			panic("RestartGateway fired where only Ensure belongs")
		},
		EnsureGateway: ensure,
		Log:           io.Discard,
	}
}

func TestAlreadyConvergedStillEnsuresTheGateway(t *testing.T) {
	called := 0
	code := Run(convergedDeps(func() error { called++; return nil }))
	if code != ExitConverged {
		t.Fatalf("exit = %d, want converged", code)
	}
	if called != 1 {
		t.Fatalf("EnsureGateway called %d times, want 1 -- the stale-gateway night must be retried", called)
	}
}

func TestAGatewayThatCannotBeEnsuredFailsTheNight(t *testing.T) {
	code := Run(convergedDeps(func() error { return errors.New("unit wedged") }))
	if code != ExitFailed {
		t.Fatalf("exit = %d, want failed: a converged daemon behind a dead gateway must not read as success", code)
	}
}

func TestANilEnsureGatewayIsTheOldBehavior(t *testing.T) {
	if code := Run(convergedDeps(nil)); code != ExitConverged {
		t.Fatalf("exit = %d, want converged: a caller predating the retry is unchanged", code)
	}
}
