package phonecore

// Slice S11 REVIEW ROUND 3 -- FAILING-FIRST (TDD RED, GG-5) for PB-INPUT-2's daemon-restart
// event, which the round-2 suite covered on its SEVERANCE half only.
//
// THE DEFECT. The daemon's lease generation counter is in-memory and per-Server-lifetime:
// internal/protocol/server.go:205 holds `leases map[string]*sessionLease` with no durable
// backing, and :220-224's own comment says "genCounter is monotonic for the Server's
// lifetime and never resets" -- for the SERVER's lifetime, which is exactly the scope that
// ends at a restart. `ls.genCounter++` (:529) therefore yields 1 for the first grant after
// any daemon restart, while the phone has recorded the severance at generation N and refuses
// everything at or below it (lease.go's `ctrl.Generation <= e.dead`).
//
// Both arrival orders break, by different routes, and neither was reachable from the
// round-2 suite:
//
//   - FORWARD (severance first): the recovery confirmation carries generation 1 and is
//     silently discarded. The keyboard is dead and Take Control does nothing visible. It
//     fires at N=1 too (1 <= 1), so the FIRST daemon restart of any session hits it.
//   - REVERSE (grant first): the post-restart grant at generation 1 is accepted, and the
//     late notice for the pre-restart generation N then fails the
//     `ctrl.Generation < e.gen` supersede test (N < 1 is false) and severs the lease the
//     daemon has just granted.
//
// WHY THE EXISTING SUITE COULD NOT SEE IT. s11_lease_test.go's "daemon restart" subtest
// asserts only that the lease is severed. The single recovery control,
// TestS11Lease_ASeveredLeaseIsNotResurrectedByAStaleConfirmation, recovers at s11Gen+1 -- a
// HIGHER generation, which is the one thing a restart never produces. The conformance
// harness makes it unreachable by construction: drainOnce does `h.leaseGen++`, monotonic
// forever.
//
// THE FIX THIS PINS. A lease is identified by the take_control OPERATION ID that opened it,
// which the daemon restart does not touch and which the gateway already carries on both the
// grant (lease_confirm.go) and the severance notice (lease_sever.go, from the lease conn's
// own cmd.OperationID). The generation stays the tiebreak WITHIN one daemon lifetime.
//
// THE TRAP, pinned below as its own test: an operation-id bypass is only safe if the
// recorded request is CLEARED by a severance. Otherwise a late duplicate of the very
// confirmation that opened the lease would carry a matching id and resurrect it -- the exact
// hazard TestS11Lease_ASeveredLeaseIsNotResurrectedByAStaleConfirmation exists to prevent.

import (
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// s11r3RestartOp is the take_control the user authors AFTER the daemon restarted.
const s11r3RestartOp = "op-take-s11r3-after-restart"

// s11r3RestartGen is what the restarted daemon grants: its counter rebuilt empty, so the
// first post-restart lease is generation 1 no matter how high the pre-restart one climbed.
const s11r3RestartGen uint64 = 1

// s11r3Grant is the gateway's lease confirmation for a post-restart take_control.
func s11r3Grant(op string, gen uint64, expiresAt *time.Time) schema.Control {
	return schema.Control{
		Op:          protocol.OpLease,
		SessionID:   s11Session,
		OperationID: op,
		Generation:  gen,
		ExpiresAt:   expiresAt,
	}
}

// s11r3Detach is a lease-death notice for a NAMED take_control, exactly as
// remotegw.CommandBridge.sealSevered builds it from the lease conn's own operation id.
func s11r3Detach(op string, gen uint64, reason string) schema.Control {
	return schema.Control{
		Op:          protocol.OpDetach,
		SessionID:   s11Session,
		OperationID: op,
		Generation:  gen,
		Error:       reason,
	}
}

// TestS11Lease_ADaemonRestartDoesNotBrickTheKeyboard is the FORWARD arrival order: the
// severance for the pre-restart generation lands first, then the user presses Take Control
// and the restarted daemon grants generation 1.
//
// Against the shipped code the recovery confirmation is discarded by the generation floor
// and Require keeps reporting "daemon connection closed" -- a dead keyboard and a button
// that does nothing, on the FIRST daemon restart of any session.
func TestS11Lease_ADaemonRestartDoesNotBrickTheKeyboard(t *testing.T) {
	now := time.Now()
	exp := now.Add(TakeControlTTL)
	l := s11Leased(t, exp) // the pre-restart lease, generation 42

	// The daemon died: its lease conn's readLoop errors, the watcher evicts it, and the
	// gateway seals the notice for the generation that just died.
	l.Apply(s11Severance("daemon connection closed"))
	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("setup: the lease survived the daemon's death: %v", err)
	}

	// The user presses Take Control. The restarted daemon's counter is rebuilt empty.
	l.Requested(s11Session, s11r3RestartOp, exp)
	l.Apply(s11r3Grant(s11r3RestartOp, s11r3RestartGen, &exp))

	if err := l.Require(s11Session, now); err != nil {
		t.Fatalf("Require after a restarted daemon granted generation %d (the phone had recorded "+
			"generation %d as dead) = %v, want nil -- the daemon's counter is in-memory and per-Server "+
			"lifetime (server.go:220-224), so a restart ALWAYS grants a generation at or below the one "+
			"the phone last severed. Gating recovery on a higher number makes the first daemon restart "+
			"of any session a dead keyboard with a Take Control button that does nothing visible",
			s11r3RestartGen, s11Gen, err)
	}
	if got, ok := l.Lease(s11Session); !ok || got.Generation != s11r3RestartGen {
		t.Fatalf("Lease() = %+v (ok=%v) after the post-restart grant, want generation %d -- the phone "+
			"must track the generation the machine actually granted", got, ok, s11r3RestartGen)
	}
	if r := l.Reason(s11Session); r != "" {
		t.Errorf("Reason() = %q while a lease is live; the stale severance reason is still on the "+
			"screen telling the user the daemon is gone", r)
	}
}

