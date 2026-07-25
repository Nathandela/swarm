package conformance_test

// Slice S16 -- PB-APP-8: "offline, reconnecting, and PER-STREAM stale/resyncing states
// visible; a stale view is NEVER presented as live."
//
// S10 made the per-stream staleness real and durable in the core. This file is about the
// half S10 did not own: whether the SCREEN can see it. Three gaps, all verified in source
// before the assertions were written:
//
//  1. Only ONE read model carries staleness. Snapshot.Stale exists; SessionList, JournalPage
//     and Session do not have it, so a triage inbox rendered from Roster() after a journal
//     gap presents a hole as live -- the exact sentence the requirement forbids, on the
//     screen the user opens first.
//
//  2. "resyncing" is not a state anywhere. App.StreamState answers "live" or "stale" and
//     App.Resync sets nothing, so between asking for a repair and the repair landing the
//     screen has no way to say a repair is in flight. PB-SYNC-3 deliberately does NOT clear
//     the stale mark on the request (clearing it would show the hole as live), which is
//     right -- and it leaves the user pressing a button that changes nothing visible.
//
//  3. THE CLOCK VERDICT NEVER CLEARS AND HAS NO PULL SURFACE. mobile/relay.go reportSkew
//     returns early when the verdict goes back to healthy, so nothing is emitted on that
//     transition, and the golden has no clock verb. A screen opened after the event, or
//     after the user fixes the clock, cannot learn the current verdict. This is the same
//     latch S11's round-1 fix removed from the command path, re-created one layer up in the
//     UI -- and it is inconsistent with UndeliveredInputs(), which the same round added
//     expressly as "the matching pull surface for a screen that opens afterwards".
//     Recorded as a residual owned by S16 in docs/verification/remote-phaseB-progress.md.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// TestPBAPP8_EveryJournalDerivedReadModelCarriesItsStreamsStaleness.
//
// The subject is the READ MODELS, not StreamState. A screen that has to remember to call
// StreamState("journal") beside every Roster() is a screen that will forget once, and the
// failure is silent and looks exactly like a working app -- which is why Snapshot.Stale was
// put ON the snapshot rather than left to the caller. The journal-derived handles need the
// same property for the same reason.
func TestPBAPP8_EveryJournalDerivedReadModelCarriesItsStreamsStaleness(t *testing.T) {
	h := newHarness(t)
	s10GapTheSharedBucket(t, h)

	// Precondition, so nothing below is vacuous: the core knows the journal is holed.
	if state, err := h.App.StreamState(phonecore.StreamJournal); err != nil {
		t.Fatalf("StreamState: %v", err)
	} else if state != "stale" {
		t.Fatalf("precondition: after a shared-bucket gap StreamState(journal) = %q, want stale. "+
			"S10 owns this and it must hold before the read models can be asked about it", state)
	}

	// The TERMINAL model already carries it -- the precedent this requirement generalises.
	snap, err := h.App.Peek(testMachineID + "/sess-gap")
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if !snap.Stale {
		t.Errorf("PB-APP-8: Snapshot.Stale = false for a session whose terminal stream has an " +
			"unrepaired hole. This is the one read model that already carried staleness; if it " +
			"has stopped, the whole requirement has regressed rather than merely being incomplete")
	}

	// The JOURNAL-DERIVED models must carry it too.
	roster, err := h.App.Roster()
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	stale, serr := s16BoolVerb(t, roster, "Stale", "PB-APP-8",
		"Roster() is rendered from the journal stream, so a roster read while that stream has "+
			"an unrepaired hole is a view of a session set that may be missing an exit, a "+
			"needs_input, or a whole session. PB-APP-2's triage inbox is the first screen the "+
			"user opens and the one they act on.")
	if serr != nil {
		t.Fatalf("SessionList.Stale: %v", serr)
	}
	if !stale {
		t.Errorf("PB-APP-8: SessionList.Stale = false while the journal stream is stale. The " +
			"triage inbox is presenting a known hole as live")
	}

	page, err := h.App.ReadJournal(0, 0)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	stale, serr = s16BoolVerb(t, page, "Stale", "PB-APP-8",
		"ReadJournal serves PB-APP-3's session detail AND PB-APP-5's activity log. A page with "+
			"a hole in it reads as a complete history of what the agent did.")
	if serr != nil {
		t.Fatalf("JournalPage.Stale: %v", serr)
	}
	if !stale {
		t.Errorf("PB-APP-8: JournalPage.Stale = false while the journal stream is stale")
	}

	// NON-VACUITY: the flag must be a report of the stream and not a constant. The REPLY
	// stream never gapped, so a model derived from it -- the operation outcome -- must not be
	// claiming staleness, and a build that hard-coded true would fail here.
	if state, err := h.App.StreamState(phonecore.StreamReply); err != nil {
		t.Fatalf("StreamState(reply): %v", err)
	} else if state != "live" {
		t.Errorf("PB-APP-8: the command-reply stream reports %q after a gap in the OTHER bucket. "+
			"A single global stale flag is exactly what PB-SYNC-1 splits apart, and it would "+
			"satisfy every assertion above while telling the user their whole phone is broken",
			state)
	}
}

