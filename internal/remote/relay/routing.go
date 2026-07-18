package relay

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"

	"golang.org/x/crypto/hkdf"
)

// routingSalt and routingInfo domain-separate the routing-id KDF so a relay-auth
// pubkey used here can never collide with the same key used for any other
// purpose.
var (
	routingSalt = []byte("swarm-relay-routing-id-v1")
	routingInfo = []byte("routing-id")
)

// authContext domain-separates the connection-auth challenge so a signature over
// it is unusable in any other Ed25519 signing context (R-CRY.3).
var authContext = []byte("swarm-relay-auth-v1\x00")

// RoutingID is the relay's opaque handle for a party: HKDF-SHA256 over the
// party's relay-auth Ed25519 public key. It is deterministic and collision-
// distinct, and it is NOT the raw pubkey — the relay never needs, stores, or can
// recover the pubkey from it (R-REL.11). A zero/short key yields a stable but
// distinct value; callers pass a real 32-byte Ed25519 pubkey.
func RoutingID(pub ed25519.PublicKey) string {
	r := hkdf.New(sha256.New, pub, routingSalt, routingInfo)
	var out [16]byte
	_, _ = io.ReadFull(r, out[:])
	return hex.EncodeToString(out[:])
}

// AuthChallengeMessage is the canonical message the relay-auth key signs during
// connection auth: a domain-separated binding of the server nonce AND the
// claimed routing id (nonce||ctx). Binding both means a signature cannot be
// replayed across routes or contexts. It is deterministic in its inputs.
func AuthChallengeMessage(nonce []byte, routingID string) []byte {
	b := make([]byte, 0, len(authContext)+4+len(nonce)+len(routingID))
	b = append(b, authContext...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(nonce)))
	b = append(b, nonce...)
	b = append(b, []byte(routingID)...)
	return b
}
