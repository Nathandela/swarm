package relay

// The BOUNDARIES of the call deadline (see calldeadline_test.go for the defect it closes).
//
// A blanket timeout that broke the long poll would be a worse bug than the one being fixed:
// MailboxWait is §6.0's low-latency inbound seam and parks for up to MaxServerWait (25 s) BY
// DESIGN, and rendezvous_recv parks for as long as the human takes to confirm a pairing. So
// the bound is placed on roundtrip -- the short request/reply exchange -- and this file pins
// that placement from both sides: what must be cut, and what must not be.
//
// The deadline is shrunk rather than waited out, so each assertion is decisive in
// milliseconds instead of resolving five real seconds later.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// shortDeadline is a call bound small enough to make its effect (or absence) obvious.
const shortDeadline = 200 * time.Millisecond

// committeeNonWaitTimeout is §6.0's binding value for PB-NET-7's non-wait request timeout.
// It is transcribed from the requirements table rather than read from the constant under
// test, which is the whole point of a budget pin: a test that asserted the code equals
// itself would have accepted every value the code ever held.
const committeeNonWaitTimeout = 10 * time.Second

// TestCallDeadline_TheNonWaitRequestTimeoutIsTheCommitteeBudget pins the VALUE, not just the
// existence of a bound.
//
// §6.0's budget table is prefaced "Changing any value requires committee agreement, not
// implementer discretion", and this constant shipped at 5 s -- a defensible number, chosen
// locally, that left PB-NET-7 unmet because the requirement binds 10 s (ADR-007 B99). The 10 s
// did exist in the tree, as transport.RequestTimeout, pinned by an equivalent test in
// internal/remote/transport -- a package the shipped phone does not use. So the budget was
// enforced exactly where it did not apply and unenforced where it did, and when that package
// was deleted (B98) the pin went with it: this test is now the only one there is.
//
// It is cheap and it is the only thing standing between a budget and the next locally-reasoned
// adjustment.
func TestCallDeadline_TheNonWaitRequestTimeoutIsTheCommitteeBudget(t *testing.T) {
	if DefaultCallTimeout != committeeNonWaitTimeout {
		t.Fatalf("DefaultCallTimeout = %v, want %v (§6.0's non-wait request timeout, bound to "+
			"PB-NET-7).\nThe budget table's preamble reserves this value to the committee. If a "+
			"shorter bound is right -- and there is a real latency argument for one, since a "+
			"keystroke queued behind a command waiting out this deadline pays it first -- the "+
			"change belongs in the table, not here.",
			DefaultCallTimeout, committeeNonWaitTimeout)
	}
}

// TestCallDeadline_TheLongPollIsNotBoundedByIt is the requirement stated as a contrast: with
// the SAME connection and the SAME deadline, an ordinary exchange is cut and the wait is not.
//
// Asserting only that the wait survives would pass on a connection where the deadline does
// nothing at all, so both halves are measured here.
func TestCallDeadline_TheLongPollIsNotBoundedByIt(t *testing.T) {
	machine, devRID, proxy := silentRelayFixture(t)
	proxy.Silence()
	machine.conn.callTimeout = shortDeadline

	// (a) an ordinary exchange IS cut, promptly.
	started := time.Now()
	_, err := machine.MailboxAppend(context.Background(), devRID, []byte("cut me"))
	if took := time.Since(started); took > time.Second {
		t.Fatalf("MailboxAppend took %v under a %v call deadline; the deadline is not being "+
			"applied and the contrast below would be meaningless", took, shortDeadline)
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("MailboxAppend against a silent relay = %v, want ErrTimeout", err)
	}

	// (b) the long poll is NOT. It parks until the CALLER's own deadline, which is the
	// contract: the server bounds a wait at MaxServerWait (25 s) and the caller decides how
	// long it is willing to hold one open.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started = time.Now()
	_, _, werr := machine.MailboxWait(ctx, 0)
	took := time.Since(started)

	if took < time.Second {
		t.Errorf("MailboxWait returned after %v under a %v call deadline. It is a DELIBERATE "+
			"long poll (ADR-007 B7, PB-NET-5): the relay holds it open for up to MaxServerWait "+
			"(%v) so a keystroke is delivered on the reply rather than on the next poll. A call "+
			"deadline that cut it would turn the inbound seam into a timeout loop -- a worse bug "+
			"than the wedge it was meant to fix.", took, shortDeadline, defaultMaxServerWait)
	}
	if !errors.Is(werr, context.DeadlineExceeded) {
		t.Errorf("MailboxWait = %v, want the CALLER's deadline; the wait must end on the "+
			"caller's terms, not on the exchange bound", werr)
	}
}