// TestPBAPP8_AResyncInFlightIsVisibleAndIsNotAThirdValueOfStreamState.
//
// PB-APP-8 enumerates FOUR states and the facade models two. The missing one is not
// cosmetic: PB-SYNC-3 forbids clearing the stale mark when a repair is REQUESTED (clearing
// on the request turns "resync" into "forget" and shows a hole as live), so with no third
// state the user presses Resync and absolutely nothing changes on screen -- and the rate
// bound then refuses their second press with an error, for a button that appeared dead.
//
// IT MUST NOT BE A THIRD VALUE OF StreamState, and this is the trap. Stale and repairing are
// ORTHOGONAL facts -- a stream is stale-and-repairing or stale-and-idle, and collapsing them
// into one enum loses the first. It would also directly contradict S10's shipped
// TestS10_ResyncDoesNotClearStalenessBeforeTheRepairLands, which requires StreamState to
// still read "stale" at exactly this moment; that fence is the one standing between this
// product and PB-SYNC-3's optimistic clear, and no S16 screen state may be bought by
// weakening it. So the repair-in-flight fact needs a surface of its own.
func TestPBAPP8_AResyncInFlightIsVisibleAndIsNotAThirdValueOfStreamState(t *testing.T) {
	h := newHarness(t)
	s10GapTheSharedBucket(t, h)

	pending := s16Lookup(t, h.App, "ResyncPending", "(string) (bool, error)", "PB-APP-8",
		"PB-APP-8's fourth state, as a fact ORTHOGONAL to staleness rather than a third value "+
			"of StreamState: true from the moment a resync is admitted until that stream's own "+
			"repair lands. StreamState must keep reading \"stale\" throughout (PB-SYNC-3, fenced "+
			"by TestS10_ResyncDoesNotClearStalenessBeforeTheRepairLands).")

	if got, err := s16BoolErr(t, pending.Call(s16Args(phonecore.StreamJournal))); err != nil {
		t.Fatalf("ResyncPending: %v", err)
	} else if got {
		t.Fatalf("precondition: ResyncPending(journal) is true before any resync was asked for")
	}

	if err := h.App.Resync(phonecore.StreamJournal); err != nil {
		t.Fatalf("Resync(journal): %v", err)
	}

	if got, err := s16BoolErr(t, pending.Call(s16Args(phonecore.StreamJournal))); err != nil {
		t.Fatalf("ResyncPending: %v", err)
	} else if !got {
		t.Errorf("PB-APP-8: a repair is in flight and ResyncPending(journal) is false. The user " +
			"pressed a button and nothing on the screen changed; the rate bound then refuses " +
			"their second press, for a control that appeared dead")
	}
	// The stale mark is UNTOUCHED, which is the half PB-SYNC-3 protects.
	if s, err := h.App.StreamState(phonecore.StreamJournal); err != nil {
		t.Fatalf("StreamState: %v", err)
	} else if s != "stale" {
		t.Errorf("PB-SYNC-3: StreamState(journal) = %q while the repair is still in flight. The "+
			"in-flight surface was bought by clearing the stale mark on the REQUEST, which shows "+
			"the user a hole as live", s)
	}

	// The repair lands, through the real sealing sink, and only THEN does the flag drop. This
	// is the half that stops "repairing" from being a state the app never leaves.
	//
	// The sink's seq counter is at 2 and the phone's high-water is at 4 (s10GapTheSharedBucket
	// appends its post-gap frame directly), so two throwaway publishes walk the sink past the
	// hole first. Both are stale-dropped by the phone's durable guard, which is the point: they
	// move the SINK, not the phone.
	h.PushEvent(schema.JournalRecord{SessionID: testMachineID + "/sess-gap", Type: "filler"})
	h.PushEvent(schema.JournalRecord{SessionID: testMachineID + "/sess-gap", Type: "filler"})
	if err := h.sink.Reseed(protocol.JournalReseed{
		Roster: []schema.JournalRecord{{SessionID: testMachineID + "/sess-gap", Type: "roster"}},
		Events: []schema.JournalRecord{},
		Cursor: 9,
	}); err != nil {
		t.Fatalf("sink.Reseed: %v", err)
	}
	eventually(t, "the journal never returned to live after its repair landed", func() bool {
		s, err := h.App.StreamState(phonecore.StreamJournal)
		return err == nil && s == "live"
	})
	if got, err := s16BoolErr(t, pending.Call(s16Args(phonecore.StreamJournal))); err != nil {
		t.Fatalf("ResyncPending: %v", err)
	} else if got {
		t.Errorf("PB-APP-8: the repair landed and ResyncPending(journal) is still true. A " +
			"spinner that never stops is the same object as no spinner at all")
	}

	// And the repair repaired ONE channel. A reseed that cleared the terminal too would be
	// PB-SYNC-2's named failure ("a journal reseed cannot repair a missed grid"), and it would
	// leave the peek showing a grid with a hole in it as live.
	if s, err := h.App.StreamState(phonecore.StreamTerminal); err != nil {
		t.Fatalf("StreamState(terminal): %v", err)
	} else if s == "live" {
		t.Errorf("PB-APP-8/PB-SYNC-2: the journal's reseed cleared the TERMINAL stream as well. " +
			"The grid the phone holds still has a missing frame in it and is now presented as live")
	}
}

