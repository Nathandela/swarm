package pushgw

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"
)

// idempotencyKeyPattern is section 3.6's normative shape: 16 CSPRNG bytes, base64url
// unpadded. Enforced before anything else that would treat the header as a lookup key --
// an unvalidated key is otherwise a caller-chosen identity, not a retry token (see
// register.go's package-level comment on the idempotency cache below).
var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// registerBodyMax is PG-TR-3's 12 KiB register row -- large enough that a body filled to
// every field's declared maxLength (10423 octets, section 1's own arithmetic) still fits.
const registerBodyMax = 12 * 1024

type registerRequest struct {
	InstallationPublicKey string `json:"installation_public_key"`
	FCMToken              string `json:"fcm_token"`
	Attestation           struct {
		Kind  string `json:"kind"`
		Token string `json:"token"`
	} `json:"attestation"`
}

type registerResponse struct {
	InstallationID string `json:"installation_id"`
	RefreshBefore  string `json:"refresh_before"`
}

// handleRegister implements POST /v1/installations (spec section 3.1).
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) int {
	if hasContentEncoding(r) {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}
	if !hasExactContentType(r, "application/json") {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}
	idemKey := r.Header.Get("Idempotency-Key")
	if !idempotencyKeyPattern.MatchString(idemKey) {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}

	body, tooLarge, err := readBounded(r, registerBodyMax)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if tooLarge {
		spec := errBodyTooLarge()
		s.writeErr(w, spec)
		return spec.status
	}

	var req registerRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}
	if req.InstallationPublicKey == "" || req.FCMToken == "" ||
		req.Attestation.Kind != "play_integrity" || req.Attestation.Token == "" {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}
	pubRaw, err := base64.RawURLEncoding.DecodeString(req.InstallationPublicKey)
	if err != nil {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}
	if _, ok := unmarshalP256(pubRaw); !ok {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}

	// PG-REG-2: a repeated Idempotency-Key inside its retention window returns the
	// mapping already minted rather than a fresh one -- but ONLY for a byte-identical
	// retry. The cache key is bound to BOTH the hashed Idempotency-Key and a hash of the
	// body, so a second caller presenting the same key with a DIFFERENT body (a different
	// installation_public_key or fcm_token) is a cache MISS: it falls through to the
	// normal, fail-closed attestation flow below rather than being handed the first
	// caller's installation_id. Without this, the idempotency cache was an unauthenticated
	// admission-control bypass -- PG-AUTH-13 requires attestation before a registration is
	// ever admitted, and a lookup keyed on the client-chosen header alone let a second body
	// skip it entirely.
	//
	// RECORDED DEVIATION, escalated rather than silently carried: PG-REG-2 as literally
	// written -- "a repeated registration with the same Idempotency-Key ... SHALL return the
	// same installation_id", unconditioned on the body -- is satisfied by this cache only for
	// a byte-identical retry, not for any two requests sharing one key. PG-RET-4 also
	// declares the cache's contents exhaustively as "SHA-256(Idempotency-Key) -> the minted
	// installation_id, and nothing else"; keying on a hash of the body too is a second field
	// that closed list does not contemplate, and the body hash is itself a derived value of
	// fcm_token. The engineering reasoning above is sound (an unconditioned cache is an
	// attestation bypass), but this document does not get to promote it to a ruling -- see
	// the return value for the escalation to the owner for a PG-REG-2 amendment and a
	// PG-RET-4 field-list update.
	bodySum := sha256.Sum256(body)
	cacheKey := hashSecret(idemKey) + ":" + hex.EncodeToString(bodySum[:])
	now := s.now()
	if e, ok := s.regIdem.get(cacheKey, now); ok {
		return s.writeJSON(w, http.StatusCreated, registerResponse{InstallationID: e.installationID, RefreshBefore: e.refreshBefore})
	}

	src := s.sourceIP(r)
	if ok, retryAfter := s.limiter.allow("reg:"+src, s.quotas.RegistrationsPerSourceIP, now); !ok {
		spec := errQuotaExceeded(retryAfter)
		s.writeErr(w, spec)
		return spec.status
	}
	if ok, retryAfter := s.limiter.allow("reg-global", s.quotas.RegistrationsGlobal, now); !ok {
		spec := errQuotaExceeded(retryAfter)
		s.writeErr(w, spec)
		return spec.status
	}

	wantHash, err := requestHash(body)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	binding, err := s.attest.Verify(r.Context(), req.Attestation.Token)
	if err != nil {
		if errors.Is(err, ErrAttestationUnavailable) {
			s.writeErr(w, errAttestationUnavailable)
			return errAttestationUnavailable.status
		}
		s.writeErr(w, errAttestationInvalid)
		return errAttestationInvalid.status
	}
	if binding.RequestHash != wantHash || !binding.LicensedBuild {
		s.writeErr(w, errAttestationInvalid)
		return errAttestationInvalid.status
	}

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	installationID := base64.RawURLEncoding.EncodeToString(idBytes)

	tokenEnc, err := s.store.encrypt(req.FCMToken)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	nowMs := now.UnixMilli()
	rec := installationRecord{
		PublicKey:     pubRaw,
		FCMTokenEnc:   tokenEnc,
		CreatedAtMs:   nowMs,
		LastActiveMs:  nowMs,
		LicensedBuild: binding.LicensedBuild,
	}
	if err := s.store.putInstallation(installationID, rec); err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}

	refreshBefore := now.Add(180 * 24 * time.Hour).UTC().Format(time.RFC3339)
	s.regIdem.put(cacheKey, regIdemEntry{installationID: installationID, refreshBefore: refreshBefore}, now)

	return s.writeJSON(w, http.StatusCreated, registerResponse{InstallationID: installationID, RefreshBefore: refreshBefore})
}

