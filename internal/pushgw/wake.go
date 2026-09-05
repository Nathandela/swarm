package pushgw

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// wakeSize is PG-WAKE-2's pinned constant: WakeV1 is exactly 74 bytes.
const wakeSize = 74

// wakeVersion and wakeType are the two bytes PG-WAKE-3 requires checked, before any AEAD
// is touched -- which the gateway never touches at all (it holds no wake key, section 7.1).
const (
	wakeVersion byte = 0x01
	wakeType    byte = 0x03
)

type wakeResponse struct {
	Status string `json:"status"`
}

// handleWake implements POST /v1/wakes (spec section 3.5).
func (s *Server) handleWake(w http.ResponseWriter, r *http.Request) int {
	if hasContentEncoding(r) {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}
	if !hasExactContentType(r, "application/octet-stream") {
		s.writeErr(w, errMalformedRequest)
		return errMalformedRequest.status
	}

	envelope, malformed, err := readWakeBody(r)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if malformed {
		s.writeErr(w, errWakeMalformed)
		return errWakeMalformed.status
	}
	if envelope[0] != wakeVersion || envelope[1] != wakeType {
		s.writeErr(w, errWakeMalformed)
		return errWakeMalformed.status
	}

	pushAddress := encodeAddress(envelope[2:18])
	// PG-AUTH-6 fixes the wire form as "Authorization: Swarm-Capability <value>"; the ok
	// flag is load-bearing, not decorative -- discarding it (as this line's earlier version
	// did) let a bare, unscoped Authorization value authenticate a wake, an accidental wire
	// contract nobody specified.
	capability, hasScheme := strings.CutPrefix(r.Header.Get("Authorization"), "Swarm-Capability ")
	if !hasScheme {
		capability = ""
	}

	rec, found, err := s.getAddress(r.Context(), pushAddress)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if !found || capability == "" || !verifierEquals(rec.SubmitCapHash, hashSecret(capability)) {
		s.writeErr(w, errUnauthorized)
		return errUnauthorized.status
	}

	now := s.now()
	if s.v2store != nil {
		return s.handleWakeV2(w, r, envelope, pushAddress, rec, capability, now)
	}
	// PG-ALLOC-2 / §8.1 row 1, enforced AT USE, not only by the lazy retention sweep:
	// RunRetention runs on an operator-configured timer (an hour, by default, in
	// cmd/swarm-pushgw), so a wake landing between the ten-minute unbound deadline and the
	// next sweep must not be able to bind an allocation the spec already requires deleted --
	// that would let the address, and both its capabilities, live indefinitely. Mirrors
	// revokeByMachineCapability's own at-use window check (revoke.go) for the same reason:
	// a durable stored deadline is only real if every reader of the record enforces it, not
	// only the sweep that eventually deletes it.
	if !rec.Bound && now.UnixMilli() >= rec.UnboundExpiresMs {
		s.writeErr(w, errUnauthorized)
		return errUnauthorized.status
	}
	if ok, retryAfter := s.limiter.allow("wake-addr:"+pushAddress, s.quotas.WakesPerAddress, now); !ok {
		spec := errQuotaExceeded(retryAfter)
		s.writeErr(w, spec)
		return spec.status
	}
	if ok, retryAfter := s.limiter.allow("wake-src:"+s.sourceIP(r), s.quotas.WakesPerSourceIP, now); !ok {
		spec := errQuotaExceeded(retryAfter)
		s.writeErr(w, spec)
		return spec.status
	}

	// PG-SUB-4: a byte-identical retry may be answered from the idempotency cache
	// without a second send.
	idemKey := sha256.Sum256(envelope)
	if e, ok := s.wakeIdem.get(idemKey, now); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(e.status)
		_, _ = w.Write(e.body)
		return e.status
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
	// PG-ROT-2 / PG-RET-2: an UNREGISTERED verdict already deleted the token bytes and
	// marked the mapping dead (see the ErrUnregistered case below). Subsequent submits are
	// refused the same way, without a second provider call -- the address survives so a
	// later rotation (§3.2) restores delivery.
	if inst.TokenDead {
		s.writeErr(w, errPushTokenUnregistered)
		return errPushTokenUnregistered.status
	}
	fcmToken, err := s.store.decrypt(inst.FCMTokenEnc)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	// Captured before the send so an ErrUnregistered verdict below can be checked against
	// what was actually attempted (PG-ROT-2 race guard) rather than whatever the record
	// holds by the time the response arrives.
	sentTokenEnc := inst.FCMTokenEnc

	sendErr := s.sender.Send(r.Context(), fcmToken, envelope)
	s.recordProviderOutcome(sendErr)
	switch {
	case sendErr == nil:
		if !rec.Bound {
			if err := s.store.markAddressBound(pushAddress); err != nil {
				s.writeErr(w, errInternal)
				return errInternal.status
			}
		}
		body, _ := json.Marshal(wakeResponse{Status: "provider_accepted"})
		s.wakeIdem.put(idemKey, wakeIdemEntry{status: http.StatusOK, body: body}, now)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return http.StatusOK
	case errors.Is(sendErr, ErrUnregistered):
		// PG-ROT-2: mark the mapping dead and delete the token bytes -- not optional
		// tidiness (PG-RET-2 forbids a tombstone that retains them). The address and its
		// verifiers survive; the dead-mapping marker is what the next PUT .../token fills.
		// Compare-and-set against sentTokenEnc: if the installation has already rotated to
		// a different token since this send began, this stale verdict must not kill it.
		if err := s.store.markInstallationTokenDeadIfCurrent(rec.InstallationID, sentTokenEnc); err != nil {
			s.writeErr(w, errInternal)
			return errInternal.status
		}
		s.writeErr(w, errPushTokenUnregistered)
		return errPushTokenUnregistered.status
	case errors.Is(sendErr, ErrUnavailable):
		s.writeErr(w, errUpstreamUnavailable)
		return errUpstreamUnavailable.status
	case errors.Is(sendErr, ErrRefused):
		s.writeErr(w, errUpstreamRefused)
		return errUpstreamRefused.status
	default:
		s.writeErr(w, errInternal)
		return errInternal.status
	}
}

