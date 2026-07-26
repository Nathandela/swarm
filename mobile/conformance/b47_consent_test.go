package conformance_test

// Shared consent fixture (ADR-007 B47): a route consent is bound to the pairing ceremony
// that produced it, so it is the ceremony-bound statement plus the ceremony id naming it,
// packaged as one credential. It is signed through the phone's own custody
// (KeyStore.SignRelayAuth), byte-identical to what mobile/pairing.go produces -- a fixture
// that signed something else would leave these tests passing against a credential the
// relay would refuse.

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// consentFrom signs a consent for granteeRID over a FRESH ceremony, which is what each
// new pairing produces. A test that needs two authorizations to be the same ceremony must
// reuse one credential; a test that needs them to be different -- a re-pairing after a
// revoke -- calls this twice.
func consentFrom(t *testing.T, ks crypto.KeyStore, granteeRID string) []byte {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("crypto/rand: %v", err)
	}
	id := hex.EncodeToString(b[:])
	sig, err := ks.SignRelayAuth(relay.ConsentMessage(id, granteeRID))
	if err != nil {
		t.Fatalf("phone signs its relay-route consent: %v", err)
	}
	return relay.MarshalConsent(id, sig)
}
