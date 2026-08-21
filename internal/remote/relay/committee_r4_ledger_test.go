// FAILING-FIRST (TDD RED, GG-5) for the audit committee's ROUND-4 finding on the reply
// ledgers (Opus F3, reproduced 5/5 by the committee).
//
// THE DOUBLE LEDGER, in the round-3 code's own terms: an exchange that abandoned its
// reply left its credit in TWO independent books -- `owed` (pump-side: the pump would
// still admit one frame into the queue for it) and `skip` (roundtrip-side: the next
// caller would discard one frame it read). Nothing tied the two spends to the SAME
// frame. With an abandoned exchange's reply still in flight, an idle-time stray spends
// the abandoned `owed` credit (the pump enqueues the stray), the next live exchange's
// `skip` then spends itself on that same stray -- both books drained by one frame -- and
// the abandoned exchange's late reply arrives against the LIVE exchange's own fresh
// credit and is adopted as its answer: wrong data, nil error, exactly the corruption
// H1's drop rule exists to prevent.
//
// The committee's probe, made permanent by the first test below: append #1's reply
// delayed past its caller's deadline; one volunteered stray while the connection is
// idle; then a live append. The live append must return ITS OWN cursor.
//
// THE PRESCRIBED DESIGN (round 4): one ledger. On abandon, the exchange's credit moves
// from `owed` into a pump-side `discard` counter; the pump drops the next `discard`
// frames that arrive DURING a live exchange's window, checking discard before owed --
// in-order reply semantics put every abandoned straggler ahead of the live reply, which
// is what makes that spend exact against an honest relay -- and a frame that arrives
// while NOTHING is owed stays an unsolicited free drop, spending no credit at all.
// roundtrip's `skip` disappears. See client.go's discard field for the full invariant,
// including the one deliberate deviation (the suppressed re-mint) the second test pins.

package relay

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestCommitteeR4_AnIdleStrayCannotRedirectAnAbandonedLateReplyOntoALiveExchange is the
// committee's reproduced probe, permanent. Timeline (generous margins, all >=300 ms, so
// the ordering is deterministic under load):
//
//	t=0        append #1 written; the script sleeps 1.5 s before answering it.
//	t~250ms    the caller's 250 ms deadline abandons the exchange (reply IN FLIGHT).
//	t~400ms    the script volunteers ONE stray MsgError while the connection is idle.
//	t~700ms    a LIVE append #2 is written (its own 10 s deadline).
//	t=1.5s     the abandoned reply (cursor 1) finally arrives; #2's reply follows it.
//
// Under the round-3 double ledger the stray spends the abandoned credit, `skip` spends
// itself on the stray, and #2 adopts cursor 1 with a nil error. Under the round-4 single
// ledger the idle stray is a free unsolicited drop, the late cursor-1 reply spends the
// discard inside #2's window, and #2 gets (2, nil).
func TestCommitteeR4_AnIdleStrayCannotRedirectAnAbandonedLateReplyOntoALiveExchange(t *testing.T) {
	script := newR3Script(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cl := script.dial(t, ctx)

	script.mu.Lock()
	script.delayReply[1] = 1500 * time.Millisecond
	script.mu.Unlock()

	cl.conn.callTimeout = 250 * time.Millisecond
	if _, err := cl.MailboxAppend(ctx, "peer", []byte("late")); !errors.Is(err, ErrTimeout) {
		t.Fatalf("MailboxAppend #1 against a delayed reply = %v, want ErrTimeout", err)
	}
	cl.conn.callTimeout = DefaultCallTimeout

	// One idle-time stray, on the wire while the abandoned reply is still in flight.
	select {
	case script.strayBurst <- 1:
	case <-ctx.Done():
		t.Fatal("the script never took the stray cue")
	}
	select {
	case <-script.burstDone:
	case <-ctx.Done():
		t.Fatal("the script never wrote the stray")
	}
	// Let the stray cross and reach the pump while the connection is provably idle
	// (the abandoned reply is still 1.1 s away).
	time.Sleep(300 * time.Millisecond)

	// The live exchange. Its answer is cursor 2 and nothing else: adopting the
	// abandoned exchange's late cursor-1 reply here is the reproduced corruption.
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("live")); err != nil || cursor != 2 {
		t.Fatalf("live MailboxAppend = (%d, %v), want (2, nil).\n"+
			"The idle stray spent the abandoned exchange's pump-side credit, the roundtrip "+
			"skip spent itself on that same stray, and the abandoned exchange's late reply "+
			"was adopted as the live exchange's answer with a nil error (Opus round-4 F3)", cursor, err)
	}
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("steady")); err != nil || cursor != 3 {
		t.Fatalf("MailboxAppend #3 = (%d, %v), want (3, nil): the stream did not re-synchronise", cursor, err)
	}
	select {
	case <-cl.Done():
		t.Fatal("the connection was torn down over a bounded stray; the ledger must absorb " +
			"it without killing the transport (the standing round-2 fence)")
	default:
	}
}

