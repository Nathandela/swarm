package pushgw_test

// PUT /v1/installations/{id}/token (spec §3.2). Covers PG-ROT-1..2 and, using this
// operation as the representative installation-signed request, the PG-AUTH-1..4 auth
// matrix (wrong key, replayed nonce, expired signature) and the 8 KiB body cap.
//
// RED: package pushgw has no implementation yet; every pushgw.* reference below is an
// undefined symbol.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
)

func rotateBody(token string) []byte {
	b, _ := json.Marshal(map[string]any{"fcm_token": token})
	return b
}

// TestRotateToken_HappyPath_Returns204.
func TestRotateToken_HappyPath_Returns204(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-v1")
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-v2")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body})
	resp := h.doJSON("PUT", path, body, headers)
	requireStatus(t, resp, http.StatusNoContent)
}

// TestRotateToken_SameTokenIsIdempotentRefresh: submitting the CURRENT token again is the
// app's periodic inactivity refresh (PG-AUTH-5), and must succeed identically.
func TestRotateToken_SameTokenIsIdempotentRefresh(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-same")
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-same")
	for i := 0; i < 2; i++ {
		headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body})
		resp := h.doJSON("PUT", path, body, headers)
		requireStatus(t, resp, http.StatusNoContent)
	}
}

// TestRotateToken_DoesNotTouchAddresses is PG-ROT-1: an address's push_address, submit
// capability and machine-revoke capability all survive a token rotation unchanged.
func TestRotateToken_DoesNotTouchAddresses(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-pre-rotate")
	a := allocateAddress(t, h, r)

	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-post-rotate")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body})
	requireStatus(t, h.doJSON("PUT", path, body, headers), http.StatusNoContent)

	// The address's own capability must still authenticate a wake after rotation — no
	// re-pairing required (PG-ROT-1's whole point).
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, resp, http.StatusOK)
}

// TestRotateToken_UnregisteredThenRestoredByRotation is PG-ROT-2: FCM's UNREGISTERED
// verdict kills the token (submits refused push_token_unregistered) while the address and
// its verifiers survive; a fresh rotation restores delivery without re-pairing.
func TestRotateToken_UnregisteredThenRestoredByRotation(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-dies")
	a := allocateAddress(t, h, r)

	h.sender.setBehavior(func(string, []byte) error { return pushgw.ErrUnregistered })
	env1 := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, a.submitCapability, env1)
	requireStatus(t, resp, http.StatusGone)
	if e := decodeError(t, resp); e.Code != "push_token_unregistered" || e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want push_token_unregistered/false", e.Code, e.Retryable)
	}

	// A second wake attempt against the still-dead mapping is refused the same way,
	// without a rotation — the address survives, the token does not.
	env2 := buildWakeV1(t, decodeAddr(t, a.pushAddress), 2, h.clock.Now())
	resp2 := submitTestWake(h, a.submitCapability, env2)
	requireStatus(t, resp2, http.StatusGone)

	// Rotate: delivery is restored with NO new allocation and NO new capability.
	h.sender.setBehavior(nil)
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-reborn")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body})
	requireStatus(t, h.doJSON("PUT", path, body, headers), http.StatusNoContent)

	env3 := buildWakeV1(t, decodeAddr(t, a.pushAddress), 3, h.clock.Now())
	resp3 := submitTestWake(h, a.submitCapability, env3)
	requireStatus(t, resp3, http.StatusOK)
}

// TestRotateToken_WrongKey_Returns401Unauthorized: signed by a key that never registered
// as this installation.
func TestRotateToken_WrongKey_Returns401Unauthorized(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wrongkey")
	impostor, _ := genInstallationKey(t)
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-attacker")
	headers, _, _ := sign(t, impostor, h.clock.Now(), signParams{method: "PUT", path: path, body: body})
	resp := h.doJSON("PUT", path, body, headers)
	requireStatus(t, resp, http.StatusUnauthorized)
	if e := decodeError(t, resp); e.Code != "unauthorized" {
		t.Fatalf("code = %q, want unauthorized", e.Code)
	}
}