// TestCallDeadline_ARawConnectionIsNotBounded pins the other exemption.
//
// A raw connection is the PAIRING rendezvous, whose rendezvous_recv blocks until the other
// party sends -- which is a human scanning a QR and comparing a SAS, not a network round
// trip. mobile/pairing.go declares its own 60 s pairingTTL for exactly that reason, and a
// five-second exchange bound underneath it would fail every pairing that took longer than a
// glance. The rendezvous conns are also the adversarial-framing tests' subject, where an
// injected deadline would change what is being measured.
//
// It is asserted on the CONNECTION rather than by waiting one out, because the property is
// "this dial installs no bound" and the alternative is a test that must burn more than
// DefaultCallTimeout of real time to say so.
func TestCallDeadline_ARawConnectionIsNotBounded(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	raw := dialRaw(t, srv.URL())
	if raw.callTimeout != 0 {
		t.Errorf("DialRaw installed a %v call deadline. The rendezvous is a long poll for a "+
			"human-paced ceremony: bounding it fails a pairing that took longer than the bound, "+
			"and the caller already declares the deadline that belongs there (pairingTTL).",
			raw.callTimeout)
	}

	pub, priv := newRelayAuthKey(t)
	authed := dialAuthed(t, srv.URL(), authFor(pub, priv))
	if authed.conn.callTimeout != DefaultCallTimeout {
		t.Errorf("an authenticated dial carries a %v call deadline, want %v; without it a relay "+
			"that answers nothing parks every caller on the connection",
			authed.conn.callTimeout, DefaultCallTimeout)
	}
}

// TestCallDeadline_EveryCallerIsBoundedFromWhenItWasIssued pins WHERE the deadline is taken,
// which is a different property from whether one exists.
//
// c.mu is held across write-then-read. A deadline taken AFTER that lock starts each caller's
// clock only once it reaches the head of the queue, so K callers stacked on a silent relay
// serialise into K deadlines -- and on the phone that queue is the inbound drain plus every
// keystroke, command and take_control. The plane would still stall; it would simply do it one
// deadline at a time. Taken BEFORE the lock, a caller that waited out its deadline in the
// queue fails at the write instead of spending a frame.
func TestCallDeadline_EveryCallerIsBoundedFromWhenItWasIssued(t *testing.T) {
	machine, devRID, proxy := silentRelayFixture(t)
	proxy.Silence()
	machine.conn.callTimeout = shortDeadline

	const callers = 10
	done := make(chan struct{}, callers)
	started := time.Now()
	for i := 0; i < callers; i++ {
		go func() {
			_, _ = machine.MailboxAppend(context.Background(), devRID, []byte("queued"))
			done <- struct{}{}
		}()
	}
	for i := 0; i < callers; i++ {
		<-done
	}

	// One deadline for all of them, plus slack. Serialised, it would be `callers` of them.
	if took := time.Since(started); took > 4*shortDeadline {
		t.Errorf("%d concurrent callers on a silent relay took %v to all return, with a %v call "+
			"deadline. They are being bounded from when each reached the exchange lock rather "+
			"than from when it was issued, so the deadlines stack: %d callers cost %v of wedged "+
			"plane instead of %v.",
			callers, took, shortDeadline, callers, time.Duration(callers)*shortDeadline, shortDeadline)
	}
}

