package pushgw

import (
	"net/http"
	"strings"
	"time"
)

// tombstoneWindow is PG-RET-3's placement of the machine-revoke tombstone under the 7-day
// diagnostics row.
const tombstoneWindow = 7 * 24 * time.Hour

// handleRevoke implements DELETE /v1/addresses/{push_address} (spec section 3.4): revoke
// by the phone's installation-key signature ("forget this computer") or by the machine's
// machine-revoke capability ("revoke this phone").
//
// A GENUINE SPEC GAP, documented in revoke_test.go's file header, the RED-phase return
// summary, and escalated again in this round's GREEN return value: the route carries no
// installation_id segment, so once an address is gone there is no way left to resolve which
// installation's key an owner-arm RETRY should verify against. This implementation resolves
// the owner arm from the address's live binding (available for the ordinary case: delete a
// live address you signed for) and, when the address cannot be found at all, refuses 401
// rather than guess -- the same open question TestRevoke_OwnerSignature_RetryAfterAlreadyGone
// (skipped) leaves to an owner ruling. PG-REV-3 says both outcomes are 204 "for a valid
// installation signature"; this 401 is what the missing route segment forces, not a reading
// of PG-REV-3 this file endorses. Two fixes exist -- add installation_id to the route, or
// accept 401 here and amend PG-REV-3 -- and this document does not pick one.
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) int {
	pushAddress := strings.TrimPrefix(r.URL.Path, "/v1/addresses/")
	if pushAddress == "" || strings.Contains(pushAddress, "/") {
		s.writeErr(w, errUnauthorized)
		return errUnauthorized.status
	}

	if hasContentEncoding(r) {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}
	present, err := hasAnyBody(r)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if present {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}

	auth := r.Header.Get("Authorization")
	if token, ok := strings.CutPrefix(auth, "Swarm-Revoke "); ok {
		return s.revokeByMachineCapability(w, pushAddress, token)
	}
	return s.revokeByOwnerSignature(w, r, pushAddress)
}

func (s *Server) revokeByMachineCapability(w http.ResponseWriter, pushAddress, capability string) int {
	capHash := hashSecret(capability)
	now := s.now()

	rec, found, err := s.store.getAddress(pushAddress)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if found {
		if !verifierEquals(rec.MachineRevokeHash, capHash) {
			s.writeErr(w, errUnauthorized)
			return errUnauthorized.status
		}
		// Delete and tombstone in ONE bbolt transaction (PG-REV-2, PG-RET-3): two separate
		// writes here used to let a crash or a write error between them leave the address
		// gone with no tombstone, which turned every later durable retry into a permanent
		// 401 instead of the idempotent 204 the tombstone exists to guarantee.
		if err := s.store.deleteAddressAndTombstone(pushAddress, rec, now.UnixMilli()); err != nil {
			s.writeErr(w, errInternal)
			return errInternal.status
		}
		w.WriteHeader(http.StatusNoContent)
		return http.StatusNoContent
	}

	// PG-REV-2: the address is already gone. A durable retry across a process exit must
	// still see 204 if it presents the same verifier the tombstone was keyed by and the
	// tombstone has not aged past its 7-day window. No comparison is needed beyond the
	// bucket lookup itself: capHash IS the key, so a hit already proves the presented
	// capability is the one that was revoked.
	tomb, found, err := s.store.getTombstone(capHash)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if found && now.UnixMilli()-tomb.RevokedAtMs <= tombstoneWindow.Milliseconds() {
		w.WriteHeader(http.StatusNoContent)
		return http.StatusNoContent
	}
	s.writeErr(w, errUnauthorized)
	return errUnauthorized.status
}

func (s *Server) revokeByOwnerSignature(w http.ResponseWriter, r *http.Request, pushAddress string) int {
	rec, found, err := s.store.getAddress(pushAddress)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if !found {
		// No binding survives to resolve an installation against -- see this file's
		// header comment. 401 is the safe default: unauthorized is deliberately
		// non-discriminating (section 4) and the alternative, 204, would need the
		// caller's identity to have been proven, which nothing here can do anymore.
		s.writeErr(w, errUnauthorized)
		return errUnauthorized.status
	}
	inst, found, err := s.store.getInstallation(rec.InstallationID)
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
	path := "/v1/addresses/" + pushAddress
	outcome := s.verifyInstallationSignature(r, r.Method, path, []byte{}, pub)
	if outcome.err != nil {
		s.writeErr(w, *outcome.err)
		return outcome.err.status
	}
	now := s.now()
	if !s.nonces.checkAndStore(rec.InstallationID, outcome.ok.nonce, now, outcome.ok.expiry) {
		s.writeErr(w, errNonceReplayed)
		return errNonceReplayed.status
	}
	if err := s.store.touchInstallation(rec.InstallationID, now.UnixMilli()); err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	// PG-REV-2: idempotency is required for BOTH credential kinds. The owner's own retry
	// is free (the installation key outlives the address), but a machine holding this
	// address's machine-revoke capability may retry ITS revoke later and must still see
	// 204 -- regardless of which credential destroyed the address first. Tombstone and
	// delete in ONE bbolt transaction (keyed by the verifier hash already on rec, not the
	// address -- see storage.go's comment), so that later retry is idempotent too, and so a
	// crash or write error between the two effects can never leave one without the other.
	if err := s.store.deleteAddressAndTombstone(pushAddress, rec, now.UnixMilli()); err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	w.WriteHeader(http.StatusNoContent)
	return http.StatusNoContent
}
