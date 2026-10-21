package daemon

// These integration tests pin kill-operation idempotency against the durable store:
// completed outcomes replay unchanged, and reusing an operation ID with a different
// action or session is rejected in every phase. The implementation is shared with the
// protocol executor; tests also ensure a cached failure can never become a success.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/idempotency"
)

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
	// priorOK was DISCARDED here, which is why the compiler never noticed. A fresh claim has
	// no prior outcome, so the only honest value is false: a true would be a cached verdict
	// leaking onto an op that has not run, and the caller below decides whether to execute on
	// exactly this pair.
	if priorOK {
		t.Fatalf("fresh op X claimed with priorOK=true; a first-time claim has no prior outcome")
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

func TestDaemon_ClaimIdempotentOp_RejectsBindingConflictsInEveryPhase(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	phases := []idempotency.Phase{
		idempotency.PhasePrepared,
		idempotency.PhaseExecuting,
		idempotency.PhaseCompleted,
		idempotency.PhaseFailed,
	}
	conflicts := []struct {
		name    string
		action  string
		session string
	}{
		{name: "action", action: "delete", session: "session-a"},
		{name: "session", action: "kill", session: "session-b"},
	}

	for _, phase := range phases {
		for _, conflict := range conflicts {
			t.Run(string(phase)+"/"+conflict.name, func(t *testing.T) {
				op := "conflict-" + string(phase) + "-" + conflict.name
				if _, existed, err := d.idem.Prepare(op, "kill", "session-a"); err != nil || existed {
					t.Fatalf("Prepare: existed=%v err=%v", existed, err)
				}
				switch phase {
				case idempotency.PhaseExecuting:
					if err := d.idem.Begin(op); err != nil {
						t.Fatal(err)
					}
				case idempotency.PhaseCompleted, idempotency.PhaseFailed:
					if err := d.idem.Begin(op); err != nil {
						t.Fatal(err)
					}
					switch phase {
					case idempotency.PhaseCompleted:
						if err := d.idem.Complete(op, nil); err != nil {
							t.Fatal(err)
						}
					case idempotency.PhaseFailed:
						if err := d.idem.Fail(op, nil); err != nil {
							t.Fatal(err)
						}
					}
				}

				if _, _, err := d.ClaimIdempotentOp(op, conflict.action, conflict.session); err == nil {
					t.Fatalf("ClaimIdempotentOp reused %q bound to kill/session-a as %s/%s", op, conflict.action, conflict.session)
				}
			})
		}
	}
}

func TestDaemon_ClaimIdempotentOp_PreservesExactReplaySemantics(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	for _, tc := range []struct {
		phase       idempotency.Phase
		wantExisted bool
		wantPriorOK bool
	}{
		{phase: idempotency.PhasePrepared},
		{phase: idempotency.PhaseExecuting},
		{phase: idempotency.PhaseCompleted, wantExisted: true, wantPriorOK: true},
		{phase: idempotency.PhaseFailed, wantExisted: true},
	} {
		t.Run(string(tc.phase), func(t *testing.T) {
			op := "exact-" + string(tc.phase)
			if _, existed, err := d.idem.Prepare(op, "kill", "session-a"); err != nil || existed {
				t.Fatalf("Prepare: existed=%v err=%v", existed, err)
			}
			if tc.phase != idempotency.PhasePrepared {
				if err := d.idem.Begin(op); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.phase {
			case idempotency.PhaseCompleted:
				if err := d.idem.Complete(op, nil); err != nil {
					t.Fatal(err)
				}
			case idempotency.PhaseFailed:
				if err := d.idem.Fail(op, nil); err != nil {
					t.Fatal(err)
				}
			}

			gotExisted, gotPriorOK, err := d.ClaimIdempotentOp(op, "kill", "session-a")
			if err != nil {
				t.Fatalf("exact replay: %v", err)
			}
			if gotExisted != tc.wantExisted || gotPriorOK != tc.wantPriorOK {
				t.Fatalf("exact replay = (%v, %v), want (%v, %v)", gotExisted, gotPriorOK, tc.wantExisted, tc.wantPriorOK)
			}
		})
	}
}
