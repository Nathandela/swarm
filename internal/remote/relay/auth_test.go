package relay

// R-REL.2 — connection auth WITHOUT the relay learning any identity key. The
// relay authenticates via an Ed25519 relay-auth signed challenge (nonce||ctx)
// bound to the routing id derived from the relay-auth pubkey; it never requires,
// stores, or learns any X25519 identity key, pairing secret, or plaintext.

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestRelayAuth_ChallengeResponse asserts a full signed-challenge handshake
// succeeds and binds the connection to RoutingID(relay-auth pub).
func TestRelayAuth_ChallengeResponse(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	pub, priv := newRelayAuthKey(t)

	c := dialAuthed(t, srv.URL(), authFor(pub, priv))
	if got, want := c.RoutingID(), RoutingID(pub); got != want {
		t.Fatalf("authenticated routing id: got %q, want %q (HKDF of relay-auth pub)", got, want)
	}

	// The signed challenge is nonce||ctx: the canonical message binds BOTH a
	// server nonce and the routing id, so a signature cannot be replayed across
	// routes or contexts.
	msgA := AuthChallengeMessage([]byte("nonce-A"), RoutingID(pub))
	msgB := AuthChallengeMessage([]byte("nonce-B"), RoutingID(pub))
	msgC := AuthChallengeMessage([]byte("nonce-A"), "other-route")
	if bytes.Equal(msgA, msgB) {
		t.Fatalf("AuthChallengeMessage ignores the nonce: A==B")
	}
	if bytes.Equal(msgA, msgC) {
		t.Fatalf("AuthChallengeMessage ignores the routing-id context: A==C")
	}
	if !ed25519.Verify(pub, msgA, ed25519.Sign(priv, msgA)) {
		t.Fatalf("a signature over the canonical challenge must verify under the relay-auth pub")
	}
}

// TestRelayAuth_BadSignatureRefused asserts a wrong signature is rejected: the
// connection never becomes authenticated.
func TestRelayAuth_BadSignatureRefused(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	pub, _ := newRelayAuthKey(t)
	_, wrongPriv := newRelayAuthKey(t)

	// Present pub but sign with an unrelated private key: the relay verifies the
	// signature against the claimed pubkey and refuses.
	bad := ClientAuth{
		RelayAuthPub: pub,
		Sign:         func(ch []byte) []byte { return ed25519.Sign(wrongPriv, ch) },
	}
	if _, err := Dial(testCtx(t), srv.URL(), bad); err == nil {
		t.Fatalf("Dial with a bad signature succeeded, want refusal")
	}

	// A structurally invalid (zeroed) signature is likewise refused, never a panic.
	zero := ClientAuth{
		RelayAuthPub: pub,
		Sign:         func(ch []byte) []byte { return make([]byte, ed25519.SignatureSize) },
	}
	if _, err := Dial(testCtx(t), srv.URL(), zero); err == nil {
		t.Fatalf("Dial with a zero signature succeeded, want refusal")
	}
}

// TestRelay_StoresNoIdentityKeys asserts the relay's persisted state never
// contains any X25519 identity/recipient key, pairing secret, or session
// plaintext — only the relay-auth pubkey + opaque ciphertext + routing metadata.
func TestRelay_StoresNoIdentityKeys(t *testing.T) {
	srv, cfg, _, clk := startTestRelay(t, nil)

	// A machine with a full X25519 identity authenticates and appends a mailbox
	// item for a paired device; only relay-auth material and ciphertext should
	// ever touch relay storage.
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	mPub, mPriv := newRelayAuthKey(t)
	dev, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileKeyStore: %v", err)
	}

	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dev.RelayAuthPublic())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	sp := newSealParty(t, id.NoiseStaticPublic(), dev.RecipientPublic())
	secret := []byte("SUPERSECRET-SESSION-PLAINTEXT")
	env := sp.sealMailbox(t, 1, secret, clk)
	if _, err := machine.MailboxAppend(testCtx(t), RoutingID(ed25519.PublicKey(dev.RelayAuthPublic())), env); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}

	// Flush + inspect the raw persisted store: the machine's X25519 identity keys,
	// the device's recipient key, and the plaintext sentinel must be absent.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := os.ReadFile(cfg.DBPath)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	for name, forbidden := range map[string][]byte{
		"noise-static identity pub": id.NoiseStaticPublic(),
		"recipient identity pub":    id.RecipientPublic(),
		"device recipient pub":      dev.RecipientPublic(),
		"session plaintext":         secret,
	} {
		if bytes.Contains(raw, forbidden) {
			t.Fatalf("relay store leaked %s at rest", name)
		}
	}
}
