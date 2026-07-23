// G0 — the gateway binary authenticates to the relay via relay.Dial(url,
// relay.ClientAuth{...}), whose auth carries a signing closure over the
// relay's challenge (see internal/remote/relay/client.go's ClientAuth and
// Dial, and internal/remote/relay/harness_test.go's authFor):
//
//	type ClientAuth struct {
//	    RelayAuthPub ed25519.PublicKey
//	    Sign         func(challenge []byte) []byte
//	}
//
// machineid.Identity currently exposes ONLY RelayAuthPublic() — no way to
// SIGN a relay challenge with the relay-auth private key it already custodies.
//
// INTENDED CONTRACT (RED — does not exist yet; GREEN implements it):
//
//	func (id *Identity) RelayAuthSign(challenge []byte) []byte
//
// matching relay.ClientAuth.Sign's exact signature (func([]byte) []byte, no
// error), so a gateway builds a ClientAuth via
// relay.ClientAuth{RelayAuthPub: id.RelayAuthPublic(), Sign: id.RelayAuthSign}
// without any wrapping. The signature is Ed25519 over the challenge using the
// relay-auth private key (mirrors GrantSignPrivate/GrantSignPublic's custody
// pattern for the grant-signing key, except relay-auth exposes only a signer,
// never the raw private, since the relay-auth key never needs to leave this
// package the way enroll.Enroll needs the raw grant-signing key).
package machineid

import (
	"bytes"
	"crypto/ed25519"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TestMachineIdentity_RelayAuthSignVerifiesUnderPublic pins the sign/verify
// contract the relay handshake depends on (relay.Dial computes
// auth.Sign(AuthChallengeMessage(nonce, rid)) and the relay verifies it under
// RelayAuthPub): a signature from RelayAuthSign must verify under
// RelayAuthPublic() for the exact challenge signed, and must NOT verify for a
// tampered challenge or under a different identity's public key.
func TestMachineIdentity_RelayAuthSignVerifiesUnderPublic(t *testing.T) {
	id, err := Generate("gateway-host")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	challenge := []byte("relay-challenge-nonce-plus-routing-id")
	sig := id.RelayAuthSign(challenge)

	if !ed25519.Verify(id.RelayAuthPublic(), challenge, sig) {
		t.Fatal("RelayAuthSign signature does not verify under RelayAuthPublic() for the signed challenge")
	}

	tampered := append([]byte(nil), challenge...)
	tampered[0] ^= 0xFF
	if ed25519.Verify(id.RelayAuthPublic(), tampered, sig) {
		t.Error("RelayAuthSign signature verified under a tampered challenge")
	}

	other, err := Generate("other-host")
	if err != nil {
		t.Fatalf("Generate(other): %v", err)
	}
	if ed25519.Verify(other.RelayAuthPublic(), challenge, sig) {
		t.Error("RelayAuthSign signature verified under a different identity's public key")
	}
}

// TestMachineIdentity_RelayAuthSignSurvivesSaveLoad pins that the relay-auth
// private key round-trips through Save/Load: a Load-reconstructed Identity
// must still be able to sign a challenge into a signature that verifies under
// its (also reloaded) RelayAuthPublic().
func TestMachineIdentity_RelayAuthSignSurvivesSaveLoad(t *testing.T) {
	m := stdMaterial(t)
	id := NewFromMaterial("build-host", m)

	path := filepath.Join(t.TempDir(), "machine-identity.key")
	if err := id.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	challenge := []byte("post-reload-relay-challenge")
	sig := reloaded.RelayAuthSign(challenge)

	if !bytes.Equal(reloaded.RelayAuthPublic(), id.RelayAuthPublic()) {
		t.Fatalf("RelayAuthPublic changed across Save/Load: got %x, want %x", reloaded.RelayAuthPublic(), id.RelayAuthPublic())
	}
	if !ed25519.Verify(reloaded.RelayAuthPublic(), challenge, sig) {
		t.Error("post-reload RelayAuthSign signature does not verify under the reloaded RelayAuthPublic()")
	}
}

// TestMachineIdentity_RelayAuthSignBuildsClientAuth is a constructive check
// that an Identity's accessors satisfy relay.ClientAuth's exact field types
// with no wrapping (RelayAuthSign's method value must be directly assignable
// to the Sign func([]byte) []byte field) — the shape the gateway binary needs
// to call relay.Dial.
func TestMachineIdentity_RelayAuthSignBuildsClientAuth(t *testing.T) {
	id, err := Generate("gateway-host")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	auth := relay.ClientAuth{
		RelayAuthPub: id.RelayAuthPublic(),
		Sign:         id.RelayAuthSign,
	}

	challenge := []byte("client-auth-challenge")
	sig := auth.Sign(challenge)
	if !ed25519.Verify(auth.RelayAuthPub, challenge, sig) {
		t.Error("signature from a relay.ClientAuth built with RelayAuthSign does not verify under RelayAuthPub")
	}
}