// TestCommitteeR4_AnIdleArrivingLateReplyNeverWedgesTheConnection pins the deliberate
// deviation from the committee's literal prescription, and the reason for it.
//
// The probe above forces the idle drop to spend NO discard credit (only that leaves the
// credit alive for the late reply that follows the stray). But an HONEST straggler -- an
// abandoned reply that arrives while the connection is idle -- is indistinguishable from
// that stray at the moment it is judged, so it too is free-dropped and its credit
// survives it. Unmitigated, that leaked credit eats the NEXT live exchange's reply, that
// exchange times out and mints a fresh credit, and every exchange on the connection
// times out forever: an unbounded honest-relay wedge bought by one slow reply.
//
// The mitigation is the suppressed re-mint: an exchange that abandons AFTER a discard
// credit was spent inside its own window mints nothing (client.go abandonReply) --
// PROVIDED the pump observed the idle free drop that leaked the credit while it was
// outstanding (the fix-wave condition; without that observation the spent credit paid
// for a predecessor's straggler and the mint is exactly right, see the double-slow
// test below). Here the straggler's own idle arrival IS that observation, so the
// suppression fires: the leaked credit is consumed by at most ONE bounded casualty and
// the connection recovers. This test drives exactly that world: a late reply arriving at idle, then a
// short-deadline exchange (the tolerated casualty), then two ordinary exchanges that
// MUST succeed with their own cursors. Green before the round-4 rewrite (the old skip
// ledger handled this shape); red if the rewrite ships without the suppressed re-mint.
func TestCommitteeR4_AnIdleArrivingLateReplyNeverWedgesTheConnection(t *testing.T) {
	script := newR3Script(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cl := script.dial(t, ctx)

	script.mu.Lock()
	script.delayReply[1] = 600 * time.Millisecond
	script.mu.Unlock()

	cl.conn.callTimeout = 200 * time.Millisecond
	if _, err := cl.MailboxAppend(ctx, "peer", []byte("late")); !errors.Is(err, ErrTimeout) {
		t.Fatalf("MailboxAppend #1 against a delayed reply = %v, want ErrTimeout", err)
	}
	// Let the abandoned reply arrive while the connection is IDLE (no live exchange).
	time.Sleep(900 * time.Millisecond)

	// The tolerated bounded casualty: this exchange may succeed (the old ledger, and any
	// exact attribution) or time out once (the round-4 ledger's leaked credit being
	// consumed). Either outcome is within contract; what is NOT is the wedge below.
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("casualty")); err != nil {
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("MailboxAppend #2 = (%d, %v), want (2, nil) or one bounded ErrTimeout", cursor, err)
		}
	} else if cursor != 2 {
		t.Fatalf("MailboxAppend #2 = (%d, nil), want cursor 2", cursor)
	}
	cl.conn.callTimeout = DefaultCallTimeout

	// The connection must be USABLE again: a design whose abandonment re-mints a credit
	// after the casualty above turns one slow reply into a permanent honest-relay wedge
	// (every later exchange's reply eaten by the credit its predecessor's timeout minted).
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("recovered")); err != nil || cursor != 3 {
		t.Fatalf("MailboxAppend #3 = (%d, %v), want (3, nil): one idle-arriving late reply "+
			"wedged the connection (the leaked discard credit cascaded)", cursor, err)
	}
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("steady")); err != nil || cursor != 4 {
		t.Fatalf("MailboxAppend #4 = (%d, %v), want (4, nil)", cursor, err)
	}
}