func (s *Server) handleWakeV2(w http.ResponseWriter, r *http.Request, envelope []byte, pushAddress string, rec addressRecord, capability string, now time.Time) int {
	issuedAtMs := int64(binary.BigEndian.Uint64(envelope[26:34]))
	issuedAt := time.UnixMilli(issuedAtMs)
	deadline := issuedAt.Add(wakeWindow)
	digest := sha256.Sum256(envelope)
	attemptID := hex.EncodeToString(digest[:])
	leaseBytes := make([]byte, 16)
	if _, err := rand.Read(leaseBytes); err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	leaseID := hex.EncodeToString(leaseBytes)
	claimStarted := time.Now()
	claim, claimed, err := s.v2store.p.claimWake(r.Context(), attemptID, leaseID, pushAddress, hashSecret(capability), now, deadline, s.quotas.WakesPerAddress, "wake-src:"+s.sourceIP(r), s.quotas.WakesPerSourceIP)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if claim.Completed {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(claim.Status)
		_, _ = w.Write(claim.Body)
		return claim.Status
	}
	if !claimed {
		switch claim.Denied {
		case "malformed":
			s.writeErr(w, errWakeMalformed)
			return errWakeMalformed.status
		case "unauthorized":
			s.writeErr(w, errUnauthorized)
			return errUnauthorized.status
		case "token_dead":
			s.writeErr(w, errPushTokenUnregistered)
			return errPushTokenUnregistered.status
		case "quota":
			spec := errQuotaExceeded(claim.RetryAfter)
			s.writeErr(w, spec)
			return spec.status
		default:
			s.writeErr(w, errUpstreamUnavailable)
			return errUpstreamUnavailable.status
		}
	}
	fcmToken, err := s.decryptToken(claim.TokenEnc, claim.KeyVersion)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	// Provider I/O is deliberately outside Firestore's retryable callback. A lease takeover
	// may repeat these exact bytes after an acceptance/commit crash; attempts and the original
	// five-minute deadline remain bounded in shared state.
	// ProviderBudget is derived from Firestore's server ReadTime. Subtract the
	// entire local monotonic claim round trip so no process-wall-clock skew can
	// extend either the durable lease or the original wake obligation.
	remaining := claim.ProviderBudget - time.Since(claimStarted)
	if remaining <= 0 {
		s.writeErr(w, errUpstreamUnavailable)
		return errUpstreamUnavailable.status
	}
	operationCtx, cancelOperation := context.WithTimeout(r.Context(), remaining)
	defer cancelOperation()
	sendErr := s.sender.Send(operationCtx, fcmToken, envelope)
	s.recordProviderOutcome(sendErr)
	statusCode := http.StatusServiceUnavailable
	var responseBody []byte
	unregistered := false
	switch {
	case sendErr == nil:
		statusCode = http.StatusOK
		responseBody, _ = json.Marshal(wakeResponse{Status: "provider_accepted"})
	case errors.Is(sendErr, ErrUnregistered):
		statusCode, unregistered = errPushTokenUnregistered.status, true
		responseBody, _ = json.Marshal(wireError{Code: errPushTokenUnregistered.code, Message: errPushTokenUnregistered.message, Retryable: errPushTokenUnregistered.retryable})
	case errors.Is(sendErr, ErrUnavailable):
		statusCode = http.StatusServiceUnavailable
	case errors.Is(sendErr, ErrRefused):
		statusCode = errUpstreamRefused.status
		responseBody, _ = json.Marshal(wireError{Code: errUpstreamRefused.code, Message: errUpstreamRefused.message, Retryable: errUpstreamRefused.retryable})
	default:
		responseBody, _ = json.Marshal(wireError{Code: errInternal.code, Message: errInternal.message, Retryable: errInternal.retryable})
	}
	staleUnregistered, err := s.v2store.p.completeWake(operationCtx, attemptID, leaseID, claim.TokenGeneration, statusCode, responseBody, unregistered, s.now())
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if staleUnregistered {
		s.writeErr(w, errPushTokenUnregistered)
		return errPushTokenUnregistered.status
	}
	if sendErr == nil {
		// This reports the provider's acceptance, not a phone receipt or a late
		// durable binding. completeWake deliberately ignores expired completions.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responseBody)
		return http.StatusOK
	}
	if errors.Is(sendErr, ErrUnregistered) {
		s.writeErr(w, errPushTokenUnregistered)
		return errPushTokenUnregistered.status
	}
	if errors.Is(sendErr, ErrUnavailable) {
		s.writeErr(w, errUpstreamUnavailable)
		return errUpstreamUnavailable.status
	}
	if errors.Is(sendErr, ErrRefused) {
		s.writeErr(w, errUpstreamRefused)
		return errUpstreamRefused.status
	}
	s.writeErr(w, errInternal)
	return errInternal.status
}

// readWakeBody enforces PG-TR-3's wake row: a declared Content-Length other than 74 is
// refused before reading, and an absent or unusable length is read through a reader
// hard-limited to 75 octets so no oversized body is ever buffered.
func readWakeBody(r *http.Request) (envelope []byte, malformed bool, err error) {
	if r.ContentLength >= 0 && r.ContentLength != wakeSize {
		return nil, true, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, wakeSize+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) != wakeSize {
		return nil, true, nil
	}
	return data, false, nil
}

func encodeAddress(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}
