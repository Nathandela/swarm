package relay

// R-REL.5 — push trigger forwarding (ciphertext only). The gateway triggers a
// device wake with an opaque push envelope; the relay forwards to APNs with only
// a generic outer alert + ciphertext for the NSE. The relay cannot read push
// content.

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

// pushFixture authorizes a device, registers its push token, and returns the
// machine client + device routing id.
func pushFixture(t *testing.T, srv *Server) (machine *Client, devRID, token string) {
	t.Helper()
	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	token = "apns-token-push"
	if err := device.TokenRegister(testCtx(t), token); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}
	mPub, mPriv := newRelayAuthKey(t)
	machine = dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	return machine, RoutingID(dPub), token
}

// TestPush_OuterPayloadGeneric asserts the outer APNs payload carries only a
// fixed generic alert and the opaque ciphertext — never session ids or command
// text.
func TestPush_OuterPayloadGeneric(t *testing.T) {
	srv, _, apns, clk := startTestRelay(t, nil)
	machine, devRID, token := pushFixture(t, srv)

	// A push envelope of the ONE size the channel admits (PB-PUSH-3's schema, see
	// PushEnvelopeSize). It carries no plaintext because it CANNOT: an envelope with a body
	// is refused at the handler, so "session or command text reaches the outer payload" is now
	// unrepresentable on this path rather than merely unobserved on one fixture. What is still
	// asserted below is the part that is representable -- the alert is the fixed constant and
	// derives nothing from the request the relay was handed.
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	env := sp.sealPush(t, 1, clk)

	if err := machine.PushTrigger(testCtx(t), devRID, env); err != nil {
		t.Fatalf("PushTrigger: %v", err)
	}
	pushes := apns.all()
	if len(pushes) != 1 {
		t.Fatalf("push count: got %d, want 1", len(pushes))
	}
	got := pushes[0]
	if got.token != token {
		t.Fatalf("push token: got %q, want %q", got.token, token)
	}
	if got.payload.Alert != GenericPushAlert {
		t.Fatalf("outer alert not generic: got %q, want the fixed GenericPushAlert", got.payload.Alert)
	}
	// The outer payload must not leak the routing id, which is the one caller-chosen string
	// this handler still receives and therefore the only one it could copy.
	if bytes.Contains([]byte(got.payload.Alert), []byte(devRID)) {
		t.Fatalf("outer alert leaked the device routing id")
	}
}

// TestPush_RelaySeesOnlyCiphertext asserts the relay forwards the exact opaque
// envelope bytes and never a decrypted body: a plaintext sentinel sealed inside
// never appears in the outer payload the relay produces.
//
// THE SENTINEL RIDES THE MAILBOX, not the push, and that is PB-PUSH-3's schema rather than a
// change of subject. The push channel admits exactly PushEnvelopeSize bytes -- a header over
// an EMPTY plaintext -- so a content-bearing push envelope cannot be built to be leaked from.
// The reachable question is therefore the one asked here: the relay HOLDS a sealed body for
// this device in its mailbox, and none of it may appear in the push it sends the same device.
func TestPush_RelaySeesOnlyCiphertext(t *testing.T) {
	srv, _, apns, clk := startTestRelay(t, nil)
	machine, devRID, _ := pushFixture(t, srv)

	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	secret := []byte("PLAINTEXT-THE-RELAY-MUST-NOT-SEE")
	stored := sp.sealMailbox(t, 1, secret, clk)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, stored); err != nil {
		t.Fatalf("MailboxAppend of the sealed sentinel: %v", err)
	}

	env := sp.sealPush(t, 2, clk)
	if err := machine.PushTrigger(testCtx(t), devRID, env); err != nil {
		t.Fatalf("PushTrigger: %v", err)
	}
	pushes := apns.all()
	if len(pushes) != 1 {
		t.Fatalf("push count: got %d, want 1", len(pushes))
	}
	// The forwarded ciphertext is exactly the opaque envelope the gateway handed
	// in — and neither the stored envelope nor its plaintext is anywhere in the outer payload.
	if !bytes.Equal(pushes[0].payload.Ciphertext, env) {
		t.Fatalf("relay altered the opaque push ciphertext in transit")
	}
	if bytes.Contains(pushes[0].payload.Ciphertext, secret) ||
		bytes.Contains(pushes[0].payload.Ciphertext, stored) {
		t.Fatalf("the mailbox body the relay is holding for this device appeared in its push payload")
	}
	if bytes.Contains([]byte(pushes[0].payload.Alert), secret) {
		t.Fatalf("outer alert leaked the sealed plaintext")
	}
}
