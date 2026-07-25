package phonecore

// Slice S11 REVIEW ROUND 4 -- FAILING-FIRST (TDD RED, GG-5) for the severance rule round 3
// narrowed on its LIVE half only.
//
// THE DEFECT. Apply's opDetach branch reads:
//
//	if e.live && e.namesAnotherLease(ctrl) {
//	    return // a notice for a lease that is not the one held
//	}
//	l.severLocked(e, ...)
//
// and severLocked clears `e.op, e.liveOp = "", ""` UNCONDITIONALLY. The guard is `e.live &&
// ...`, so for a NON-LIVE entry namesAnotherLease is never consulted at all: a notice that
// provably names a different take_control -- both ids present and unequal -- still wipes a
// request the phone authored afterwards and is still waiting on.
//
// lease.go's own words are the invariant it breaks. `op` is documented as the take_control
// the phone "has authored and NOT had severed since", and a detach naming some other lease
// did not sever this request's lease.
//
// THE SEQUENCE IS THE NATURAL ONE, not a contrivance: the user re-taps Take Control BECAUSE
// they are typing into a void, and they do it while the death notice for the old lease is
// still in flight. The relay makes that an ordering, not a race.
//
// WHAT THE USER SEES. The recovery grant answers a request the phone no longer remembers,
// falls back to the generation floor, and is refused (the restarted daemon's counter is
// rebuilt empty, so it grants 1 against a floor of 42). Take Control does nothing visible --
// precisely the symptom the daemon-restart work exists to fix, reintroduced through a
// narrower window.
//
// This file contains NO implementation.

import (
	"errors"
	"testing"
	"time"
)

// s11r4FreshOp is the take_control the user authors while the previous lease's death notice
// is still in flight.
const s11r4FreshOp = "op-take-s11r4-fresh"

// TestS11Lease_AForeignDetachDoesNotClobberAFreshlyAuthoredRequest is the defect.
//
// The phone severs on its own transport drop (PB-INPUT-2's first enumerated event, which is
// the only signal a gateway restart can produce), the user re-taps, and only then does the
// notice for the lease that already died arrive.
func TestS11Lease_AForeignDetachDoesNotClobberAFreshlyAuthoredRequest(t *testing.T) {
	now := time.Now()
	exp := now.Add(TakeControlTTL)
	l := s11Leased(t, exp) // live at generation 42, opened by s11TakeOp

	// The link went away, so the phone severs from its own side: no notice can be trusted to
	// arrive, and typing against a lease the machine may not hold is what PB-INPUT-2 forbids.
	l.SeverAll("the connection to the machine dropped")
	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("setup: the lease survived the transport drop: %v", err)
	}

	// The user is typing into a void, so they tap Take Control again. This is the request the
	// recovery grant will answer, and the phone is waiting on it.
	l.Requested(s11Session, s11r4FreshOp, exp)

	// ... and only NOW does the death notice for the OLD lease land. It names s11TakeOp and
	// generation 42: both ids are present and unequal, so it provably belongs to a lease that
	// is not the one just requested.
	l.Apply(s11Severance("daemon connection closed"))

	// The restarted daemon confirms the fresh request. Its counter was rebuilt empty, so the
	// generation is 1 -- at or below the 42 the phone recorded dead, which is why the
	// operation id is the only thing that can admit this grant.
	l.Apply(s11r3Grant(s11r4FreshOp, s11r3RestartGen, &exp))

	if err := l.Require(s11Session, now); err != nil {
		t.Fatalf("Require after the recovery grant for the request the user authored = %v, want nil "+
			"-- a detach naming take_control %q cleared the pending request %q, which it did not "+
			"sever. lease.go calls `op` the take_control the phone has authored and NOT had severed "+
			"since; with it gone the grant falls back to the generation floor (%d <= %d) and is "+
			"refused, so Take Control does nothing visible",
			err, s11TakeOp, s11r4FreshOp, s11r3RestartGen, s11Gen)
	}
	if got, ok := l.Lease(s11Session); !ok || got.Generation != s11r3RestartGen {
		t.Fatalf("Lease() = %+v (ok=%v), want generation %d", got, ok, s11r3RestartGen)
	}
}

// TestS11Lease_AnAttributableDetachStillClearsTheRequest is the control, and it is what keeps
// the narrowing above from being a hole. Clearing the recorded request on severance is what
// makes the operation-id route safe -- a replayed copy of the grant that opened a dead lease
// must land on the generation floor -- so the two cases where the notice IS attributable must
// still clear it: the ids match, or the notice carries none at all (an older gateway, where
// the generation comparison has always been the fallback).
func TestS11Lease_AnAttributableDetachStillClearsTheRequest(t *testing.T) {
	now := time.Now()
	exp := now.Add(TakeControlTTL)

	t.Run("matching id", func(t *testing.T) {
		l := s11Leased(t, exp)
		l.SeverAll("the connection to the machine dropped")
		l.Requested(s11Session, s11r4FreshOp, exp)

		// The machine ended the very lease this request opened.
		l.Apply(s11r3Detach(s11r4FreshOp, s11r3RestartGen, "session exited"))

		// A replay of the grant for that request must NOT reopen it: the phone has authored
		// nothing since, and generation 1 is at or below the 42 it recorded dead.
		l.Apply(s11r3Grant(s11r4FreshOp, s11r3RestartGen, &exp))
		if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
			t.Fatalf("Require after a replayed grant for a request its OWN detach severed = %v, want "+
				"ErrNoLease -- an id-keyed confirmation is only safe while an attributable severance "+
				"still drops the request", err)
		}
	})

	t.Run("no id on the notice", func(t *testing.T) {
		l := s11Leased(t, exp)
		l.SeverAll("the connection to the machine dropped")
		l.Requested(s11Session, s11r4FreshOp, exp)

		// An older gateway seals no operation id. There is no proof the notice belongs to some
		// other lease, so it severs -- which is what namesAnotherLease already says for the live
		// case ("It is a proof, not a guess: absent one, the notice severs").
		noID := s11r3Detach("", s11Gen, "daemon connection closed")
		l.Apply(noID)

		l.Apply(s11r3Grant(s11r4FreshOp, s11r3RestartGen, &exp))
		if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
			t.Fatalf("Require after an unattributed detach and a grant for the pre-notice request = "+
				"%v, want ErrNoLease -- with no id on the notice there is no proof it belongs to "+
				"another lease, so it must clear the request exactly as it always has", err)
		}
	})
}
