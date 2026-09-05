package pushgw_test

// Quotas and abuse (spec §9). Covers PG-Q-1..3: per-address and per-source wake rates, and
// bounded registrations per source. PG-ALLOC-4's per-installation allocation bound is
// exercised in allocate_test.go (address_limit_reached is a distinct code from
// quota_exceeded and belongs beside PG-ALLOC-4's other assertions).
//
// RED: package pushgw has no implementation yet; every pushgw.* reference below is an
// undefined symbol.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
)

// TestQuota_WakesPerAddress_RefusesBeforeCallingProvider is PG-Q-1 and PG-Q-2: the
// per-address bucket is what stops a leaked submit capability from draining a battery, and
// the refusal SHALL happen before any provider call, never a silent drop.
func TestQuota_WakesPerAddress_RefusesBeforeCallingProvider(t *testing.T) {
	h := newHarness(t, func(cfg *pushgw.Config) {
		cfg.Quotas.WakesPerAddress = pushgw.RateLimit{Max: 1, Window: time.Minute}
	})
	r := registerInstallation(t, h, "fcm-token-quota-addr")
	a := allocateAddress(t, h, r)
	addr := decodeAddr(t, a.pushAddress)

	first := submitTestWake(h, a.submitCapability, buildWakeV1(t, addr, 1, h.clock.Now()))
	requireStatus(t, first, http.StatusOK)

	second := submitTestWake(h, a.submitCapability, buildWakeV1(t, addr, 2, h.clock.Now()))
	requireStatus(t, second, http.StatusTooManyRequests)
	e := decodeError(t, second)
	if e.Code != "quota_exceeded" || !e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want quota_exceeded/true", e.Code, e.Retryable)
	}
	if second.Header.Get("Retry-After") == "" {
		t.Fatalf("429 response is missing Retry-After")
	}
	if got := len(h.sender.calls()); got != 1 {
		t.Fatalf("provider called %d times; the refused wake must never reach it (PG-Q-2)", got)
	}
}

// TestQuota_WakesPerSourceIP_ChargesAcrossAddresses is PG-Q-1's other bucket: charged to
// the SOURCE, so one developer machine spamming many pairings is bounded too.
func TestQuota_WakesPerSourceIP_ChargesAcrossAddresses(t *testing.T) {
	h := newHarness(t, func(cfg *pushgw.Config) {
		cfg.Quotas.WakesPerSourceIP = pushgw.RateLimit{Max: 1, Window: time.Minute}
	})
	r := registerInstallation(t, h, "fcm-token-quota-src")
	a := allocateAddress(t, h, r)
	b := allocateAddress(t, h, r)

	first := submitTestWake(h, a.submitCapability, buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now()))
	requireStatus(t, first, http.StatusOK)

	// A DIFFERENT address, same loopback source: still refused, because the bucket is
	// per-source, not per-address.
	second := submitTestWake(h, b.submitCapability, buildWakeV1(t, decodeAddr(t, b.pushAddress), 1, h.clock.Now()))
	requireStatus(t, second, http.StatusTooManyRequests)
	if e := decodeError(t, second); e.Code != "quota_exceeded" {
		t.Fatalf("code = %q, want quota_exceeded", e.Code)
	}
}

// TestQuota_AllocationsPerSourceIP_Returns429 is PG-Q-3: allocations bounded per source
// IP, independent of the per-installation bound (PG-ALLOC-4, tested in allocate_test.go).
func TestQuota_AllocationsPerSourceIP_Returns429(t *testing.T) {
	h := newHarness(t, func(cfg *pushgw.Config) {
		cfg.Quotas.AllocationsPerSourceIP = pushgw.RateLimit{Max: 1, Window: time.Minute}
	})
	r := registerInstallation(t, h, "fcm-token-quota-allocsrc")
	_ = allocateAddress(t, h, r) // consumes the one allowed slot

	path := "/v1/installations/" + r.installationID + "/addresses"
	body := []byte("{}")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "POST", path: path, body: body})
	resp := h.doJSON("POST", path, body, headers)
	requireStatus(t, resp, http.StatusTooManyRequests)
	if e := decodeError(t, resp); e.Code != "quota_exceeded" || !e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want quota_exceeded/true", e.Code, e.Retryable)
	}
}

// TestQuota_AllocationsGlobal_ChargesAcrossInstallations is PG-Q-3's global clause: bounded
// GLOBALLY, not only per installation and per source -- two different installations,
// generously high per-source and per-installation buckets, still share one global ceiling.
func TestQuota_AllocationsGlobal_ChargesAcrossInstallations(t *testing.T) {
	h := newHarness(t, func(cfg *pushgw.Config) {
		cfg.Quotas.AllocationsGlobal = pushgw.RateLimit{Max: 1, Window: time.Minute}
	})
	r1 := registerInstallation(t, h, "fcm-token-quota-allocglobal-1")
	_ = allocateAddress(t, h, r1)

	r2 := registerInstallation(t, h, "fcm-token-quota-allocglobal-2")
	path := "/v1/installations/" + r2.installationID + "/addresses"
	body := []byte("{}")
	headers, _, _ := sign(t, r2.priv, h.clock.Now(), signParams{method: "POST", path: path, body: body})
	resp := h.doJSON("POST", path, body, headers)
	requireStatus(t, resp, http.StatusTooManyRequests)
	if e := decodeError(t, resp); e.Code != "quota_exceeded" {
		t.Fatalf("code = %q, want quota_exceeded", e.Code)
	}
}

// TestQuota_RegistrationsGlobal_Returns429 is PG-Q-3's global clause for registrations:
// bounded globally, in addition to per source IP.
func TestQuota_RegistrationsGlobal_Returns429(t *testing.T) {
	h := newHarness(t, func(cfg *pushgw.Config) {
		cfg.Quotas.RegistrationsGlobal = pushgw.RateLimit{Max: 1, Window: time.Minute}
	})
	_ = registerInstallation(t, h, "fcm-token-quota-regglobal-1")

	priv, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-quota-regglobal-2",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-quota-regglobal-2"},
	})
	hash := jcsRequestHash(t, body)
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{RequestHash: hash, LicensedBuild: true}, nil
	})
	idem := fixedIdemKey("quota-regglobal-key")
	resp := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idem, body))
	requireStatus(t, resp, http.StatusTooManyRequests)
	if e := decodeError(t, resp); e.Code != "quota_exceeded" || !e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want quota_exceeded/true", e.Code, e.Retryable)
	}
}

// TestQuota_RegistrationsPerSourceIP_Returns429 is PG-REG-3 / PG-Q-3.
func TestQuota_RegistrationsPerSourceIP_Returns429(t *testing.T) {
	h := newHarness(t, func(cfg *pushgw.Config) {
		cfg.Quotas.RegistrationsPerSourceIP = pushgw.RateLimit{Max: 1, Window: time.Minute}
	})
	_ = registerInstallation(t, h, "fcm-token-quota-reg-1")

	priv, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-quota-reg-2",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-quota-reg-2"},
	})
	hash := jcsRequestHash(t, body)
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{RequestHash: hash, LicensedBuild: true}, nil
	})
	idem := fixedIdemKey("quota-reg-key")
	resp := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idem, body))
	requireStatus(t, resp, http.StatusTooManyRequests)
	if e := decodeError(t, resp); e.Code != "quota_exceeded" || !e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want quota_exceeded/true", e.Code, e.Retryable)
	}
}
