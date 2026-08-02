package relay

// PB-NET-7's TWO CANCELLATION CLAUSES, restored over the live client (ADR-007 B105).
//
// WHAT HAPPENED. hygiene_test.go fenced six PB-NET-7 clauses against transport.Session, which
// had zero production constructions (B94). B98 step 4 deleted it. Four clauses survived that
// deletion because r6-fix-netto was porting them to the live path in the same hours -- the
// non-wait budget and the silent-relay bound into calldeadline_test.go, the goroutine-leak
// assertion into mobile/conformance. TWO DID NOT, and B105 records them as owed:
//
//	TestContextCancellationIsHonoured   the caller's ctx must win over the transport's own bound
//	TestDialHonoursCallerContext        the connect path must not outlive its context
//
// B105 also records WHY the stop condition did not catch it: it guarded rows marked MET, and
// PB-NET-7 was already NOT MET -- which is precisely the row whose fence is the specification
// of its own fix. A deletion is safe for a met row and dangerous for an unmet one, which is the
// opposite of the intuition the guard was built on.
//
// THE SUBJECT IS NOW relay.Client AND relay.DialSecure, which is what the phone actually calls
// (mobile/relay.go), rather than a Session nothing constructs. Both clauses are about the same
// property from two ends: a caller that gives up must be able to, on a request and on a dial.
//
// WHY BOTH HALVES MATTER SEPARATELY. calldeadline_test.go establishes that a call is BOUNDED --
// it ends on its own. That is a different claim from "it ends when the CALLER says so": a
// 10-second bound that ignores cancellation still holds a torn-down App's goroutine for ten
// seconds after Close, and on the dial path it holds one per retry against a dead cell.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPBNET7_TheCallersCancellationWinsOverTheTransportBound. The client's own deadline bounds
// a call the caller cannot end; this is the other direction -- the caller ending a call the
// bound would otherwise hold. A phone that is being closed, backgrounded or torn down cancels
// its context, and every parked exchange must come back with the caller's error rather than
// sit out the remaining budget.
func TestPBNET7_TheCallersCancellationWinsOverTheTransportBound(t *testing.T) {
	machine, devRID, proxy := silentRelayFixture(t)

	// PREMISE: the append works while the relay answers, so a hang afterwards is the silence
	// and not the fixture.
	if _, err := machine.MailboxAppend(context.Background(), devRID, []byte("premise")); err != nil {
		t.Fatalf("MailboxAppend before the relay went silent: %v", err)
	}
	proxy.Silence()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := machine.MailboxAppend(ctx, devRID, []byte("cancel-me"))
		done <- err
	}()

	// Let the call reach the wire and park, then give up on it.
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a cancelled MailboxAppend returned %v, want context.Canceled. The caller's "+
				"cancellation must be the reason the call ends, and its identity is what tells a "+
				"screen 'you stopped this' apart from 'the relay is unreachable'", err)
		}
		// The call must end BECAUSE of the cancel, not because the client's own deadline
		// happened to expire around the same time.
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("the cancelled call took %v to return; that is the transport's own bound "+
				"elapsing, not cancellation being honoured", elapsed)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("a cancelled MailboxAppend never returned: the call ignores its caller's context " +
			"and can only end on the transport's own bound, so a closing App holds the goroutine " +
			"for the remainder of it")
	}
}

// TestPBNET7_DialHonoursItsCallersContext is the same clause on the connect path, which is the
// one a phone hits on a dead cell: mobile/relay.go redials on a loop, and a dial that outlives
// its context accumulates one parked attempt per retry.
//
// The silent proxy is the right adversary here too -- it completes the TCP and websocket
// handshake and then never answers the relay-auth challenge, so the dial is past the socket and
// waiting on a frame that will not come, which is exactly where a bound-but-uncancellable dial
// would sit.
func TestPBNET7_DialHonoursItsCallersContext(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	proxy := newSilentRelay(t, srv.URL())
	proxy.Silence()

	pub, priv := newRelayAuthKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	c, err := DialSecure(ctx, proxy.URL(), authFor(pub, priv), Security{AllowLoopbackCleartext: true})
	elapsed := time.Since(start)

	if err == nil {
		_ = c.Close()
		t.Fatal("DialSecure succeeded against a relay that never answers the auth challenge")
	}
	if elapsed > 3*time.Second {
		t.Errorf("DialSecure took %v to honour a 300ms context. On a dead cell the phone redials "+
			"on a loop (mobile/relay.go), so a dial that outlives its context leaves one parked "+
			"attempt per retry and the teardown that cancels them all waits for every one",
			elapsed)
	}
}
