package claudeinput

import "time"

const proofLifetime = 3 * time.Second

const readinessHeader = "X-Swarm-Claude-Readiness"

type ReadinessError string

const (
	ReadinessRetired     ReadinessError = "retired"
	readinessWaiting     ReadinessError = "waiting"
	readinessExpired     ReadinessError = "expired"
	readinessRecovering  ReadinessError = "recovering"
	readinessMismatch    ReadinessError = "mismatch"
	readinessLegacy      ReadinessError = "legacy_unproven"
	readinessUnconfirmed ReadinessError = "unconfirmed"
)

var readinessMessages = map[ReadinessError]string{
	ReadinessRetired:     "Claude native input is retired; explicitly refresh this session",
	readinessWaiting:     "Claude native input is waiting for sender and guard registration; retry after the mods load",
	readinessExpired:     "Claude native input heartbeat expired; retry when the sender and guard are responsive",
	readinessRecovering:  "Claude native input is waiting for fresh sender and guard proof; retry",
	readinessMismatch:    "Claude native input identity could not be verified; explicitly refresh this session",
	readinessLegacy:      "Claude native input is unproven; retry during startup or explicitly refresh this session if it persists",
	readinessUnconfirmed: "Claude native input broker readiness could not be confirmed; retry",
}

func (e ReadinessError) Error() string { return readinessMessages[e] }

func (b *Broker) liveLocked() bool { return b.readinessLocked() == "" }

func (b *Broker) readinessLocked() ReadinessError {
	switch {
	case b.revoked:
		return ReadinessRetired
	case b.guardRecovering:
		return readinessRecovering
	case b.worker.Generation == "" || b.guard.Generation == "":
		return readinessWaiting
	case b.guard.NativeSessionID != b.worker.NativeSessionID || b.guard.Version != b.worker.Version:
		return readinessMismatch
	case time.Since(b.seen) >= proofLifetime || time.Since(b.guardSeen) >= proofLifetime:
		return readinessExpired
	default:
		return ""
	}
}
