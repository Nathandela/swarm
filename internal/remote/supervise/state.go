package supervise

import "errors"

// State is a supervision state (PB-LIFE-3). There are three, and the whole point of the
// first one is that it is NOT a failure.
type State int

const (
	// StateQuiescent: installed, not running, and nothing wrong. A machine with no paired
	// device has nothing to serve.
	StateQuiescent State = iota
	// StateActive: exactly one device paired; the gateway runs.
	StateActive
	// StateFailed: exited for a reason that is not quiescence, often enough that the
	// supervisor gave up restarting it.
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateQuiescent:
		return "quiescent"
	case StateActive:
		return "active"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Desired reports the state the unit must be in for a given paired-device count. It is
// the ONE definition of quiescence, shared by the CLI, the unit and the gateway binary.
//
// More than one device is quiescent too: cmd/swarm-remote's resolveGatewayParams refuses
// that count as hard as it refuses zero, so a unit that tried to run would loop just the
// same.
func Desired(deviceCount int) State {
	if deviceCount == 1 {
		return StateActive
	}
	return StateQuiescent
}

const (
	// ExitQuiescent is "nothing to serve", and it is a SUCCESS status on purpose.
	// launchd's KeepAlive has no per-exit-code list -- the only status that stops it
	// restarting a job is a successful one -- so a nonzero quiescent code could be
	// expressed in systemd (SuccessExitStatus=) and NOT in launchd, and the two unit
	// types would then disagree about the single case that matters. A zero status makes
	// ONE policy (restart on failure only) correct on both.
	ExitQuiescent = 0
	// ExitFailure is a real failure: the supervisor restarts it, throttled.
	ExitFailure = 1
)

// ErrQuiescent marks a gateway outcome that is not a failure. cmd/swarm-remote wraps BOTH
// quiescent outcomes in it: the zero-paired-device refusal at startup (PB-LIFE-3(a)) and
// remotegw.ErrDeviceRevoked at runtime (PB-LIFE-3(c)).
var ErrQuiescent = errors.New("supervise: nothing to serve; gateway quiescent")

// ExitCodeFor is the exit status the units' restart policy is written against.
func ExitCodeFor(err error) int {
	if err == nil || errors.Is(err, ErrQuiescent) {
		return ExitQuiescent
	}
	return ExitFailure
}

// ShouldRestart is what both unit types do with an exit status, in Go:
// launchd's KeepAlive{SuccessfulExit:false} and systemd's Restart=on-failure.
func ShouldRestart(exitCode int) bool { return exitCode != ExitQuiescent }