// TestPBAPP8_TheClockVerdictIsPullableAndClears is the first of the two residuals S16
// inherits, and the requirement it blocks is PB-APP-8's own "a stale view is never presented
// as live": a phone whose clock is out of budget signs an ExpiresAt the daemon refuses, and
// the daemon's refusal reads "not authorized" -- which sends the user to re-pair when the fix
// is to correct their clock.
//
// TWO defects, and they compound. The verdict is push-only, so a screen opened after the
// event never learns it; and nothing is emitted when the verdict goes back to healthy, so a
// UI that latched the pushed event shows a broken clock forever after the user fixes it.
func TestPBAPP8_TheClockVerdictIsPullableAndClears(t *testing.T) {
	h := newHarness(t)
	rec := &s11r3Recorder{}
	if err := h.App.SetEventListener(rec); err != nil {
		t.Fatalf("SetEventListener: %v", err)
	}
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	verdict := s16Lookup(t, h.App, "ClockVerdict", "() (string, error)", "PB-APP-8",
		"The PULL surface for PB-TIME-1's verdict, and the exact shape UndeliveredInputs() "+
			"already has: \"\" when the clock is in budget, the user-legible reason when it is "+
			"not. Without it a screen that opens after the event -- which on Android is most of "+
			"them, because the process is killed and rebuilt constantly -- cannot render a state "+
			"it was never told about.")

	// Healthy to begin with, so the assertion after the skew is a CHANGE and not a constant.
	if got, err := s16StringErr(t, verdict.Call(nil)); err != nil {
		t.Fatalf("ClockVerdict: %v", err)
	} else if got != "" {
		t.Fatalf("precondition: ClockVerdict = %q on a phone whose clock is fine", got)
	}

	const skew = 2 * time.Minute
	s16DriveReply(t, h, rec, skew)

	got, err := s16StringErr(t, verdict.Call(nil))
	if err != nil {
		t.Fatalf("ClockVerdict: %v", err)
	}
	if got == "" {
		t.Errorf("PB-APP-8: the phone measured a %v skew and ClockVerdict is empty. The verdict "+
			"exists only as a fired event, so a screen that was not listening at that instant -- "+
			"or a process Android rebuilt since -- has no way to ask", skew)
	}

	// THE CLEARING TRANSITION. reportSkew's `if !changed || msg == "" { return }` emits nothing
	// here, so a UI that latched the event above is now permanently telling a user with a
	// correct clock to fix their clock.
	before := len(rec.of("clock"))
	s16DriveReply(t, h, rec, 0)

	if got, err := s16StringErr(t, verdict.Call(nil)); err != nil {
		t.Fatalf("ClockVerdict: %v", err)
	} else if got != "" {
		t.Errorf("PB-APP-8: the clock is back in budget and ClockVerdict still reads %q. The "+
			"verdict is latched: the user did exactly what they were told and the app has not "+
			"noticed", got)
	}
	if after := len(rec.of("clock")); after == before {
		t.Errorf("PB-APP-8: the verdict went back to healthy and NO event was raised (%d clock "+
			"events before and after). A screen that rendered the first event has nothing to "+
			"clear it with, so the pull surface above is the only way out -- and a screen that "+
			"is already open will never call it", before)
	}
}

// s16DriveReply issues one command and answers it with a reply whose authenticated machine
// timestamp is offset by skew, then waits for the phone to resolve it.
//
// A reply is the ONLY authenticated machine time the phone ever sees (PB-TIME-1), so a
// bracket can close nowhere else -- which is why the verdict cannot be driven directly.
func s16DriveReply(t *testing.T, h *harness, rec *s11r3Recorder, skew time.Duration) {
	t.Helper()
	op, err := h.App.Kill(testSession)
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	s11r3SkewedReply(t, h, protocol.Control{
		Op: protocol.OpOK, EndpointID: h.Machine, SessionID: testSession, OperationID: op.OperationID,
	}, skew)
	eventually(t, "the phone never resolved the reply, so no clock bracket closed", func() bool {
		return rec.sawOutcome(op.OperationID)
	})
}