// TestRotateToken_ReplayedNonce_Returns409NonceReplayed is PG-AUTH-4: the identical
// (installation_id, nonce) pair presented twice.
func TestRotateToken_ReplayedNonce_Returns409NonceReplayed(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-replay")
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-replay-2")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body})

	first := h.doJSON("PUT", path, body, headers)
	requireStatus(t, first, http.StatusNoContent)

	second := h.doJSON("PUT", path, body, headers)
	requireStatus(t, second, http.StatusConflict)
	if e := decodeError(t, second); e.Code != "nonce_replayed" || e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want nonce_replayed/false", e.Code, e.Retryable)
	}
}

// TestRotateToken_ExpiredSignature_Returns401WithServerTime is PG-AUTH-3: an expiry more
// than 120s in the past. This is the ONE code that carries server_time.
func TestRotateToken_ExpiredSignature_Returns401WithServerTime(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-expired")
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-expired-2")
	past := h.clock.Now().Add(-121 * time.Second).Unix()
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body, expiry: past})
	resp := h.doJSON("PUT", path, body, headers)
	requireStatus(t, resp, http.StatusUnauthorized)
	e := decodeError(t, resp)
	if e.Code != "request_expired" || e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want request_expired/false", e.Code, e.Retryable)
	}
	if e.ServerTime == nil || *e.ServerTime == "" {
		t.Fatalf("request_expired must carry server_time so a skewed client can correct")
	}
}

// TestRotateToken_ExpiryTooFarInFuture_Returns401RequestExpired: PG-AUTH-3's other edge,
// more than 120s ahead of server_time.
func TestRotateToken_ExpiryTooFarInFuture_Returns401RequestExpired(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-future")
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-future-2")
	future := h.clock.Now().Add(121 * time.Second).Unix()
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body, expiry: future})
	resp := h.doJSON("PUT", path, body, headers)
	requireStatus(t, resp, http.StatusUnauthorized)
	if e := decodeError(t, resp); e.Code != "request_expired" {
		t.Fatalf("code = %q, want request_expired", e.Code)
	}
}

// TestRotateToken_QueryString_Returns400MalformedRequestBeforeVerification: PG-AUTH-1
// bans a query string on any signed operation, refused BEFORE signature verification —
// so even a well-formed signature over the pathless canonical string doesn't save it.
func TestRotateToken_QueryString_Returns400MalformedRequestBeforeVerification(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-query")
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-query-2")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body})
	resp := h.doJSON("PUT", path+"?a=b", body, headers)
	requireStatus(t, resp, http.StatusBadRequest)
	if e := decodeError(t, resp); e.Code != "malformed_request" {
		t.Fatalf("code = %q, want malformed_request", e.Code)
	}
}

// TestRotateToken_BodyOverEightKiB_Returns413.
func TestRotateToken_BodyOverEightKiB_Returns413(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-oversize")
	path := "/v1/installations/" + r.installationID + "/token"
	oversized := bytes.Repeat([]byte("y"), 8*1024+1)
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: oversized})
	resp := h.doJSON("PUT", path, oversized, headers)
	requireStatus(t, resp, http.StatusRequestEntityTooLarge)
	if e := decodeError(t, resp); e.Code != "body_too_large" {
		t.Fatalf("code = %q, want body_too_large", e.Code)
	}
}

// TestRotateToken_MissingSignature_Returns401.
func TestRotateToken_MissingSignature_Returns401(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-nosig")
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-nosig-2")
	resp := h.doJSON("PUT", path, body, nil)
	requireStatus(t, resp, http.StatusUnauthorized)
}

// TestRotateToken_MalformedNonce_Returns401 is PG-AUTH-1's unambiguity argument, enforced
// rather than merely asserted: Swarm-Nonce must be 16 CSPRNG bytes, base64url unpadded (the
// same alphabet as Idempotency-Key). A caller-supplied value outside that shape -- here, one
// carrying the "|" the canonical string's separator relies on no component containing -- is
// refused before it is ever used as a canonical-string component or nonce-cache key.
func TestRotateToken_MalformedNonce_Returns401(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-badnonce")
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-badnonce-2")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body, nonce: "not-the-right-shape|="})
	resp := h.doJSON("PUT", path, body, headers)
	requireStatus(t, resp, http.StatusUnauthorized)
}
