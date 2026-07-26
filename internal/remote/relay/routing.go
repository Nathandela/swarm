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

// consentContext domain-separates the ROUTE-CONSENT statement from the
// connection-auth challenge. The two are signed by the SAME relay-auth key, so a
// distinct, non-empty prefix is what stops one being produced as a side effect of
// the other: no auth challenge can ever equal a consent message, because the
// challenge's first bytes are authContext and a consent's are these.
var consentContext = []byte("swarm-relay-consent-v1\x00")

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

// ConsentMessage is the canonical statement a party's relay-auth key signs to
// grant granteeRoutingID authority over that party's own route: append to its
// mailbox, wake it, and revoke it. It is ADR-007 B27's consent signature, which
// B38 made mandatory, and it needs no new crypto — the signer is the existing
// relay-auth key, through the custody it already has (crypto.KeyStore.SignRelayAuth
// on the phone, machineid.Identity.RelayAuthSign on the machine).
//
// WHAT IT BINDS, AND WHY THAT IS THE WHOLE SECURITY ARGUMENT. The GRANTER is bound
// implicitly and unforgeably: the relay verifies under the granter's own relay-auth
// public key, which is the key its routing id derives from. The GRANTEE is bound
// explicitly by its routing id, so the signature is NOT a bearer token — a copy
// taken off the machine's disk, off the wire, or out of a device registry authorizes
// only the one routing id it names, and any other caller presenting it is refused.
// There is no nonce and none is needed: the statement is a standing grant, not a
// challenge response, and it is revoked by device_revoke rather than by expiry.
//
// It is deterministic in its input and carries no length prefix because it has one
// variable-length field at the end, so no two grantees share an encoding.
func ConsentMessage(granteeRoutingID string) []byte {
	b := make([]byte, 0, len(consentContext)+len(granteeRoutingID))
	b = append(b, consentContext...)
	b = append(b, granteeRoutingID...)
	return b
}
