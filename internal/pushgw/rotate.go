package pushgw

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// rotateBodyMax is PG-TR-3's 8 KiB row.
const rotateBodyMax = 8 * 1024

type rotateRequest struct {
	FCMToken string `json:"fcm_token"`
}

// handleRotate implements PUT /v1/installations/{id}/token (spec section 3.2).
func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) int {
	installationID, ok := pathSegment(r.URL.Path, "/v1/installations/", "/token")
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
	body, tooLarge, err := readBounded(r, rotateBodyMax)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if tooLarge {
		spec := errBodyTooLarge()
		s.writeErr(w, spec)
		return spec.status
	}

	inst, found, err := s.store.getInstallation(installationID)
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
	path := "/v1/installations/" + installationID + "/token"
	outcome := s.verifyInstallationSignature(r, r.Method, path, body, pub)
	if outcome.err != nil {
		s.writeErr(w, *outcome.err)
		return outcome.err.status
	}
	now := s.now()
	if !s.nonces.checkAndStore(installationID, outcome.ok.nonce, now, outcome.ok.expiry) {
		s.writeErr(w, errNonceReplayed)
		return errNonceReplayed.status
	}
	// PG-AUTH-5: the inactivity clock resets on ANY successfully authenticated request,
	// unconditionally -- written to the store immediately, exactly like allocate.go's
	// touchInstallation call, so a LATER refusal in this same handler (an unknown-field
	// body, an empty fcm_token) does not discard the reset.
	if err := s.store.touchInstallation(installationID, now.UnixMilli()); err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}

	var req rotateRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.FCMToken == "" {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}

	tokenEnc, err := s.store.encrypt(req.FCMToken)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	// Mutate the CURRENT record inside one bbolt transaction rather than blind-writing the
	// `inst` snapshot loaded above: a read-modify-write against that stale snapshot could
	// resurrect an installation the 180-day inactivity sweep deleted between this handler's
	// read and its write -- token and all, minus its addresses. updateInstallationIfPresent
	// is a no-op, not a re-creation, if the row is already gone.
	updated, err := s.store.updateInstallationIfPresent(installationID, func(rec *installationRecord) {
		rec.FCMTokenEnc = tokenEnc
		// A fresh token clears PG-ROT-2's dead-mapping marker: this IS the operation that
		// restores delivery after an UNREGISTERED verdict.
		rec.TokenDead = false
	})
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if !updated {
		s.writeErr(w, errUnauthorized)
		return errUnauthorized.status
	}

	w.WriteHeader(http.StatusNoContent)
	return http.StatusNoContent
}

// pathSegment extracts the path segment between prefix and suffix, refusing an empty or
// slash-containing result (which would mean the path did not actually match the shape the
// caller expects).
func pathSegment(path, prefix, suffix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	seg := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if seg == "" || strings.Contains(seg, "/") {
		return "", false
	}
	return seg, true
}