// TestCommitteeR4_AnHonestDoubleTimeoutKeepsFIFOAttributionForTheSuccessor pins the
// honest DOUBLE-SLOW corner the round-4 suppressed re-mint got wrong (round-4 fix-wave
// blocker): two back-to-back exchanges both time out against the SAME relay stall, and
// no frame ever arrives at idle.
//
// Timeline (script serial; margins >= 300 ms so ordering is deterministic under load):
//
//	t=0        append #1 written; the script sleeps 600 ms before answering it.
//	t~250ms    #1's 250 ms deadline abandons the exchange (reply IN FLIGHT): discard = 1.
//	t~260ms    append #2 written (700 ms deadline).
//	t=600ms    #1's straggler (cursor 1) arrives INSIDE #2's window: it spends the
//	           discard credit -- the exact FIFO attribution. The script then reads #2
//	           and sleeps 900 ms.
//	t~960ms    #2's deadline abandons it too, with ITS reply genuinely in flight. The
//	           credit it minted must protect the successor: no idle free-drop was ever
//	           observed, so this is provably NOT the idle-leak world the suppressed
//	           re-mint exists for.
//	t~970ms    append #3 written (default deadline).
//	t=1.5s     #2's straggler (cursor 2) arrives inside #3's window; #3's own reply
//	           (cursor 3) follows it.
//
// Under the unconditional suppression, #2's abandonment minted NOTHING (a discard was
// spent inside its window -- on its PREDECESSOR's frame), so cursor 2 is delivered to
// #3 as its answer with a nil error, and the one-back shift persists through every
// back-to-back successor. Under the observed-leak-conditioned suppression, #2 mints
// its credit, cursor 2 is discarded inside #3's window, and #3 gets (3, nil).
func TestCommitteeR4_AnHonestDoubleTimeoutKeepsFIFOAttributionForTheSuccessor(t *testing.T) {
	script := newR3Script(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cl := script.dial(t, ctx)

	script.mu.Lock()
	script.delayReply[1] = 600 * time.Millisecond
	script.delayReply[2] = 900 * time.Millisecond
	script.mu.Unlock()

	cl.conn.callTimeout = 250 * time.Millisecond
	if _, err := cl.MailboxAppend(ctx, "peer", []byte("slow-1")); !errors.Is(err, ErrTimeout) {
		t.Fatalf("MailboxAppend #1 against a delayed reply = %v, want ErrTimeout", err)
	}
	cl.conn.callTimeout = 700 * time.Millisecond
	if _, err := cl.MailboxAppend(ctx, "peer", []byte("slow-2")); !errors.Is(err, ErrTimeout) {
		t.Fatalf("MailboxAppend #2 against the same stall = %v, want ErrTimeout (its "+
			"predecessor's straggler was discarded inside this window, exactly FIFO)", err)
	}
	cl.conn.callTimeout = DefaultCallTimeout

	// The successor. Its answer is cursor 3 and nothing else: adopting #2's late
	// cursor-2 reply here is the double-slow mis-spend.
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("live")); err != nil || cursor != 3 {
		t.Fatalf("MailboxAppend #3 = (%d, %v), want (3, nil).\n"+
			"#2's abandonment suppressed its re-mint because a discard credit was spent "+
			"inside its window -- but that credit was spent on #1's straggler, not on any "+
			"idle leak, so #2's genuinely in-flight reply was left unprotected and adopted "+
			"by its successor with a nil error (honest double-slow corner)", cursor, err)
	}
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("steady")); err != nil || cursor != 4 {
		t.Fatalf("MailboxAppend #4 = (%d, %v), want (4, nil): the stream did not re-synchronise "+
			"(the one-back shift persisted past the first successor)", cursor, err)
	}
	select {
	case <-cl.Done():
		t.Fatal("the connection was torn down over an honest double timeout")
	default:
	}
}

