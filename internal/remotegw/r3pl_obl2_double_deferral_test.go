package remotegw

// R3 GREEN review round 1 -- FAILING-FIRST (TDD RED, GG-5) regression tests for the two
// findings the reviewer raised against the deferred-wake provisional pre-append
// (bd agents-tracker-hggx.4.4).
//
// BLOCKING (PG-OBL-7 / PB-PUSH-8): TWO deferred interactions inside ONE 30 s window land
// on the SAME provisional record -- the second pre-append COALESCES (PG-OBL-5), so
// Coalesced becomes 1 -- and Supersede's `ob.Coalesced > 0` guard then REFUSES to cancel
// on a preference flip. The provisional record stays pending, a later drive submits the
// wake the user turned off (PB-PUSH-8 wants ZERO calls for a disabled category, verified
// at the sender), and if nothing drives it the record expires and Service.Err() reports a
// PG-OBL-10 degraded pairing that is really just the user's own toggle. The guard is
// correct in general -- a coalesce this deferral did NOT make is another session's
// hand-off, authorized by a preference this re-read never consulted -- but every coalesce
// HERE is one of this cycle's own deferred interactions, i.e. needs_input, exactly the
// category the at-send re-read found disabled, and the process knows it in memory.
//
// This is NOT the accepted deviation recorded in docs/verification/r3-red/
// obligations-red.txt: that one is the post-crash, category-less redrive, where the
// machine genuinely cannot know which preference authorized which coalesced trigger.
//
// MINOR (PG-OBL-10): a superseded record silently erases a REAL degraded signal. A
// pairing driven to `abandoned` with a permanent code, then re-triggered and superseded,
// reports healthy -- the supersede is not a delivery and must not clear a degraded end it
// merely wrote over.
//
// Both tests exercise ONLY symbols that already exist and fail at ASSERTION level.
// This file contains NO implementation.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestOBL2_TwoDeferralsInOneWindowStillSupersedeOnAPreferenceFlip is the BLOCKING
// regression. The harness's own setup defers interaction #2; a THIRD interaction for the
// SAME session five seconds later is deferred into the SAME armed cycle and its
// pre-append coalesces into the SAME provisional record. The user then flips needs_input
// off before the timer fires, and the two properties PB-PUSH-8 and PG-OBL-10 each own
// must both hold: NO wake may leave the machine, ever, and the pairing must not be
// reported as a degraded push path for having honoured a preference.
func TestOBL2_TwoDeferralsInOneWindowStillSupersedeOnAPreferenceFlip(t *testing.T) {
	h := newDeferredGapHarness(t)
	h.deferOneInteractionWake(t)

	// The SECOND deferral of the same cycle: still inside the 30 s window interaction #1
	// claimed, so it is deferred onto the already-armed timer and its pre-append
	// coalesces into the provisional record interaction #2 minted.
	h.clk.advance(5 * time.Second)
	h.interaction(t, 3, "m/s1")
	if got := len(h.ft.scheduled()); got != 1 {
		t.Fatalf("setup: %d deferral timers armed after the second deferred interaction, want still exactly 1 "+
			"-- one timer serves every session pending at that moment (ADR-010 section 4(b))", got)
	}
	if got := len(h.sub.all()); got != 1 {
		t.Fatalf("setup: the second deferred interaction produced a submit (total %d), want it suppressed and deferred", got)
	}
	prov, ok, err := h.store.Get(h.addr)
	if err != nil || !ok {
		t.Fatalf("setup: store.Get: ok=%v err=%v", ok, err)
	}
	if !prov.nonTerminal() {
		t.Fatalf("setup: the provisional obligation is %q, want a live record carrying both deferrals", prov.State)
	}
	if prov.Coalesced != 1 {
		t.Fatalf("setup: provisional obligation has coalesced=%d, want 1 -- this test only means anything if the "+
			"second deferral's pre-append landed on the SAME record (PG-OBL-5)", prov.Coalesced)
	}

	// The user flips needs_input OFF while both deferrals are still pending.
	if err := h.prefs.SavePrefs(PushPrefs{Version: 1, NeedsInput: false, Finished: true}); err != nil {
		t.Fatalf("SavePrefs: %v", err)
	}
	h.clk.advance(DefaultPushWindow - 10*time.Second)
	h.ft.fire(t, 0)

	if got := len(h.sub.all()); got != 1 {
		t.Fatalf("gateway submits after the preference flipped off = %d, want 1 (only the pre-flip immediate wake): "+
			"PB-PUSH-8 means ZERO calls for a disabled category, verified at the sender", got)
	}
	// The record as the timer left it, snapshotted BEFORE anything else touches it.
	cur, ok, err := h.store.Get(h.addr)
	if err != nil || !ok {
		t.Fatalf("store.Get after the timer fired: ok=%v err=%v", ok, err)
	}

	// A LATER DRIVE -- a redial, a restart redrive, or PG-OBL-9's retry timer -- must not
	// resurrect the send. This is the half that actually puts a forbidden push on the wire.
	later := NewWakeObligationMachine(WakeObligationConfig{
		Store: h.store, Submitter: h.sub, WakeKey: testWakeKey(), Address: h.addr,
		Seq: mustSeqSource(t), Now: h.clk.Now,
	})
	if err := later.Drive(context.Background()); err != nil {
		t.Fatalf("later Drive: %v", err)
	}
	if got := len(h.sub.all()); got != 1 {
		t.Fatalf("a later drive submitted the wake the user turned off (%d submits, want 1): the provisional record "+
			"was left %q with coalesced=%d, so PB-PUSH-8's suppression did not hold", got, cur.State, cur.Coalesced)
	}

	if cur.WakeSeq != prov.WakeSeq {
		t.Fatalf("record for the address is wake_seq %d, want the provisional %d cancelled IN PLACE", cur.WakeSeq, prov.WakeSeq)
	}
	if cur.nonTerminal() {
		t.Fatalf("the provisional obligation is still %q (coalesced=%d) after the send was cancelled: every trigger "+
			"riding this record is one of THIS deferral cycle's own needs_input interactions -- exactly the category "+
			"the at-send re-read found disabled -- so the cancel must land, not be refused as a foreign coalesce",
			cur.State, cur.Coalesced)
	}
	if cur.State == ObligationDelivered || cur.LastOutcome == "provider_accepted" {
		t.Fatalf("cancelled obligation claims a send that never happened (state=%q outcome=%q)", cur.State, cur.LastOutcome)
	}
	if cur.LastOutcome == "" {
		t.Fatalf("cancelled obligation carries no last-outcome word at all: the record must SAY why it ended (PG-OBL-1)")
	}

	// And the OTHER arm of the same defect: with nothing driving it the record runs out
	// its five minutes and the operator's one health surface reports a push outage that
	// is really the user's own toggle.
	h.clk.advance(WakeV1Expiry + time.Second)
	if err := later.Drive(context.Background()); err != nil {
		t.Fatalf("post-expiry Drive: %v", err)
	}
	if got := len(h.sub.all()); got != 1 {
		t.Fatalf("a post-expiry drive submitted %d wakes, want 1", got)
	}
	if err := newObligationHealthService(t, h.addr, h.store).Err(); err != nil {
		t.Fatalf("Service.Err() = %v after a preference flip cancelled a deferred wake: a user turning a category "+
			"off is not a degraded push path (PG-OBL-10 reports outages, not preferences)", err)
	}
}

