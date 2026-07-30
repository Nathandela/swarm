package relay

// PB-NET-7's "shutdown is a STATE, not a crash" -- the half of the deleted
// TestCallsAfterCloseFailCleanly that had no port (ADR-007 B105 follow-up).
//
// THE CLAUSE HAD TWO HALVES AND ONLY ONE SURVIVED.
//
//	"every later call is a clean typed refusal"  -> COVERED. calldeadline_test.go's
//	    TestCallDeadline_ATornDownConnectionAlwaysReportsItself has both arms, mid-flight
//	    and issued-after-close, each asserting ErrConnClosed. Not repeated here.
//	"Close is idempotent"                        -> nothing asserted it. This file does.
//
// IT IS TRUE BY CONSTRUCTION TODAY, which is exactly why it needs a fence rather than a
// reading. Conn.Close and Conn.CloseNow share one sync.Once (client.go), so the second call is
// a no-op and the doc comment says "It is idempotent" -- an unasserted claim in a comment is
// the shape this audit has spent seven rounds finding. The way it breaks is a maintainer
// replacing the Once with a plain teardown, or adding a `close(ch)` outside it: a second Close
// then panics on a closed channel, and the panic lands in whatever called it.
//
// WHERE IT LANDS MATTERS. On Android, Close is what the lifecycle calls before the process is
// taken away, and swarmmobile.App.Close tears down the relay client underneath it. A double
// teardown -- Stop into an already-closed client, or a Close racing the lifecycle's own -- is
// an ordinary sequence, not an adversarial one, and a panic there crosses JNI as a crash on the
// one path a user cannot retry.

import (
	"testing"
)

// TestPBNET7_CloseIsIdempotent pins the property for both teardown verbs and for the mixed
// order, since they share one sync.Once and a change to either can break the other.
func TestPBNET7_CloseIsIdempotent(t *testing.T) {
	t.Run("Close twice", func(t *testing.T) {
		srv, _, _, _ := startTestRelay(t, nil)
		pub, priv := newRelayAuthKey(t)
		c := dialAuthed(t, srv.URL(), authFor(pub, priv))

		first := c.Close()
		second := c.Close() // must not panic on a closed channel or a re-closed socket
		if first != second {
			t.Errorf("Close() returned %v then %v; the second call must report the SAME outcome as "+
				"the first, because a caller that closes twice has no way to tell which one it is "+
				"looking at", first, second)
		}
	})

	t.Run("CloseNow after Close", func(t *testing.T) {
		srv, _, _, _ := startTestRelay(t, nil)
		pub, priv := newRelayAuthKey(t)
		c := dialAuthed(t, srv.URL(), authFor(pub, priv))

		_ = c.Close()
		// The abort path after a graceful close: pairing.go:573 reaches CloseNow on its abort
		// leg, and the lifecycle can reach Close first.
		_ = c.conn.CloseNow()
	})

	t.Run("Close after CloseNow", func(t *testing.T) {
		srv, _, _, _ := startTestRelay(t, nil)
		pub, priv := newRelayAuthKey(t)
		c := dialAuthed(t, srv.URL(), authFor(pub, priv))

		_ = c.conn.CloseNow()
		_ = c.Close()
	})
}

// TestPBNET7_CloseIsIdempotentAfterTheSocketAlreadyDied is the case a handset actually hits:
// the relay goes away first, the pump observes the death, and only then does the lifecycle
// call Close. The teardown must still be a state transition rather than a second failure.
func TestPBNET7_CloseIsIdempotentAfterTheSocketAlreadyDied(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	pub, priv := newRelayAuthKey(t)
	c := dialAuthed(t, srv.URL(), authFor(pub, priv))

	// Kill the socket underneath the client, then tear down twice on top of the corpse.
	_ = c.conn.CloseNow()
	<-c.conn.Done() // the pump has observed the death

	first := c.Close()
	second := c.Close()
	if first != second {
		t.Errorf("after the socket died, Close() returned %v then %v; a teardown over an already "+
			"dead connection must still be idempotent -- this is the ordinary handset sequence "+
			"(the cell drops, then the lifecycle closes), not an adversarial one", first, second)
	}
}
