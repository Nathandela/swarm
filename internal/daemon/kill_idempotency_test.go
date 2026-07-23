package daemon

// FAILING-FIRST daemon-integration test for kill/delete idempotency (slice DHI-3): the
// durable backing of protocol.IdempotentExecutor. ClaimIdempotentOp returns the ORIGINAL
// attempt's cached outcome on a replay — a committed-SUCCESS op replays as (existed=true,
// priorOK=true); a committed-FAILURE op replays as (existed=true, priorOK=false), a cached
// failure NEVER a false success. CommitIdempotentOp records that terminal outcome durably.
// RED is undefined-only: these methods do not exist yet, so the daemon test package fails
// to compile.
//
// Expected seam (the GREEN implementer's deliverable), backed by the existing two-phase
// idempotency.Store (Prepare/Complete/Fail + the Phase constants):
//
//	func (d *Daemon) ClaimIdempotentOp(op, action, session string) (existed, priorOK bool, err error) {
//	    rec, existed, err := d.idem.Prepare(op, action, session)
//	    if err != nil || !existed { return existed, false, err }
//	    switch rec.Phase {
//	    case idempotency.PhaseCompleted: return true, true, nil
//	    case idempotency.PhaseFailed:    return true, false, nil
//	    default: return false, false, nil // prepared/executing: self-idempotent, safe to re-run
//	    }
//	}
//	func (d *Daemon) CommitIdempotentOp(op string, ok bool) error {
//	    if ok { return d.idem.Complete(op, nil) }
//	    return d.idem.Fail(op, nil)
//	}

import "testing"

// TestDaemon_ClaimIdempotentOp_ReplayReturnsCachedOutcome pins the cached-outcome
// contract against a real daemon + idempotency store: a succeeding kill (op X) replays
// as a cached success, a failed first attempt (op Y) replays as a cached failure.
func TestDaemon_ClaimIdempotentOp_ReplayReturnsCachedOutcome(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	// op X — a SUCCEEDING kill: claim (fresh), execute the real Kill (signature
	// unchanged), commit success; the replay must surface the cached success.
	m, _ := launchAnnounce(t, d)
	const opX = "devA:01JKILLIDEMPOTENTOK000000X"

	existed, priorOK, err := d.ClaimIdempotentOp(opX, "kill", m.ID)
	if err != nil {
		t.Fatalf("first ClaimIdempotentOp(opX): %v", err)
	}
	if existed {
		t.Fatalf("fresh op X claimed as existed=true; want a first-time claim (existed=false)")
	}
	if err := d.Kill(m.ID); err != nil {
		t.Fatalf("Kill(%s): %v", m.ID, err)
	}
	if err := d.CommitIdempotentOp(opX, true); err != nil {
		t.Fatalf("CommitIdempotentOp(opX, true): %v", err)
	}

	existed, priorOK, err = d.ClaimIdempotentOp(opX, "kill", m.ID)
	if err != nil {
		t.Fatalf("replay ClaimIdempotentOp(opX): %v", err)
	}
	if !existed || !priorOK {
		t.Fatalf("replay of a committed-SUCCESS op = (existed=%v, priorOK=%v); want (true, true) — cached success", existed, priorOK)
	}

	// op Y — a FAILED first attempt: claim (fresh), commit failure; the replay must
	// surface the CACHED FAILURE, never a false success.
	const opY = "devA:01JKILLIDEMPOTENTFAIL000Y"
	existed, _, err = d.ClaimIdempotentOp(opY, "kill", "sessY")
	if err != nil {
		t.Fatalf("first ClaimIdempotentOp(opY): %v", err)
	}
	if existed {
		t.Fatalf("fresh op Y claimed as existed=true; want existed=false")
	}
	if err := d.CommitIdempotentOp(opY, false); err != nil {
		t.Fatalf("CommitIdempotentOp(opY, false): %v", err)
	}

	existed, priorOK, err = d.ClaimIdempotentOp(opY, "kill", "sessY")
	if err != nil {
		t.Fatalf("replay ClaimIdempotentOp(opY): %v", err)
	}
	if !existed {
		t.Fatalf("replay of a failed op Y = existed=false; want existed=true (the record persists)")
	}
	if priorOK {
		t.Fatalf("replay of a committed-FAILURE op = priorOK=true; want false (cached failure, NOT a false success)")
	}
}
