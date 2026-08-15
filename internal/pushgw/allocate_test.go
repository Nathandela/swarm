package pushgw_test

// POST /v1/installations/{id}/addresses (spec §3.3). Covers PG-ALLOC-1, PG-ALLOC-4, the
// 1 KiB body cap, and ADR-018 MM5's N-independent-allocations-per-installation invariant.
// PG-ALLOC-2's ten-minute unbound sweep is exercised on the fake clock in
// retention_test.go, not here.
//
// RED: package pushgw has no implementation yet; every pushgw.* reference below is an
// undefined symbol.

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
)

// TestAllocate_HappyPath_ReturnsUnboundTripleWithTenMinuteWindow.
func TestAllocate_HappyPath_ReturnsUnboundTripleWithTenMinuteWindow(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-alloc")
	a := allocateAddress(t, h, r)
	deadline, err := time.Parse(time.RFC3339, a.unboundExpiresAt)
	if err != nil {
		t.Fatalf("unbound_expires_at %q is not RFC3339: %v", a.unboundExpiresAt, err)
	}
	got := deadline.Sub(h.clock.Now())
	if got != 10*time.Minute {
		t.Fatalf("unbound_expires_at is issued+%s, want issued+10m", got)
	}
}

// TestAllocate_WirePatterns_LengthsMatchSpec is §3.6's normative wire-shape patterns,
// unpinned before this: InstallationId/PushAddress are 16 opaque bytes, base64url unpadded
// -> 22 chars (`^[A-Za-z0-9_-]{22}$`); the two capabilities are 32 CSPRNG bytes, base64url
// unpadded -> 43 chars.
func TestAllocate_WirePatterns_LengthsMatchSpec(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wireshape")
	if len(r.installationID) != 22 {
		t.Fatalf("installation_id length = %d, want 22 (§3.6 InstallationId pattern)", len(r.installationID))
	}
	a := allocateAddress(t, h, r)
	if len(a.pushAddress) != 22 {
		t.Fatalf("push_address length = %d, want 22 (§3.6 PushAddress pattern)", len(a.pushAddress))
	}
	if len(a.submitCapability) != 43 {
		t.Fatalf("submit_capability length = %d, want 43 (32-byte capability, base64url unpadded)", len(a.submitCapability))
	}
	if len(a.machineRevokeCapability) != 43 {
		t.Fatalf("machine_revoke_capability length = %d, want 43 (32-byte capability, base64url unpadded)", len(a.machineRevokeCapability))
	}
}

// TestAllocate_TwoAllocationsAreFullyIndependent is ADR-018 MM5: no object is shared
// between two pairings of the same installation.
func TestAllocate_TwoAllocationsAreFullyIndependent(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-mm5")
	first := allocateAddress(t, h, r)
	second := allocateAddress(t, h, r)
	if first.pushAddress == second.pushAddress {
		t.Fatalf("two allocations shared a push_address")
	}
	if first.submitCapability == second.submitCapability {
		t.Fatalf("two allocations shared a submit_capability")
	}
	if first.machineRevokeCapability == second.machineRevokeCapability {
		t.Fatalf("two allocations shared a machine_revoke_capability")
	}
	// Binding one pairing's wake sequence must not affect the other's.
	bindAddress(t, h, first)
	env := buildWakeV1(t, decodeAddr(t, second.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, second.submitCapability, env)
	requireStatus(t, resp, http.StatusOK)
}

// TestAllocate_WrongKey_Returns401.
func TestAllocate_WrongKey_Returns401(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-allocwrong")
	impostor, _ := genInstallationKey(t)
	path := "/v1/installations/" + r.installationID + "/addresses"
	body := []byte("{}")
	headers, _, _ := sign(t, impostor, h.clock.Now(), signParams{method: "POST", path: path, body: body})
	resp := h.doJSON("POST", path, body, headers)
	requireStatus(t, resp, http.StatusUnauthorized)
}

// TestAllocate_AddressLimitReached_Returns409 is PG-ALLOC-4.
func TestAllocate_AddressLimitReached_Returns409(t *testing.T) {
	h := newHarness(t, func(cfg *pushgw.Config) {
		cfg.Quotas.AllocationsPerInstallation = 1
	})
	r := registerInstallation(t, h, "fcm-token-limit")
	_ = allocateAddress(t, h, r) // consumes the one allowed slot

	path := "/v1/installations/" + r.installationID + "/addresses"
	body := []byte("{}")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "POST", path: path, body: body})
	resp := h.doJSON("POST", path, body, headers)
	requireStatus(t, resp, http.StatusConflict)
	if e := decodeError(t, resp); e.Code != "address_limit_reached" || e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want address_limit_reached/false", e.Code, e.Retryable)
	}
}

// TestAllocate_BodyOverOneKiB_Returns413.
func TestAllocate_BodyOverOneKiB_Returns413(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-allocbig")
	path := "/v1/installations/" + r.installationID + "/addresses"
	oversized := bytes.Repeat([]byte("z"), 1024+1)
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "POST", path: path, body: oversized})
	resp := h.doJSON("POST", path, oversized, headers)
	requireStatus(t, resp, http.StatusRequestEntityTooLarge)
	if e := decodeError(t, resp); e.Code != "body_too_large" {
		t.Fatalf("code = %q, want body_too_large", e.Code)
	}
}

// TestAllocate_UnexpectedField_Returns400MalformedRequest: the request schema is
// deliberately empty (§3.3) — the gateway learns nothing about the machine being paired.
func TestAllocate_UnexpectedField_Returns400MalformedRequest(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-allocfield")
	path := "/v1/installations/" + r.installationID + "/addresses"
	body := []byte(`{"hostname":"shouldnt-be-here"}`)
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "POST", path: path, body: body})
	resp := h.doJSON("POST", path, body, headers)
	requireStatus(t, resp, http.StatusBadRequest)
	if e := decodeError(t, resp); e.Code != "malformed_request" {
		t.Fatalf("code = %q, want malformed_request", e.Code)
	}
}