// TestS11Lease_APreRestartSeveranceDoesNotKillThePostRestartLease is the REVERSE arrival
// order, and it breaks by a different route: the post-restart grant at generation 1 is
// ACCEPTED, and the late notice for the pre-restart generation 42 then fails the supersede
// test (`ctrl.Generation < e.gen` is 42 < 1, false) and severs the lease the daemon is
// holding open.
//
// The relay makes this an ordering, not a race: a notice sealed before the grant can arrive
// after it.
func TestS11Lease_APreRestartSeveranceDoesNotKillThePostRestartLease(t *testing.T) {
	now := time.Now()
	exp := now.Add(TakeControlTTL)
	l := s11Leased(t, exp) // the pre-restart lease, generation 42

	// The user re-takes control and the restarted daemon's grant overtakes the notice.
	l.Requested(s11Session, s11r3RestartOp, exp)
	l.Apply(s11r3Grant(s11r3RestartOp, s11r3RestartGen, &exp))
	if err := l.Require(s11Session, now); err != nil {
		t.Fatalf("setup: the post-restart grant was not adopted: %v", err)
	}

	// ... and only now does the pre-restart lease's death notice arrive.
	l.Apply(s11Severance("daemon connection closed")) // generation 42, operation s11TakeOp

	if err := l.Require(s11Session, now); err != nil {
		t.Fatalf("Require after a LATE severance for the pre-restart generation %d = %v, want nil -- "+
			"the live lease is generation %d, granted by the restarted daemon under a different "+
			"take_control. The generation comparison cannot tell the two apart across a restart, so "+
			"keying the notice on the number alone kills the lease the daemon just granted",
			s11Gen, err, s11r3RestartGen)
	}

	// MUTATION CONTROL: the notice for the LIVE lease must still shut the gate, or the fix
	// has simply disabled the severance path.
	l.Apply(s11r3Detach(s11r3RestartOp, s11r3RestartGen, "session exited"))
	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Require after the severance for the LIVE lease = %v, want ErrNoLease -- the notice "+
			"must discriminate between leases, not be suppressed", err)
	}
}

// TestS11Lease_ALateDuplicateOfTheRecoveryGrantCannotResurrectTheLease is the trap the
// operation-id route opens if the recorded request is not cleared by a severance.
//
// The generation floor stops a REPLAYED confirmation today
// (TestS11Lease_ASeveredLeaseIsNotResurrectedByAStaleConfirmation). Any bypass keyed on the
// operation id must not reopen that hole: the duplicate carries the SAME id as the grant
// that opened the lease, so a bypass that only compares ids would readmit it. Only a
// FRESHLY AUTHORED take_control may be confirmable.
func TestS11Lease_ALateDuplicateOfTheRecoveryGrantCannotResurrectTheLease(t *testing.T) {
	now := time.Now()
	exp := now.Add(TakeControlTTL)
	l := s11Leased(t, exp)

	// Recover across a restart, exactly as the forward-order test does.
	l.Apply(s11Severance("daemon connection closed"))
	l.Requested(s11Session, s11r3RestartOp, exp)
	l.Apply(s11r3Grant(s11r3RestartOp, s11r3RestartGen, &exp))
	if err := l.Require(s11Session, now); err != nil {
		t.Fatalf("setup: the post-restart lease is not usable: %v", err)
	}

	// The recovered lease dies in its turn.
	l.Apply(s11r3Detach(s11r3RestartOp, s11r3RestartGen, "session exited"))
	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("setup: the recovered lease survived its own severance: %v", err)
	}

	// The relay re-delivers the grant that opened it. The phone has authored nothing since.
	l.Apply(s11r3Grant(s11r3RestartOp, s11r3RestartGen, &exp))
	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Require after a REPLAYED copy of the grant that opened the now-dead lease = %v, "+
			"want ErrNoLease -- the id matches the take_control the phone last authored, so an "+
			"id-keyed bypass that does not clear the request on severance readmits a lease the "+
			"daemon released", err)
	}

	// ... and with no new take_control authored, nothing at all may reopen it.
	l.Apply(s11r3Grant("op-take-s11r3-never-authored", s11r3RestartGen+5, &exp))
	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Require after a grant for a take_control the phone never authored = %v, want "+
			"ErrNoLease -- the phone gates on ITS OWN request, and a confirmation answering nothing "+
			"it sent is not evidence of a lease it holds", err)
	}

	// MUTATION CONTROL / anti-brick (PB-STATE-10): pressing Take Control again must work,
	// even though the restarted daemon keeps handing out low generations.
	const againOp = "op-take-s11r3-again"
	l.Requested(s11Session, againOp, exp)
	l.Apply(s11r3Grant(againOp, s11r3RestartGen+1, &exp))
	if err := l.Require(s11Session, now); err != nil {
		t.Fatalf("Require after a genuinely NEW take_control was confirmed = %v, want nil -- a "+
			"severance that cannot be recovered from is the permanent brick PB-STATE-10 forbids", err)
	}
}