// requestHash implements PG-AUTH-11's formula: SHA-256 of the RFC 8785 (JCS)
// canonicalization of the registration body with attestation.token replaced by "".
//
// This is a BEST-EFFORT canonicalizer, not a general RFC 8785 implementation: it relies on
// encoding/json's map-key ordering (bytewise ASCII, which agrees with JCS's UTF-16
// code-unit order for the ASCII field names section 3.1 declares) and disables
// HTML-escaping so no value is silently rewritten. It is deliberately identical to the
// test suite's own jcsRequestHash (helpers_test.go) so both sides are pinned to the same
// digest for a given body.
//
// RECORDED DEVIATION, escalated rather than silently carried: Go's encoding/json still
// escapes U+2028 and U+2029 inside string values unconditionally, even with SetEscapeHTML
// disabled -- RFC 8785 does not. A real Play Integrity client computing a true JCS
// requestHash over an installation_public_key or fcm_token that happens to contain either
// code point would mint a verdict token this gateway rejects as attestation_invalid: the
// exact "verifier that fails on a client library change" PG-AUTH-11 pins JCS to prevent.
// This is invisible to the test suite by construction, because the suite's own
// jcsRequestHash is deliberately the same best-effort function, so both sides agree. Two
// closing moves exist -- vendor a real RFC 8785 canonicalizer before the Play Integrity
// client is wired, or record an ASCII-only restriction on these field values as a permanent
// deviation -- and this comment does not pick one; see the return value for the escalation.
//
// Returns an error rather than a zero [32]byte on failure: the caller compares the result
// against an AttestationVerifier's returned RequestHash for equality (PG-AUTH-11), and an
// all-zero sentinel silently returned here would fail OPEN if a verifier implementation
// ever produced a zero-valued VerdictBinding.RequestHash of its own -- exactly the shape
// this table compares against, not a coincidence worth risking.
func requestHash(body []byte) ([32]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return [32]byte{}, fmt.Errorf("pushgw: canonicalize request body: %w", err)
	}
	if att, ok := m["attestation"].(map[string]any); ok {
		att["token"] = ""
		m["attestation"] = att
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return [32]byte{}, fmt.Errorf("pushgw: canonicalize request body: %w", err)
	}
	return sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) int {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
	return status
}
