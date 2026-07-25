// Slice S6b review remediation, BLOCKING-1: the parked wait must remember the
// routing id it was REGISTERED under, and must never read sc.rid off the
// connection's own goroutine.
//
// Neither handleAuthInit nor handleAuthResp checks sc.authed, so re-running the
// signed-challenge handshake on an ALREADY authenticated socket under a fresh key
// is a legal frame sequence. registerSession then writes sc.rid under s.mu on the
// request loop while a parked wait is reading it from its own goroutine, which is
// two defects at once:
//
//   - a DATA RACE on a string header (a torn two-word read yields one string's
//     pointer with another's length), reachable by any authenticated client;
//   - a PERMANENT wait-slot leak, because s.waits is keyed by routing id while
//     releaseWaitLocked/severWaitLocked/removeConn all look the entry up under the
//     CURRENT sc.rid. After a re-auth the entry is orphaned under the old key, and
//     unlike the analogous stale s.sessions entry it does not self-heal:
//     registerWait refuses while the key is busy and severWaitLocked requires
//     w.sc == sc, which no future connection satisfies. That routing id can never
//     park a wait again -- live typing permanently dead for that identity -- and
//     the state is minted with self-generated keys, which server.go's H3 comment
//     says must not be possible.
//
// These tests are FAILING-FIRST against the first S6b implementation. They do NOT
// assert anything about whether re-auth on an established connection should be
// refused outright: that is a Phase A question (the s.sessions half of it is
// pre-existing behaviour) and is filed separately. They assert only that the wait
// machinery is correct however sc.rid moves.
package relay

import (
	"encoding/json"
	"testing"
	"time"
)

// reauthSameConn completes a SECOND signed-challenge handshake on an already
// authenticated connection under a fresh key, and returns the new routing id. It
// goes through the client's own control path, so it is exactly the frame sequence
// a client can produce today.
func reauthSameConn(t *testing.T, c *Client, auth ClientAuth) string {
	t.Helper()
	resp, err := c.conn.control(testCtx(t), "auth_init", map[string]any{"relay_auth_pub": []byte(auth.RelayAuthPub)})
	if err != nil {
		t.Fatalf("re-auth auth_init: %v", err)
	}
	var chal struct {
		Nonce []byte `json:"nonce"`
	}
	if err := json.Unmarshal(resp, &chal); err != nil {
		t.Fatalf("re-auth nonce: %v", err)
	}
	rid := RoutingID(auth.RelayAuthPub)
	sig, err := auth.Sign(AuthChallengeMessage(chal.Nonce, rid))
	if err != nil {
		t.Fatalf("re-auth sign: %v", err)
	}
	if _, err := c.conn.control(testCtx(t), "auth_resp", map[string]any{"signature": sig}); err != nil {
		t.Fatalf("re-auth auth_resp: %v", err)
	}
	return rid
}

// waitKeys snapshots the server's live wait registrations.
func waitKeys(s *Server) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.waits))
	for rid := range s.waits {
		out = append(out, rid)
	}
	return out
}

// TestParkedWaitServesTheMailboxItRegisteredFor is the unsynchronised-read half,
// asserted STRUCTURALLY rather than through the race detector.
//
// serveWait reads sc.rid from its own goroutine to decide which mailbox to serve,
// while registerSession writes it on the request loop. The observable consequence
// is sharper than the race: after a re-auth, a wait registered for A starts reading
// B's mailbox, so an item appended to A never reaches the client that is waiting
// for it. That is deterministic, and it is what this asserts.
//
// It is asserted this way ON PURPOSE. Under -race this exact sequence reports
// nothing, because readItemsPage and the isRevoked lookup inside the re-auth both
// enter bolt, whose internal metalock incidentally orders the two accesses. That
// ordering is an accident of a third-party lock, not a property this package
// establishes, so a detector-based test here would pass for the wrong reason and
// would stop failing the moment bolt changed. The wait must carry its own copy of
// the routing id, and this fails until it does.
func TestParkedWaitServesTheMailboxItRegisteredFor(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	p := s6bFixture(t, srv)

	waited, cancelWait := s6bParkWait(t, p.device, 0, 200*time.Millisecond)
	defer cancelWait()

	// The SAME socket re-authenticates under a fresh key. The wait parked for the
	// old routing id is untouched by that; it must keep serving the mailbox it
	// registered for.
	nPub, nPriv := newRelayAuthKey(t)
	newRID := reauthSameConn(t, p.device, authFor(nPub, nPriv))
	if newRID == p.devRID {
		t.Fatal("the re-auth produced the same routing id; the fixture is not exercising the defect")
	}

	env := p.sp.sealMailbox(t, 8_001, []byte("for-the-original-rid"), clk)
	if _, err := p.machine.MailboxAppend(testCtx(t), p.devRID, env); err != nil {
		t.Fatalf("append to the waiting routing id: %v", err)
	}

	select {
	case r := <-waited:
		if r.err != nil {
			t.Fatalf("the parked wait resolved with %v, want the appended item", r.err)
		}
		if len(r.items) != 1 || string(r.items[0].Envelope) != string(env) {
			t.Fatalf("the parked wait returned %d items, want the 1 appended to %s", len(r.items), p.devRID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("an item appended to %s never reached the wait parked for it: after the connection re-authenticated as %s the wait began serving the NEW routing id's mailbox, because it reads sc.rid instead of the routing id it registered under",
			p.devRID, newRID)
	}
}

// TestReauthenticatingDoesNotOrphanTheWaitSlot is the leak half. A wait registered
// under routing id A must be released from A's slot when the connection dies, even
// if the connection has since re-authenticated as B. Otherwise A can never park a
// wait again, and an attacker mints one permanent orphan per self-generated key.
func TestReauthenticatingDoesNotOrphanTheWaitSlot(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	p := s6bFixture(t, srv)

	_, cancelWait := s6bParkWait(t, p.device, 0, 200*time.Millisecond)
	defer cancelWait()

	if got := waitKeys(srv); len(got) != 1 || got[0] != p.devRID {
		t.Fatalf("live wait registrations = %v, want exactly [%s] before the re-auth", got, p.devRID)
	}

	nPub, nPriv := newRelayAuthKey(t)
	newRID := reauthSameConn(t, p.device, authFor(nPub, nPriv))
	if newRID == p.devRID {
		t.Fatal("the re-auth produced the same routing id; the fixture is not exercising the leak")
	}

	cancelWait()
	if err := p.device.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		got := waitKeys(srv)
		if len(got) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("s.waits still holds %v after the connection re-authenticated as %s and closed; the slot registered under %s was orphaned, so that routing id can never park a wait again (registerWait refuses while the key is busy, and severWaitLocked requires w.sc == sc, which no future connection satisfies)",
				got, newRID, p.devRID)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// And the routing id is usable again: a fresh connection for A parks a wait.
	revived := dialAuthed(t, srv.URL(), p.devAuth)
	second, cancelSecond := s6bParkWait(t, revived, 0, 200*time.Millisecond)
	defer cancelSecond()
	select {
	case r := <-second:
		t.Fatalf("a fresh connection for the re-used routing id could not park a wait (items=%d err=%v)", len(r.items), r.err)
	default:
	}
}
