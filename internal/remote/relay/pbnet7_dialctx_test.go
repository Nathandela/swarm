package relay

// PB-NET-7: THE DIAL'S DEADLINE MUST NOT BECOME THE CONNECTION'S DEADLINE.
//
// The dial is the one relay call nothing inside this package can bound: Conn.bounded applies
// c.callTimeout, and during the dial there is no Conn to carry one. So the bound has to be the
// caller's -- and a caller that declares one writes the ordinary Go shape:
//
//	dctx, cancel := context.WithTimeout(ctx, relay.DefaultCallTimeout)
//	defer cancel()
//	cl, err := relay.DialSecure(dctx, ...)
//
// That `defer cancel()` fires the instant the dial returns, on a context the returned *Client
// was built under. THE WHOLE FIX RESTS ON THAT BEING HARMLESS, and "harmless" is a property of
// coder/websocket and net/http rather than of anything in this repository: the dial context is
// documented as covering the handshake only, and dialConn gives the Conn its OWN
// context.WithCancel(context.Background()) precisely so the two lifetimes do not touch.
//
// A dependency bump that tied the upgraded connection to its request context would sever every
// client ten seconds after it connected -- silently, on the happy path, in the field, and
// nowhere in any test that dials with an undeadlined context. This is the fence for that, and
// it is the reason the fix was allowed to use `defer cancel()` at all.
//
// The mutation that proves it: derive the Conn's context from the dial's
// (`cctx, cancel := context.WithCancel(ctx)` in dialConn) and this fails on the first call
// after cancel with ErrConnClosed, while the rest of the package stays green.

import (
	"context"
	"testing"
	"time"
)

func TestPBNET7_ADialDeadlineDoesNotOutliveTheHandshake(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	pub, priv := newRelayAuthKey(t)

	// The exact shape a bounded caller writes: a deadline on the dial, cancelled the moment
	// the dial is done. `defer` would fire at the end of THIS function, which proves nothing;
	// cancelling explicitly here puts the cancellation where a caller's defer actually puts
	// it -- before the connection is used.
	dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cl, err := Dial(dctx, srv.URL(), authFor(pub, priv))
	if err != nil {
		t.Fatalf("Dial under a bounded context: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	cancel()

	// A read of the client's own mailbox: no authorization, no fixture, and it fails for
	// exactly one reason if the cancellation propagated.
	if _, err := cl.MailboxRead(testCtx(t), 0); err != nil {
		t.Fatalf("the first call after the DIAL's context was cancelled failed: %v\n"+
			"The dial context must cover the handshake and nothing else. If it now owns the "+
			"connection, every caller that bounds its dial -- mobile/relay.go's App.dial, "+
			"cmd/swarm-remote's run, internal/skeleton's rendezvous factory -- severs its own "+
			"connection at the dial deadline, on the happy path.", err)
	}

	// Anti-vacuity: a Client that was never really connected would also "survive" the
	// cancellation. Bind the pass to a live exchange the relay actually answered.
	if cl.RoutingID() != RoutingID(pub) {
		t.Fatalf("routing id %q: the handshake did not complete, so the assertion above passed "+
			"over a connection that was never live", cl.RoutingID())
	}
	select {
	case <-cl.Done():
		t.Fatal("the connection reports itself DONE after the dial context was cancelled; the " +
			"call above returned from a cached reply rather than a live socket")
	default:
	}
}
