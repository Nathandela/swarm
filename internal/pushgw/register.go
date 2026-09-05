package pushgw

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/Nathandela/swarm/internal/pushreg"
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
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
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
	if s.v2store == nil {
		if e, ok := s.regIdem.get(cacheKey, now); ok {
			return s.writeJSON(w, http.StatusCreated, registerResponse{InstallationID: e.installationID, RefreshBefore: e.refreshBefore})
		}
	} else {
		// Admission is an in-memory operator decision and must precede any attacker-selected
		// Firestore read. An accepted completed retry remains cheap and provider-free while
		// the key stays admitted; removing it closes registration immediately.
		if !s.registrationAdmission(req.InstallationPublicKey) {
			s.writeErr(w, errBetaClosed)
			return errBetaClosed.status
		}
		result, found, mismatch, lookupErr := s.v2store.p.lookupRegistration(r.Context(), s.v2store.idempotencyID(idemKey), hex.EncodeToString(bodySum[:]), now)
		if lookupErr != nil {
			s.writeErr(w, errInternal)
			return errInternal.status
		}
		if mismatch {
			s.writeErr(w, errIdempotencyConflict)
			return errIdempotencyConflict.status
		}
		if found {
			return s.writeJSON(w, http.StatusCreated, registerResponse(result))
		}
	}

	src := s.sourceIP(r)
	// Consume the bounded global document before creating an attacker-variable source
	// document, so source spoofing cannot bypass the shared admission ceiling.
	if ok, retryAfter, limitErr := s.allow(r.Context(), "reg-global", s.quotas.RegistrationsGlobal, now); limitErr != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	} else if !ok {
		spec := errQuotaExceeded(retryAfter)
		s.writeErr(w, spec)
		return spec.status
	}
	if ok, retryAfter, limitErr := s.allow(r.Context(), "reg:"+src, s.quotas.RegistrationsPerSourceIP, now); limitErr != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	} else if !ok {
		spec := errQuotaExceeded(retryAfter)
		s.writeErr(w, spec)
		return spec.status
	}

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	installationID := base64.RawURLEncoding.EncodeToString(idBytes)
	leaseBytes := make([]byte, 16)
	if _, err := rand.Read(leaseBytes); err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	leaseID := hex.EncodeToString(leaseBytes)
	if s.v2store != nil {
		result, won, busy, mismatch, claimErr := s.v2store.p.claimRegistration(r.Context(), s.v2store.idempotencyID(idemKey), hex.EncodeToString(bodySum[:]), installationID, leaseID, now)
		if claimErr != nil {
			s.writeErr(w, errInternal)
			return errInternal.status
		}
		if mismatch {
			s.writeErr(w, errIdempotencyConflict)
			return errIdempotencyConflict.status
		}
		if result.InstallationID != "" {
			return s.writeJSON(w, http.StatusCreated, registerResponse(result))
		}
		if busy || !won {
			s.writeErr(w, errRegistrationInProgress)
			return errRegistrationInProgress.status
		}
	}

	wantHash, err := pushreg.RequestHash(req.InstallationPublicKey, req.FCMToken)
	if err != nil {
		if s.v2store != nil {
			_ = s.v2store.p.releaseRegistration(r.Context(), s.v2store.idempotencyID(idemKey), leaseID, s.now())
		}
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	binding, err := s.attest.Verify(r.Context(), req.Attestation.Token)
	if err != nil {
		if s.v2store != nil {
			_ = s.v2store.p.releaseRegistration(r.Context(), s.v2store.idempotencyID(idemKey), leaseID, s.now())
		}
		if errors.Is(err, ErrAttestationUnavailable) {
			s.writeErr(w, errAttestationUnavailable)
			return errAttestationUnavailable.status
		}
		s.writeErr(w, errAttestationInvalid)
		return errAttestationInvalid.status
	}
	if binding.RequestHash != wantHash || !binding.LicensedBuild {
		if s.v2store != nil {
			_ = s.v2store.p.releaseRegistration(r.Context(), s.v2store.idempotencyID(idemKey), leaseID, s.now())
		}
		s.writeErr(w, errAttestationInvalid)
		return errAttestationInvalid.status
	}

	tokenEnc, keyVersion, err := s.encryptToken(req.FCMToken)
	if err != nil {
		if s.v2store != nil {
			_ = s.v2store.p.releaseRegistration(r.Context(), s.v2store.idempotencyID(idemKey), leaseID, s.now())
		}
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	commitNow := s.now()
	nowMs := commitNow.UnixMilli()
	rec := installationRecord{
		PublicKey:       pubRaw,
		FCMTokenEnc:     tokenEnc,
		CreatedAtMs:     nowMs,
		LastActiveMs:    nowMs,
		LicensedBuild:   binding.LicensedBuild,
		TokenKeyVersion: keyVersion,
		TokenGeneration: 1,
	}
	if s.v2store != nil {
		result, completed, commitErr := s.v2store.p.completeRegistration(r.Context(), s.v2store.idempotencyID(idemKey), hex.EncodeToString(bodySum[:]), leaseID, rec, commitNow)
		if commitErr != nil {
			s.writeErr(w, errInternal)
			return errInternal.status
		}
		if !completed {
			s.writeErr(w, errRegistrationInProgress)
			return errRegistrationInProgress.status
		}
		return s.writeJSON(w, http.StatusCreated, registerResponse(result))
	}
	if err := s.store.putInstallation(installationID, rec); err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}

	refreshBefore := now.Add(180 * 24 * time.Hour).UTC().Format(time.RFC3339)
	s.regIdem.put(cacheKey, regIdemEntry{installationID: installationID, refreshBefore: refreshBefore}, now)

	return s.writeJSON(w, http.StatusCreated, registerResponse{InstallationID: installationID, RefreshBefore: refreshBefore})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) int {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
	return status
}
