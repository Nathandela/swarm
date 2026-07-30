package relay

// PB-NET-7's "shutdown is a STATE, not a crash" -- the half of the deleted
// TestCallsAfterCloseFailCleanly that had no port (ADR-007 B105 follow-up).
//
// THE CLAUSE HAD TWO HALVES AND ONLY ONE SURVIVED.
//
//	"every later call is a clean typed refusal"  -> COVERED. calldeadline_test.go's
//	    TestCallDeadline_ATornDownConnectionAlwaysReportsItself has both arms, mid-flight
//	    and issued-after-close, each asserting ErrConnClosed. Not repeated here.
//	"Close is idempotent"                        -> SPLIT ACROSS TWO FILES, deliberately.
//	    calldeadline_boundary_test.go's TestCallDeadline_CloseIsIdempotent (2727f0d) owns the
//	    REPEATED-Close case and landed hours before this file. It is not repeated here. What
//	    this file owns is the part that one does not reach: the MIXED teardown order, and a
//	    Close over a socket that already died.
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

// TestPBNET7_CloseIsIdempotent owns EVERY teardown order, repeated and mixed.
//
// IT OWNS THE REPEATED CASE BECAUSE A MUTUAL DE-DUPLICATION DELETED IT. Two agents wrote this
// fence hours apart; each then removed its own repeated-Close case, correctly observing that
// the other file covered it, and the two removals landed back to back. Both reads were true
// when taken. The result was a hole nothing reported -- go test stays green over a property
// nobody asserts, which is why a DUPLICATE is a nuisance and a GAP is a defect (ADR-007 B111).
//
// This file is now the sole owner of Close idempotency in every order. Do not split it again.
func TestPBNET7_CloseIsIdempotent(t *testing.T) {
	// REPEATED CLOSE: the second call must report what the first did. Conn.Close guards its
	// body with closeOnce and caches closeErr; markDone has its own doneOnce, so dropping
	// closeOnce does not panic here -- it lets a second Close re-run ws.Close and overwrite the
	// cached error. Shutdown would stop being a state and start depending on how many times it
	// was asked for.
	t.Run("Close twice", func(t *testing.T) {
		srv, _, _, _ := startTestRelay(t, nil)
		pub, priv := newRelayAuthKey(t)
		c := dialAuthed(t, srv.URL(), authFor(pub, priv))

		first := c.Close()
		if second := c.Close(); first != second {
			t.Errorf("Close() returned %v then %v; the second call must report the SAME outcome "+
				"as the first, because a caller that closes a connection someone else already "+
				"closed has no way to tell which call it is looking at", first, second)
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
