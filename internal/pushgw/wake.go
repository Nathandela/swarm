package pushgw

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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

	rec, found, err := s.store.getAddress(pushAddress)
	if err != nil {
		s.writeErr(w, errInternal)
		return errInternal.status
	}
	if !found || capability == "" || !verifierEquals(rec.SubmitCapHash, hashSecret(capability)) {
		s.writeErr(w, errUnauthorized)
		return errUnauthorized.status
	}

	now := s.now()
	// PG-ALLOC-2 / §8.1 row 1, enforced AT USE, not only by the lazy retention sweep:
	// RunRetention runs on an operator-configured timer (an hour, by default, in
	// cmd/swarm-pushgw), so a wake landing between the ten-minute unbound deadline and the
	// next sweep must not be able to bind an allocation the spec already requires deleted --
	// that would let the address, and both its capabilities, live indefinitely. Mirrors
	// revokeByMachineCapability's own at-use window check (revoke.go) for the same reason:
	// a durable stored deadline is only real if every reader of the record enforces it, not
	// only the sweep that eventually deletes it.
	if !rec.Bound && now.UnixMilli() > rec.UnboundExpiresMs {
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
