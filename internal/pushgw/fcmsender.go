package pushgw

import (
	"context"
	"errors"
	"strings"

	"github.com/Nathandela/swarm/internal/remote/push"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// WakeSender is the FCM leg's seam (spec section 3.5, ADR-015 P2). The gateway never
// constructs a push.FCM directly in a handler -- every submit-wake path goes through this
// interface, so the conformance suite can swap in a fake and never dial Google.
type WakeSender interface {
	Send(ctx context.Context, fcmToken string, envelope []byte) error
}

// AttestationVerifier is Play Integrity's seam (PG-AUTH-11). Verify is handed the
// verdict token from a registration request and returns what the gateway must
// independently check the request against.
type AttestationVerifier interface {
	Verify(ctx context.Context, verdictToken string) (VerdictBinding, error)
}

// VerdictBinding is what an AttestationVerifier concludes about one verdict token:
// the requestHash it was bound to (PG-AUTH-11) and whether it names the licensed
// Play-signed build.
type VerdictBinding struct {
	RequestHash   [32]byte
	LicensedBuild bool
}

// fcmSenderAdapter adapts the real internal/remote/push.FCM sender (ADR-015 P2's
// relocated asset) to WakeSender. It is the only place in this package that imports
// internal/remote/push or internal/remote/relay.
type fcmSenderAdapter struct {
	fcm *push.FCM
}

// NewFCMSender wraps the real FCM v1 sender for use as a WakeSender. fcm is never nil in
// production wiring (cmd/swarm-pushgw); the conformance suite uses this exactly once
// (fcmwiring_test.go), against a loopback fake FCM endpoint, never Google.
func NewFCMSender(fcm *push.FCM) WakeSender {
	return &fcmSenderAdapter{fcm: fcm}
}

// Send forwards envelope unchanged (PG-SUB-1): relay.PushPayload.Ciphertext carries the
// exact 74 received octets, with no Alert (a wake is content-free by construction).
func (a *fcmSenderAdapter) Send(ctx context.Context, fcmToken string, envelope []byte) error {
	err := a.fcm.Push(ctx, fcmToken, relay.PushPayload{Ciphertext: envelope})
	if err == nil {
		return nil
	}
	if errors.Is(err, relay.ErrPushUnregistered) {
		return ErrUnregistered
	}
	// push.FCM already exhausts its own bounded retry budget for retryable failures
	// (5xx, transport errors) before returning to us; the only way its wrapped error
	// carries "giving up after" is that internal exhaustion, so surface it as
	// upstream_unavailable (still worth an obligation-side retry).
	if strings.Contains(err.Error(), "giving up after") {
		return ErrUnavailable
	}
	// Section 4 scopes upstream_refused strictly to "FCM refused with a non-retryable 4xx
	// that is not UNREGISTERED" -- and §6.4 routes it to the terminal `abandoned` state, so
	// misclassifying anything else here permanently kills a wake obligation FCM never
	// refused. classify()'s "provider refused with %d" (fcm.go:118) is the ONE shape that
	// is an affirmatively-identified non-retryable Google refusal; match on it specifically
	// rather than treating it as the default. Everything else that can escape Push --
	// ctx.Err() on cancellation or deadline (fcm.go:108-110, :116-118), a marshal fault
	// (fcm.go:103-105), or a request-build fault -- is a gateway-side or transport fault
	// with no confirmed refusal behind it, and falls through to the caller's default arm
	// (`internal`, 500, retryable) by returning the error unchanged.
	if strings.Contains(err.Error(), "provider refused with") {
		return ErrRefused
	}
	return err
}