// TestCallDeadline_ATornDownConnectionAlwaysReportsItself covers the OTHER endings of the
// same outage, which is what makes the phone's class deterministic.
//
// The reconnect that follows a timeout closes the connection underneath whatever is still in
// flight, and the caller sees one of two things depending on how far the teardown had got:
// readFrame's ErrConnClosed, or a RAW socket error from the write. The raw one is a foreign
// identity that matched no arm of the phone's classifier, so the SAME outage was reported to
// the user as a transport problem or as an app bug depending on a race -- observed in the
// full suite, where App.Paste failed with "use of closed network connection" and was classed
// "report a bug".
func TestCallDeadline_ATornDownConnectionAlwaysReportsItself(t *testing.T) {
	// (a) TORN DOWN MID-FLIGHT: the read loses its socket.
	t.Run("mid-flight", func(t *testing.T) {
		machine, devRID, proxy := silentRelayFixture(t)
		proxy.Silence()
		go func() {
			time.Sleep(100 * time.Millisecond)
			_ = machine.Close()
		}()
		if _, err := machine.MailboxAppend(context.Background(), devRID, []byte("in flight")); !errors.Is(err, ErrConnClosed) {
			t.Errorf("an exchange whose connection was closed under it = %v, want ErrConnClosed", err)
		}
	})

	// (b) ISSUED AFTER THE TEARDOWN: the WRITE loses its socket, which is the arm that used
	// to surface a raw net error.
	t.Run("after-teardown", func(t *testing.T) {
		machine, devRID, proxy := silentRelayFixture(t)
		proxy.Silence()
		_ = machine.Close()
		_, err := machine.MailboxAppend(context.Background(), devRID, []byte("too late"))
		if !errors.Is(err, ErrConnClosed) {
			t.Errorf("an exchange issued on a closed connection = %v, want ErrConnClosed.\n"+
				"A raw socket error is a foreign identity: nothing routes it, so the phone shows "+
				"the user \"report a bug\" for their own network dropping -- and only sometimes, "+
				"since which arm of the teardown wins is a race.", err)
		}
	})
}

// TestCallDeadline_ATimeoutIsNeverMistakenForARefusal is the seq-safety half.
//
// The relay writes its reply AFTER it stores the item, so a timed-out append MAY have
// committed. remotegw.ClassifyAppend reuses the seq of a DEFINITIVE pre-commit refusal
// (ErrQuotaExceeded / ErrNotAuthorized / ErrRevoked) and must never do that here: reusing a
// seq the relay actually stored puts two different envelopes at one seq, and the phone
// accepts whichever lands first and stale-drops the other -- silent journal loss, which is
// strictly worse than the gap the reuse exists to avoid.
func TestCallDeadline_ATimeoutIsNeverMistakenForARefusal(t *testing.T) {
	machine, devRID, proxy := silentRelayFixture(t)
	proxy.Silence()
	machine.conn.callTimeout = shortDeadline

	_, err := machine.MailboxAppend(context.Background(), devRID, []byte("delivery unknown"))
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("MailboxAppend against a silent relay = %v, want ErrTimeout", err)
	}
	// The underlying cause stays reachable, so a caller already testing for it is unaffected.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ErrTimeout does not wrap context.DeadlineExceeded (%v); callers that already "+
			"test for the deadline would stop matching", err)
	}
	for _, refusal := range []error{ErrQuotaExceeded, ErrNotAuthorized, ErrRevoked} {
		if errors.Is(err, refusal) {
			t.Errorf("a timeout matches the refusal sentinel %v. A refusal is answered BEFORE "+
				"the store and its seq is safe to reuse; a timeout is delivery-UNKNOWN and its "+
				"seq may already be spent.", refusal)
		}
	}
}

// TestCallDeadline_CloseIsIdempotent is the second half of the deleted
// TestCallsAfterCloseFailCleanly (ADR-007 B105). The first half -- every later call is a clean
// typed refusal -- is TestCallDeadline_ATornDownConnectionAlwaysReportsItself above. This is
// the half nothing named.
//
// IT WAS ALREADY EXERCISED, BY ACCIDENT, WHICH IS THE POINT. dialAuthed registers
// t.Cleanup(Close), and the torn-down test above closes explicitly, so a double Close has run
// on every pass of this package for as long as both have existed -- absorbed by a shared
// fixture and asserted by nothing (residual 4.10). Change either seam and the coverage leaves
// with it, silently, because no test names it.
//
// WHAT IDEMPOTENT MEANS FOR A FUNC RETURNING AN ERROR is that the second call reports what the
// first did. Conn.Close guards its body with closeOnce and caches closeErr; markDone has its
// own doneOnce, so dropping closeOnce does NOT panic -- it lets a second Close run ws.Close
// again and overwrite the cached error with the second attempt's. Shutdown would stop being a
// state and start depending on how many times it was asked for.
func TestCallDeadline_CloseIsIdempotent(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	pub, priv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(pub, priv))

	first := machine.Close()
	for i := 2; i <= 3; i++ {
		if again := machine.Close(); !errors.Is(again, first) && again != first {
			t.Errorf("Close() call %d = %v, want the same result as the first call (%v).\n"+
				"Shutdown is a STATE, not an event: a caller that closes a connection someone "+
				"else already closed must be told the same thing, not a fresh error from a "+
				"second teardown attempt.", i, again, first)
		}
	}
}
