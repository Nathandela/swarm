package pushgw_test

// Retention (spec §8.1), driven entirely by the fake clock — nothing here sleeps for real
// minutes or days. Every assertion is made through the public HTTP surface rather than by
// reaching into storage, so a retention sweep is verified by its PRODUCT effect (a
// capability that used to work no longer does) rather than by a row count alone.
//
// The middle §8.1 row ("delivery diagnostics, 7 days") has no read API of its own, so it
// is exercised through the one diagnostics-row artifact the public API DOES expose an
// effect for: PG-REV-2/PG-RET-3's machine-revoke tombstone, which this document places
// under that row.
//
// RED: package pushgw has no implementation yet; every pushgw.* reference below is an
// undefined symbol.

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestRetention_UnboundAllocationExpiresAfterTenMinutes is §8.1 row 1 / PG-ALLOC-2: an
// allocation that never had a wake accepted is deleted with all its verifiers.
func TestRetention_UnboundAllocationExpiresAfterTenMinutes(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-retention-unbound")
	a := allocateAddress(t, h, r) // deliberately never bound

	h.clock.advance(10*time.Minute + time.Second)
	if err := h.srv.RunRetention(context.Background()); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}

	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, resp, http.StatusUnauthorized)
}

// TestRetention_UnboundAllocationRefusedAtUseBeforeSweepRuns is the BLOCKING finding's
// reproduction of the reviewer's probe: PG-ALLOC-2 / §8.1 row 1's ten-minute deadline must
// be enforced AT USE inside handleWake, not only by the lazy retention sweep. Before this
// fix, a wake landing after the deadline but before RunRetention next ran was accepted,
// marked the address Bound, and a bound address is exempt from the unbound-sweep predicate
// -- so the escape was permanent, not merely late. This test deliberately never calls
// RunRetention before the first wake attempt (exactly the gap between allocation+10m and an
// hourly-by-default production sweep, cmd/swarm-pushgw's defaultRetentionInterval), then
// confirms the sweep afterward still finds and deletes the allocation, proving the at-use
// refusal did not itself mark it Bound.
func TestRetention_UnboundAllocationRefusedAtUseBeforeSweepRuns(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-retention-atuse")
	a := allocateAddress(t, h, r) // deliberately never bound

	h.clock.advance(10*time.Minute + time.Second)

	// No RunRetention call here: the deadline must be enforced by handleWake itself.
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, resp, http.StatusUnauthorized)
	if got := len(h.sender.calls()); got != 0 {
		t.Fatalf("provider called %d times for a wake against an expired-unbound allocation, want 0", got)
	}

	// The sweep, run afterward, must still find and delete the (still-unbound) allocation.
	if err := h.srv.RunRetention(context.Background()); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	env2 := buildWakeV1(t, decodeAddr(t, a.pushAddress), 2, h.clock.Now())
	resp2 := submitTestWake(h, a.submitCapability, env2)
	requireStatus(t, resp2, http.StatusUnauthorized)
}

// TestRetention_BoundAllocationSurvivesTenMinutes: the same sweep must NOT touch an
// address whose binding wake already landed (PG-ALLOC-2's "unbound" is a status, not a
// clock).
func TestRetention_BoundAllocationSurvivesTenMinutes(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-retention-bound")
	a := allocateAddress(t, h, r)
	bindAddress(t, h, a)

	h.clock.advance(10*time.Minute + time.Second)
	if err := h.srv.RunRetention(context.Background()); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}

	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 2, h.clock.Now())
	resp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, resp, http.StatusOK)
}

// TestRetention_UnboundSweep_MachineRevokeRetryStillGets204 is PG-REV-2: the ten-minute
// unbound sweep destroys an address exactly as an explicit revoke does, so a machine's
// durable revoke retry landing AFTER the sweep must still be idempotent (204), not 401 --
// the tombstone has to be written on every path that destroys an address.
func TestRetention_UnboundSweep_MachineRevokeRetryStillGets204(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-retention-unbound-tombstone")
	a := allocateAddress(t, h, r) // deliberately never bound

	h.clock.advance(10*time.Minute + time.Second)
	if err := h.srv.RunRetention(context.Background()); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}

	retry := revokeByMachine(h, a.pushAddress, a.machineRevokeCapability)
	requireStatus(t, retry, http.StatusNoContent)
}

// TestRetention_InstallationInactiveOneEightyDays_RevokesAccess is §8.1 row 3: no
// authenticated app refresh for 180 days deletes the token mapping and every address.
func TestRetention_InstallationInactiveOneEightyDays_RevokesAccess(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-retention-inactive")
	a := allocateAddress(t, h, r)
	bindAddress(t, h, a)

	h.clock.advance(180*24*time.Hour + time.Second)
	if err := h.srv.RunRetention(context.Background()); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}

	// The installation itself is gone: a correctly signed rotate against the dead id is
	// unauthorized (§4's unauthorized row explicitly covers "unknown installation").
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-after-expiry")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body})
	resp := h.doJSON("PUT", path, body, headers)
	requireStatus(t, resp, http.StatusUnauthorized)

	// Its address is gone too (§8.1 row 3: "Delete FCM token mapping and addresses").
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 3, h.clock.Now())
	wakeResp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, wakeResp, http.StatusUnauthorized)
}

// TestRetention_AuthenticatedRefreshResetsInactivityClock is PG-AUTH-5: ANY successfully
// authenticated installation request — not only the token-refresh idiom — moves the
// 180-day floor, so a device active well within the window must not be swept.
func TestRetention_AuthenticatedRefreshResetsInactivityClock(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-retention-refresh")

	h.clock.advance(170 * 24 * time.Hour)
	path := "/v1/installations/" + r.installationID + "/token"
	refreshBody := rotateBody("fcm-token-retention-refresh") // same token: the refresh idiom
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: refreshBody})
	requireStatus(t, h.doJSON("PUT", path, refreshBody, headers), http.StatusNoContent)

	// 170 more days since the REFRESH (340 since registration, but only 170 since the last
	// authenticated request) — must still be alive.
	h.clock.advance(170 * 24 * time.Hour)
	if err := h.srv.RunRetention(context.Background()); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}

	secondBody := rotateBody("fcm-token-retention-refresh-2")
	headers2, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: secondBody})
	resp := h.doJSON("PUT", path, secondBody, headers2)
	requireStatus(t, resp, http.StatusNoContent)
}

// TestRetention_MachineRevokeTombstoneExpiresAfterSevenDays is §8.1 row 2 / PG-RET-3: the
// bounded tombstone that makes a durable machine-side retry idempotent does not live
// forever — after 7 days a repeat of the same (now ancient) delete is no longer answered
// from it.
func TestRetention_MachineRevokeTombstoneExpiresAfterSevenDays(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-retention-tombstone")
	a := allocateAddress(t, h, r)

	first := revokeByMachine(h, a.pushAddress, a.machineRevokeCapability)
	requireStatus(t, first, http.StatusNoContent)

	// Within the window: idempotent retry still lands on the tombstone (204).
	soon := revokeByMachine(h, a.pushAddress, a.machineRevokeCapability)
	requireStatus(t, soon, http.StatusNoContent)

	h.clock.advance(7*24*time.Hour + time.Second)
	if err := h.srv.RunRetention(context.Background()); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}

	late := revokeByMachine(h, a.pushAddress, a.machineRevokeCapability)
	requireStatus(t, late, http.StatusUnauthorized)
}
