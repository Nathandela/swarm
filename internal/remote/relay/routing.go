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

// maxCeremonyIDLen bounds the ceremony id a consent may name (ADR-007 B61).
//
// PRODUCTION SENDS 32: hex of the 16-byte rendezvous id, and that is the ONLY producer
// in the tree — mobile/pairing.go's Consent callback over pairing.QRPayload.RendezvousID
// ([16]byte). 128 is four times that, so the rule leaves the real format room to change
// shape without a second decision here.
//
// IT IS A LENGTH BOUND AND NOT A FORMAT BOUND ON PURPOSE. "exactly 32 hex chars" would
// describe production exactly and still be wrong: the id is an opaque label to this
// relay, TestB47_AnUnknownCeremonyIsAccepted deliberately presents one this relay has
// never seen so that deliverEpochGrant survives a store rebuild, and every fixture in the
// B47 suite names its ceremony in prose. A relay that demanded hex would refuse ids it
// has no business having an opinion about.
//
// WHAT THE BOUND IS FOR IS NOT DISK. The id rides into bucketConsents as a bbolt VALUE,
// which is unbounded, but retiredKey makes it part of a bbolt KEY — and bbolt refuses a
// key over 32768 bytes. Since that only happens at supersession and at revoke, an
// oversized id was ACCEPTED at pairing and then aborted the whole revokeAndPurge
// transaction forever after: the pairs edges were never deleted, device_revoke and the
// re-pair that would recover both failed, and the pairing became permanently unrevokable.
// The device CHOOSES this id and signs it, and internal/remote/device/registry.go stores
// it without a length check by explicit design ("the relay is the authority that verifies
// it"), so the whole weight of that rule rests here. Bounding it well below bbolt's limit
// means every key retiredKey can ever build is writable, which is what makes the owner's
// revoke unconditional (ADR-007 B47/B49, PB-STATE-10).
//
// It also makes store.maxRetiredPerPair a real bound on the bucket rather than only on
// the row count — see the argument there.
const maxCeremonyIDLen = 128

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
//
// AND IT BINDS THE CEREMONY THAT PRODUCED IT (ADR-007 B47). This used to read "there
// is no nonce and none is needed: the statement is a standing grant... and it is
// revoked by device_revoke rather than by expiry". Both halves were true and the join
// was never checked: revokeAndPurge deletes the pairs edges and writes a ban, and the
// SIGNATURE is a durable artifact the grantee still holds — so re-presenting the
// identical bytes rewrote both edges and, because authorizePair clears a ban placed by
// that same pairer, lifted the ban in the same transaction. `swarm remote revoke` was
// undone by a file, and the phone was never asked.
//
// THE CEREMONY ID IS THE RENDEZVOUS ID, WHICH IS WHY THERE IS NO BOOTSTRAP HERE. Both
// parties hold it from the QR before msg1, so nothing has to be fetched from the relay
// before the machine can speak — the recorded alternative, a relay-held generation
// counter, would have to reach the phone in msg2 while being keyed by a routing id the
// machine does not learn until msg3, which is deliverEpochGrant's own shape.
//
// It is NOT a challenge and confers no freshness on the connection: the statement stays
// a standing grant, presentable as often as the machine's gateway needs it (see
// store.authorizePair, where a re-presentation of the LIVE id is idempotent). What it
// buys is that the relay can RETIRE one, which is what a revoke now does.
//
// It is deterministic in its inputs, and the ceremony id is length-prefixed because
// there are now two variable-length fields: without it "ab"+"c" and "a"+"bc" would
// share an encoding (F11 — no splicing).
func ConsentMessage(ceremonyID, granteeRoutingID string) []byte {
	b := make([]byte, 0, len(consentContext)+4+len(ceremonyID)+len(granteeRoutingID))
	b = append(b, consentContext...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(ceremonyID)))
	b = append(b, ceremonyID...)
	b = append(b, granteeRoutingID...)
	return b
}

// MarshalConsent packages a signed consent for carriage: the ceremony id the signature
// covers, then the signature. It travels as ONE opaque credential rather than two
// parameters so everything that stores or forwards it — the device registry, the gateway
// params, authorize_device — keeps handling a single blob, and so no call site can hold a
// signature and a ceremony id that were never produced together.
//
// Carrying the id is not trusting it: ParseConsent only reads it back out, and
// handleAuthorizeDevice verifies the signature OVER it, so a relabelled credential
// verifies against nothing.
func MarshalConsent(ceremonyID string, sig []byte) []byte {
	b := make([]byte, 0, 4+len(ceremonyID)+len(sig))
	b = binary.BigEndian.AppendUint32(b, uint32(len(ceremonyID)))
	b = append(b, ceremonyID...)
	return append(b, sig...)
}

// ParseConsent is the inverse of MarshalConsent. A malformed or truncated credential is
// an error, never a zero-length ceremony id that might match a stored one.
func ParseConsent(b []byte) (ceremonyID string, sig []byte, err error) {
	if len(b) < 4 {
		return "", nil, ErrConsentMalformed
	}
	n := binary.BigEndian.Uint32(b[:4])
	b = b[4:]
	if uint32(len(b)) < n {
		return "", nil, ErrConsentMalformed
	}
	return string(b[:n]), b[n:], nil
}
