package skeleton

// Shared consent fixture for the E2E tests in this package (ADR-007 B47).
//
// A route consent is bound to the pairing ceremony that produced it, so it is no longer
// a bare signature: it is the ceremony-bound statement plus the ceremony id that names
// it, packaged as one credential. These tests short-circuit the pairing ceremony and
// authorize at the relay directly, so they mint a fresh ceremony id per consent — which
// is what a real pairing produces, and what keeps each of them independent of the others.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

func e2eConsent(priv ed25519.PrivateKey, granteeRID string) []byte {
	id := e2eCeremonyID()
	return relay.MarshalConsent(id, ed25519.Sign(priv, relay.ConsentMessage(id, granteeRID)))
}

// e2eCeremonyID mints a rendezvous-shaped id: the same 16 random bytes hex-encoded that
// BeginPairing puts in the QR.
func e2eCeremonyID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("skeleton test: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
