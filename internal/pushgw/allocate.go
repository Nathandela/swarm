package pushgw

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"
)

// allocateBodyMax is PG-TR-3's 1 KiB row.
const allocateBodyMax = 1024

// unboundWindow is PG-ALLOC-2's ten-minute unbound sweep horizon.
const unboundWindow = 10 * time.Minute

type allocateResponse struct {
	PushAddress             string `json:"push_address"`
	SubmitCapability        string `json:"submit_capability"`
	MachineRevokeCapability string `json:"machine_revoke_capability"`
	UnboundExpiresAt        string `json:"unbound_expires_at"`
}

// handleAllocate implements POST /v1/installations/{id}/addresses (spec section 3.3).
func (s *Server) handleAllocate(w http.ResponseWriter, r *http.Request) int {
	installationID, ok := pathSegment(r.URL.Path, "/v1/installations/", "/addresses")
	if !ok {
		s.writeErr(w, errUnauthorized)
		return errUnauthorized.status
	}
	if hasContentEncoding(r) {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}
	if !hasExactContentType(r, "application/json") {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}
	body, tooLarge, err := readBounded(r, allocateBodyMax)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if tooLarge {
		spec := errBodyTooLarge()
		s.writeErr(w, spec)
		return spec.status
	}

	inst, found, err := s.getInstallation(r.Context(), installationID)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if !found {
		s.writeErr(w, errUnauthorized)
		return errUnauthorized.status
	}
	pub, ok := unmarshalP256(inst.PublicKey)
	if !ok {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	path := "/v1/installations/" + installationID + "/addresses"
	outcome := s.verifyInstallationSignature(r, r.Method, path, body, pub)
	if outcome.err != nil {
		s.writeErr(w, *outcome.err)
		return outcome.err.status
	}
	now := s.now()
	claimed, err := s.claimNonce(r.Context(), installationID, inst.PublicKey, outcome.ok.nonce, now, outcome.ok.expiry)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if !claimed {
		s.writeErr(w, errNonceReplayed)
		return errNonceReplayed.status
	}

	// The request schema is deliberately empty (section 3.3): the only legal body is {}.
	var empty struct{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&empty); err != nil {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}

	// PG-Q-3: allocations SHALL be bounded per source IP and globally, in addition to
	// the per-installation bound below.
	if ok, retryAfter, limitErr := s.allow(r.Context(), "alloc-global", s.quotas.AllocationsGlobal, now); limitErr != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	} else if !ok {
		spec := errQuotaExceeded(retryAfter)
		s.writeErr(w, spec)
		return spec.status
	}
	if ok, retryAfter, limitErr := s.allow(r.Context(), "alloc-src:"+s.sourceIP(r), s.quotas.AllocationsPerSourceIP, now); limitErr != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	} else if !ok {
		spec := errQuotaExceeded(retryAfter)
		s.writeErr(w, spec)
		return spec.status
	}

	if s.v2store == nil {
		count, countErr := s.store.countAddresses(installationID)
		if countErr != nil {
			s.writeErr(w, errInternal)
			return errInternal.status
		}
		if count >= s.quotas.AllocationsPerInstallation {
			s.writeErr(w, errAddressLimitReached)
			return errAddressLimitReached.status
		}
	}

	addrBytes := make([]byte, 16)
	if _, err := rand.Read(addrBytes); err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	pushAddress := base64.RawURLEncoding.EncodeToString(addrBytes)

	submitCap, err := randomCapability()
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	machineCap, err := randomCapability()
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}

	nowMs := now.UnixMilli()
	unboundExpires := now.Add(unboundWindow)
	rec := addressRecord{
		InstallationID:    installationID,
		SubmitCapHash:     hashSecret(submitCap),
		MachineRevokeHash: hashSecret(machineCap),
		CreatedAtMs:       nowMs,
		Bound:             false,
		UnboundExpiresMs:  unboundExpires.UnixMilli(),
	}
	if s.v2store != nil {
		created, putErr := s.v2store.p.putAddressIfBelowLimit(r.Context(), pushAddress, installationID, rec, s.quotas.AllocationsPerInstallation, now)
		if putErr != nil {
			s.writeErr(w, errInternal)
			return errInternal.status
		}
		if !created {
			s.writeErr(w, errAddressLimitReached)
			return errAddressLimitReached.status
		}
	} else if err := s.store.putAddress(pushAddress, rec); err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}

	return s.writeJSON(w, http.StatusCreated, allocateResponse{
		PushAddress:             pushAddress,
		SubmitCapability:        submitCap,
		MachineRevokeCapability: machineCap,
		UnboundExpiresAt:        unboundExpires.UTC().Format(time.RFC3339),
	})
}

// randomCapability mints 32 CSPRNG bytes, base64url unpadded (PG-AUTH-6/9).
func randomCapability() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
