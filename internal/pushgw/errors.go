package pushgw

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Sentinel errors a WakeSender returns (spec table PG-ERR, section 4). The gateway
// translates these into the wire error codes; no other error value from a WakeSender is
// given special treatment -- an unrecognized error becomes `internal` (500, retryable).
var (
	ErrUnregistered = errors.New("pushgw: fcm token no longer registered")
	ErrUnavailable  = errors.New("pushgw: fcm provider unavailable")
	ErrRefused      = errors.New("pushgw: fcm provider refused the request")
)

// ErrAttestationUnavailable is returned by an AttestationVerifier when Google's
// verification endpoint could not be reached -- distinct from an invalid verdict
// (spec PG-ERR table: attestation_unavailable is retryable, attestation_invalid is not).
var ErrAttestationUnavailable = errors.New("pushgw: attestation verification unavailable")

// wireError is the wire shape of components.schemas.Error (spec section 3.6). The JSON
// wire contract is what section 4 makes normative, not this type's name.
type wireError struct {
	Code              string  `json:"code"`
	Message           string  `json:"message"`
	Retryable         bool    `json:"retryable"`
	RetryAfterSeconds *int    `json:"retry_after_seconds,omitempty"`
	ServerTime        *string `json:"server_time,omitempty"`
}

// errSpec fully describes one refusal: everything writeError needs and nothing an error
// body may ever carry (PG-ERR-2 -- never a token, capability, envelope, signature or
// attestation token; the message strings below are all static).
type errSpec struct {
	status     int
	code       string
	message    string
	retryable  bool
	retryAfter int  // seconds; 0 means "no Retry-After header"
	serverTime bool // true only for request_expired (PG-AUTH-3)
}

func (s *Server) writeErr(w http.ResponseWriter, spec errSpec) {
	w.Header().Set("Content-Type", "application/json")
	if spec.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(spec.retryAfter))
	}
	w.WriteHeader(spec.status)
	body := wireError{Code: spec.code, Message: spec.message, Retryable: spec.retryable}
	if spec.retryAfter > 0 {
		n := spec.retryAfter
		body.RetryAfterSeconds = &n
	}
	if spec.serverTime {
		t := s.now().UTC().Format(time.RFC3339)
		body.ServerTime = &t
	}
	_ = json.NewEncoder(w).Encode(body)
}

// Named refusals, one per row of section 4 this implementation reaches. Grouping them
// here keeps every status/code/retryable triple in one place rather than scattered
// through the handlers as string literals a typo could silently diverge.
//
// `address_revoked` (410) is in the closed vocabulary but deliberately absent below:
// PG-REV-1's whole-address deletion destroys the submit-capability verifier along with
// everything else, so a wake against a revoked address never reaches a code path that
// still knows the address once verified -- it is `unauthorized`, same as any other unknown
// capability (revoke_test.go documents this as its suite's primary reading of section 4).
//
// RECORDED DEVIATION, escalated rather than silently carried: this makes 410
// address_revoked UNREACHABLE on POST /v1/wakes as implemented, even though section 3.5's
// responses map declares it, section 4's row says it is "only reachable by a caller
// presenting a capability that once verified," and section 6.4 carries an
// `in_flight | 410 address_revoked | abandoned` transition that can now never fire.
// Behaviour coincides with the 401 path today (both route the obligation machine to
// `abandoned`), but the spec deliberately distinguished the two codes so a machine can
// surface the right repair. Closing it for real needs a submit-side revocation tombstone --
// which collides with PG-REV-1's "whole-address, nothing survives" rule and PG-RET-2's
// actual-deletion requirement -- or an amendment to section 3.5/section 4/section 6.4. This
// document does not pick one; see the return value for the escalation.
//
// `service_unavailable` (503) is also absent, for a different and simpler reason: this
// gateway has no load-shedding or maintenance-mode path -- every request that reaches
// ServeHTTP is served or refused on its own merits, never turned away to protect gateway
// capacity -- so the code is correctly unreachable today, not merely unimplemented.
var (
	errVersionUnsupported     = errSpec{http.StatusNotFound, "version_unsupported", "unknown API version", false, 0, false}
	errMalformedRequest       = errSpec{http.StatusBadRequest, "malformed_request", "the request could not be parsed", false, 0, false}
	errWakeMalformed          = errSpec{http.StatusBadRequest, "wake_malformed", "the wake body is not a valid WakeV1 envelope", false, 0, false}
	errUnauthorized           = errSpec{http.StatusUnauthorized, "unauthorized", "the credential presented was not accepted", false, 0, false}
	errNonceReplayed          = errSpec{http.StatusConflict, "nonce_replayed", "this request nonce has already been used", false, 0, false}
	errIdempotencyConflict    = errSpec{http.StatusConflict, "idempotency_conflict", "this idempotency key was already used with a different body", false, 0, false}
	errRegistrationInProgress = errSpec{http.StatusServiceUnavailable, "service_unavailable", "registration with this idempotency key is in progress", true, 1, false}
	errAttestationInvalid     = errSpec{http.StatusForbidden, "attestation_invalid", "app attestation did not verify", false, 0, false}
	errAttestationUnavailable = errSpec{http.StatusForbidden, "attestation_unavailable", "attestation verification is temporarily unavailable", true, 0, false}
	errBetaClosed             = errSpec{http.StatusForbidden, "beta_closed", "registration is not enabled for this installation", false, 0, false}
	errAddressLimitReached    = errSpec{http.StatusConflict, "address_limit_reached", "this installation has reached its allocation limit", false, 0, false}
	errPushTokenUnregistered  = errSpec{http.StatusGone, "push_token_unregistered", "the provider reports this token is no longer registered", false, 0, false}
	errUpstreamUnavailable    = errSpec{http.StatusBadGateway, "upstream_unavailable", "the push provider is temporarily unavailable", true, 0, false}
	errUpstreamRefused        = errSpec{http.StatusBadGateway, "upstream_refused", "the push provider refused the request", false, 0, false}
	errInternal               = errSpec{http.StatusInternalServerError, "internal", "an unclassified gateway fault occurred", true, 0, false}
)

func errBodyTooLarge() errSpec {
	return errSpec{http.StatusRequestEntityTooLarge, "body_too_large", "the request body exceeds the bound for this operation", false, 0, false}
}

func errRequestExpired() errSpec {
	return errSpec{http.StatusUnauthorized, "request_expired", "the signed request has expired", false, 0, true}
}

func errQuotaExceeded(retryAfterSeconds int) errSpec {
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	return errSpec{http.StatusTooManyRequests, "quota_exceeded", "quota exceeded for this operation", true, retryAfterSeconds, false}
}
