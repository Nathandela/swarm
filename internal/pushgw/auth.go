package pushgw

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/pushreg"
)

// expiryHorizon is PG-AUTH-3's window: a signed request's expiry must be no more than this
// far in the future (and never in the past) of the gateway's own clock.
const expiryHorizon = 120 * time.Second

// noncePattern is PG-AUTH-1's normative shape for Swarm-Nonce: 16 CSPRNG bytes, base64url
// unpadded. PG-AUTH-1's unambiguity argument for the canonical string's "|" separator rests
// on every component's alphabet excluding "|" -- an argument the wire form only actually
// enforces if the gateway checks it, rather than accepting any header value and trusting
// the argument to hold. Enforced before the value is used as a canonical-string component
// or a nonce-cache key, on the same pattern register.go already applies to Idempotency-Key.
var noncePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// nonceCache is PG-AUTH-4's authenticated-request replay cache, keyed (installation_id,
// nonce). It is a bounded cache on its own horizon (PG-RET-4), not a durable stored field,
// so it lives in memory and is pruned lazily on access.
type nonceCache struct {
	mu      sync.Mutex
	entries map[string]time.Time // "installationID\x00nonce" -> expiry
}

func newNonceCache() *nonceCache {
	return &nonceCache{entries: make(map[string]time.Time)}
}

// checkAndStore returns true (and records the nonce) the first time (installationID,
// nonce) is seen before expiry; it returns false -- a replay -- on every later call with
// the same pair, until the entry's own horizon lapses.
func (c *nonceCache) checkAndStore(installationID, nonce string, now, expiry time.Time) bool {
	key := installationID + "\x00" + nonce
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, exp := range c.entries {
		if now.After(exp) {
			delete(c.entries, k)
		}
	}
	if exp, ok := c.entries[key]; ok && now.Before(exp) {
		return false
	}
	horizon := expiry
	if ceiling := now.Add(expiryHorizon); horizon.Before(ceiling) {
		horizon = ceiling
	}
	c.entries[key] = horizon
	return true
}

// unmarshalP256 decodes a SEC1 uncompressed P-256 point (0x04 || X || Y, 65 bytes) into a
// verifiable public key (PG-AUTH-2, spec section 3.1's installation_public_key pattern).
// crypto/ecdh's NewPublicKey does the encoding and on-curve validation (the non-deprecated
// replacement for elliptic.IsOnCurve); ecdsa.Verify still wants an *ecdsa.PublicKey, so the
// X/Y pair is parsed again once validation has already ruled out a malformed point.
func unmarshalP256(raw []byte) (*ecdsa.PublicKey, bool) {
	if _, err := ecdh.P256().NewPublicKey(raw); err != nil {
		return nil, false
	}
	x := new(big.Int).SetBytes(raw[1:33])
	y := new(big.Int).SetBytes(raw[33:65])
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, true
}

func verifyP256P1363(pub *ecdsa.PublicKey, message, signature []byte) bool {
	if len(signature) != 64 {
		return false
	}
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	half := new(big.Int).Rsh(pub.Curve.Params().N, 1)
	if s.Cmp(half) > 0 {
		return false
	}
	digest := sha256.Sum256(message)
	return ecdsa.Verify(pub, digest[:], r, s)
}

func verifyRegistrationProof(r *http.Request, pub *ecdsa.PublicKey, idempotencyKey string, body []byte) bool {
	const prefix = "p256-sha256 "
	values := r.Header.Values("Swarm-Registration-Proof")
	if len(values) != 1 || len(values[0]) != len(prefix)+86 || !strings.HasPrefix(values[0], prefix) {
		return false
	}
	encoded := strings.TrimPrefix(values[0], prefix)
	signature, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != encoded {
		return false
	}
	return verifyP256P1363(pub, pushreg.RegistrationProofMessage(idempotencyKey, body), signature)
}

// verifyResult is what a successful installation-signature check (PG-AUTH-1) hands back
// to the caller so it can perform the post-auth housekeeping PG-AUTH-4/5 require.
type verifyResult struct {
	nonce  string
	expiry time.Time
}

// authOutcome is either a verifyResult or exactly one errSpec explaining the refusal --
// never both, so a handler cannot accidentally act on a result that failed to verify.
type authOutcome struct {
	ok  *verifyResult
	err *errSpec
}

// verifyInstallationSignature implements PG-AUTH-1..5 against one installation's public
// key. installationID is used only to key the nonce cache and the inactivity-clock touch;
// the caller resolves it (from the URL path for rotate/allocate, from the target address's
// stored binding for revoke's owner arm -- see revoke.go's file header for that gap).
func (s *Server) verifyInstallationSignature(r *http.Request, method, path string, body []byte, pub *ecdsa.PublicKey) authOutcome {
	if r.URL.RawQuery != "" {
		e := errMalformedRequest
		return authOutcome{err: &e}
	}
	nonce := r.Header.Get("Swarm-Nonce")
	expiryStr := r.Header.Get("Swarm-Expiry")
	sigHeader := r.Header.Get("Swarm-Signature")
	if nonce == "" || expiryStr == "" || sigHeader == "" {
		e := errUnauthorized
		return authOutcome{err: &e}
	}
	if !noncePattern.MatchString(nonce) {
		e := errUnauthorized
		return authOutcome{err: &e}
	}
	const sigPrefix = "p256-sha256 "
	if !strings.HasPrefix(sigHeader, sigPrefix) {
		e := errUnauthorized
		return authOutcome{err: &e}
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sigHeader, sigPrefix))
	if err != nil || len(sigBytes) != 64 {
		e := errUnauthorized
		return authOutcome{err: &e}
	}
	expirySec, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		e := errUnauthorized
		return authOutcome{err: &e}
	}

	bodyHash := sha256.Sum256(body)
	canonical := strings.Join([]string{
		"swarm-pg-v1",
		strings.ToUpper(method),
		path,
		base64.RawURLEncoding.EncodeToString(bodyHash[:]),
		nonce,
		strconv.FormatInt(expirySec, 10),
	}, "|")
	if !verifyP256P1363(pub, []byte(canonical), sigBytes) {
		e := errUnauthorized
		return authOutcome{err: &e}
	}

	now := s.now()
	expiry := time.Unix(expirySec, 0)
	if expiry.Before(now) || expiry.After(now.Add(expiryHorizon)) {
		e := errRequestExpired()
		return authOutcome{err: &e}
	}
	return authOutcome{ok: &verifyResult{nonce: nonce, expiry: expiry}}
}
