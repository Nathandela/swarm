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
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub)); err != nil {
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

	// A push envelope built as a content-free wake (type 0x02). The relay only
	// ever handles the marshalled ciphertext.
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	env := sp.sealMailbox(t, 1, []byte("machine-3 group transition: session feature-x needs approval"), clk)

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
	// The outer payload must not leak the routing id or any inner text.
	if bytes.Contains([]byte(got.payload.Alert), []byte(devRID)) {
		t.Fatalf("outer alert leaked the device routing id")
	}
	if bytes.Contains([]byte(got.payload.Alert), []byte("approval")) ||
		bytes.Contains([]byte(got.payload.Alert), []byte("feature-x")) {
		t.Fatalf("outer alert leaked session/command text")
	}
}

// TestPush_RelaySeesOnlyCiphertext asserts the relay forwards the exact opaque
// envelope bytes and never a decrypted body: a plaintext sentinel sealed inside
// never appears in the outer payload the relay produces.
func TestPush_RelaySeesOnlyCiphertext(t *testing.T) {
	srv, _, apns, clk := startTestRelay(t, nil)
	machine, devRID, _ := pushFixture(t, srv)

	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	secret := []byte("PLAINTEXT-THE-RELAY-MUST-NOT-SEE")
	env := sp.sealMailbox(t, 1, secret, clk)

	if err := machine.PushTrigger(testCtx(t), devRID, env); err != nil {
		t.Fatalf("PushTrigger: %v", err)
	}
	pushes := apns.all()
	if len(pushes) != 1 {
		t.Fatalf("push count: got %d, want 1", len(pushes))
	}
	// The forwarded ciphertext is exactly the opaque envelope the gateway handed
	// in — and the sealed plaintext is nowhere in the outer payload.
	if !bytes.Equal(pushes[0].payload.Ciphertext, env) {
		t.Fatalf("relay altered the opaque push ciphertext in transit")
	}
	if bytes.Contains(pushes[0].payload.Ciphertext, secret) {
		t.Fatalf("sealed plaintext appeared in the push ciphertext — the seal is broken, not the relay's fault, fix the fixture")
	}
	if bytes.Contains([]byte(pushes[0].payload.Alert), secret) {
		t.Fatalf("outer alert leaked the sealed plaintext")
	}
}