// mustSeqSource returns a fresh in-memory durable wake_seq source.
func mustSeqSource(t *testing.T) SeqSource {
	t.Helper()
	s, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	return s
}

// TestOBL10_ASupersededObligationDoesNotEraseADegradedPairing is the MINOR regression.
// A pairing whose last obligation was ABANDONED with a permanent code (address_revoked:
// the phone must re-pair) is degraded, and PG-OBL-10 requires that be visible. A fresh
// trigger carries that degraded end onto its replacement -- and then a supersede (the
// user turned push off, WakeRetryScheduler.driveOnce's SupersedeAll, or a deferral's own
// cancellation) marks that replacement terminal. A supersede is NOT a delivery: nothing
// reached the gateway, so nothing proved the pairing repaired, and the degraded state
// must survive it. Today Service.Err() returns nil the moment the record reads
// `superseded`, so a revoked pairing reports healthy until the next real wake attempt
// re-derives the failure.
func TestOBL10_ASupersededObligationDoesNotEraseADegradedPairing(t *testing.T) {
	addr := testPushAddress(0x62)
	store := driveObligationTo(t, addr, ObligationAbandoned)

	m := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: &fakeSubmitter{}, WakeKey: testWakeKey(), Address: addr,
		Seq: mustSeqSource(t), Now: newTestClock().Now,
	})
	if err := m.Trigger(); err != nil {
		t.Fatalf("Trigger over the abandoned record: %v", err)
	}
	if err := m.SupersedeAll(wakeOutcomePreferenceSuppressed); err != nil {
		t.Fatalf("SupersedeAll: %v", err)
	}
	cur, ok, err := store.Get(addr)
	if err != nil || !ok || cur.State != ObligationSuperseded {
		t.Fatalf("setup: record after the supersede: ok=%v state=%q err=%v, want %q", ok, cur.State, err, ObligationSuperseded)
	}

	err = newObligationHealthService(t, addr, store).Err()
	if err == nil {
		t.Fatalf("Service.Err() = nil for a pairing whose push address was REVOKED and whose replacement obligation " +
			"was merely superseded: a supersede sends nothing, so it cannot prove the pairing repaired -- PG-OBL-10's " +
			"degraded state must survive it")
	}
	if !strings.Contains(err.Error(), "address_revoked") {
		t.Fatalf("Service.Err() = %q: the degraded state must still carry the outcome code that names the repair "+
			"path (%q)", err, "address_revoked")
	}
}
