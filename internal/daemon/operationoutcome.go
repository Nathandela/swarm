package daemon

// Wave R5 (bead agents-tracker-hggx.6): the READ-ONLY operation-status
// reconciliation surface over the EXISTING two-phase launch reservation (ADR-007 D6
// is the source of truth; ADR-017 T9 is the delivery vocabulary). The playbook's bar
// (deliverable 2): a launch that dies mid-flight resolves authoritative or
// outcome_unknown, NEVER silent -- and the read has no side effect, because
// operation_status must never authorize a retry (playbook:449). The replay of the
// SAME signed operation id through Launch is the one re-driver.

import (
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/status"
)

// Stable operation-outcome states (the machine side of schema.OutcomeApplied /
// schema.OutcomeUnknown; spelled here rather than imported because the daemon
// package sits below the wire schema).
const (
	OpStateApplied        = "applied"
	OpStateOutcomeUnknown = "outcome_unknown"
)

// OpOutcome is one operation's reconciliation answer.
type OpOutcome struct {
	State     string
	SessionID string
}

// OperationOutcome answers the reconciliation question for one operation id.
//
//   - ok=false for an id this daemon has NO record of: the protocol layer turns that
//     into the stable unknown_operation answer; what the daemon must never do is
//     fabricate an outcome for it.
//   - applied (authoritative, with the session id) when the operation's recorded
//     session is present and usable -- the same liveness test resolveReplay applies,
//     so status and replay can never disagree about whether the launch happened.
//     Usable requires a RECORDED SHIM (ShimPID != 0), exactly as resolveReplay's
//     phantom rule does (round 3, review MAJOR 1): the phase-1 reservation persists a
//     Running meta BEFORE any process exists, and calling that shape "applied" is an
//     authoritative POSITIVE for a launch that can still fail and roll back.
//   - applied ALSO for any COMPLETED record, whatever became of its session -- row GONE
//     (round 3, review LOW) or since LOST (round 4, review LOW 4). launch() records the
//     terminal phase when it returns success, and that record survives both Delete and
//     reconcile: the machine CAN prove the launch happened, and a deletion or a lost
//     shim is a later, separate fact about the PROCESS. resolveReplay refuses the
//     re-drive for BOTH shapes (ErrLaunchOpConsumed), so status and replay still agree.
//   - outcome_unknown when the record exists but is undecidable: its session is missing
//     or LOST with a mid-flight (non-terminal) record, or it is a phantom reservation.
//
// The read touches nothing: no Fail, no Redrive, no launch -- asking twice returns
// the identical answer.
func (d *Daemon) OperationOutcome(operationID string) (OpOutcome, bool) {
	rec, ok := d.idem.Get(operationID)
	if !ok {
		return OpOutcome{}, false
	}
	d.mu.Lock()
	s, present := d.sessions[rec.SessionID]
	usable := present && s.meta.Status.Process != status.ProcessLost && s.meta.ShimPID != 0
	d.mu.Unlock()
	// composer_send owns an exact coded terminal outcome in the idempotency record, but
	// operation_status's launch-era vocabulary can express only applied/outcome_unknown.
	// A running target session is not evidence that a FAILED message was delivered: report
	// applied only for the completed phase and otherwise choose the honest non-positive.
	if rec.Action == "composer_send" {
		if rec.Phase == idempotency.PhaseCompleted {
			return OpOutcome{State: OpStateApplied, SessionID: rec.SessionID}, true
		}
		return OpOutcome{State: OpStateOutcomeUnknown}, true
	}
	if usable || rec.Phase == idempotency.PhaseCompleted {
		return OpOutcome{State: OpStateApplied, SessionID: rec.SessionID}, true
	}
	return OpOutcome{State: OpStateOutcomeUnknown}, true
}