// TestCommitteeR4_ATaintIsConsumedByTheSuppressionItLicensed is round 4's own blocking
// finding (Opus F3-B), reproduced against a fully honest relay -- no stray, no protocol
// violation, only slow replies. The idle-leak observation licenses EXACTLY ONE suppressed
// re-mint; the boolean form of idleLeak survived the suppression that paid for it and
// falsely tainted the NEXT window's spend, so an exchange whose reply was genuinely in
// flight (E4 here) had its re-mint suppressed too, and its straggler was adopted by the
// successor with a nil error.
//
// The script rig processes requests SERIALLY (a delayed reply delays every later reply),
// so the timeline is built on that: reply-N is written at (processing start of N) +
// delayReply[N], and processing of N starts after reply-(N-1) is written.
//
//	   0- 250  E1 (ct 250ms, delay 1300) times out; abandon re-mints: discard=1
//	 300- 550  E2 (ct 250ms, delay 600) times out; abandon re-mints: discard=2
//	    1300   reply-1 arrives IDLE: free drop, THE one leak
//	1700-2000  E3 (ct 300ms, delay 1500): reply-2 (t=1900) spends inside its window
//	           (discard=1, taint transferred); E3's own reply will not arrive until
//	           3400, so E3 abandons -- the ONE suppression the leak licenses. The
//	           leak is now PAID; discard stays 1.
//	3300-3600  E4 (ct 300ms, delay 800): reply-3 (t=3400) spends inside its window
//	           (discard=0). The paid leak must NOT taint this spend: E4's abandonment
//	           must RE-MINT (discard=1), protecting E4's reply (in flight, t=4200).
//	    3700+  E5 (default ct): reply-4 (4200) is consumed by E4's re-minted credit,
//	           and reply-5 answers E5. E6/E7 stay aligned.
func TestCommitteeR4_ATaintIsConsumedByTheSuppressionItLicensed(t *testing.T) {
	script := newR3Script(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cl := script.dial(t, ctx)

	script.mu.Lock()
	script.delayReply[1] = 1300 * time.Millisecond
	script.delayReply[2] = 600 * time.Millisecond
	script.delayReply[3] = 1500 * time.Millisecond
	script.delayReply[4] = 800 * time.Millisecond
	script.mu.Unlock()

	t0 := time.Now()
	at := func(d time.Duration) {
		if s := time.Until(t0.Add(d)); s > 0 {
			time.Sleep(s)
		}
	}

	cl.conn.callTimeout = 250 * time.Millisecond
	if _, err := cl.MailboxAppend(ctx, "peer", []byte("slow-1")); !errors.Is(err, ErrTimeout) {
		t.Fatalf("E1 = %v, want ErrTimeout", err)
	}
	at(300 * time.Millisecond)
	if _, err := cl.MailboxAppend(ctx, "peer", []byte("slow-2")); !errors.Is(err, ErrTimeout) {
		t.Fatalf("E2 = %v, want ErrTimeout", err)
	}

	// The idle gap in which reply-1 free-drops at t=1300 and becomes the one leak.
	at(1700 * time.Millisecond)
	cl.conn.callTimeout = 300 * time.Millisecond
	if _, err := cl.MailboxAppend(ctx, "peer", []byte("slow-3")); !errors.Is(err, ErrTimeout) {
		t.Fatalf("E3 = %v, want ErrTimeout (reply-2 spends inside this window and this "+
			"abandonment is the ONE suppression the idle leak licenses)", err)
	}
	at(3300 * time.Millisecond)
	if _, err := cl.MailboxAppend(ctx, "peer", []byte("slow-4")); !errors.Is(err, ErrTimeout) {
		t.Fatalf("E4 = %v, want ErrTimeout", err)
	}
	cl.conn.callTimeout = DefaultCallTimeout

	at(3700 * time.Millisecond)
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("live")); err != nil || cursor != 5 {
		t.Fatalf("E5 = (%d, %v), want (5, nil).\n"+
			"The idle-leak taint outlived the suppression that paid for it (idleLeak held a "+
			"boolean epoch, not a countable license), so E4's re-mint was suppressed while its "+
			"reply was genuinely in flight, and E5 adopted E4's straggler with a nil error", cursor, err)
	}
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("steady-6")); err != nil || cursor != 6 {
		t.Fatalf("E6 = (%d, %v), want (6, nil): the shift persisted", cursor, err)
	}
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("steady-7")); err != nil || cursor != 7 {
		t.Fatalf("E7 = (%d, %v), want (7, nil): the shift persisted", cursor, err)
	}
	select {
	case <-cl.Done():
		t.Fatal("the connection was torn down over honest slow replies")
	default:
	}
}
