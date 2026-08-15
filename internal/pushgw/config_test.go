package pushgw_test

// NewServer's config validation (section 9's fail-closed requirement). A RateLimit bucket
// that would silently disable itself at runtime -- a zero Window resetting on every call,
// or a negative Max -- must instead be refused at boot, never admitted as "unlimited".
//
// These drive pushgw.NewServer directly (not through newHarness, which always supplies a
// fully-valid QuotaConfig) so an invalid bucket is exercised in isolation.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
)

func minimalValidConfig(t *testing.T) pushgw.Config {
	t.Helper()
	return pushgw.Config{
		DBPath: filepath.Join(t.TempDir(), "pushgw.db"),
		Sender: newFakeSender(),
		Attest: newFakeAttestor(),
		Quotas: defaultTestQuotas(),
	}
}

// TestNewServer_ZeroWindowWithNonZeroMax_FailsClosedAtBoot is section 9's fail-closed
// requirement: RateLimit{Max: 1} (Window unset) previously reset its window on every call,
// so the bucket never actually refused anything.
func TestNewServer_ZeroWindowWithNonZeroMax_FailsClosedAtBoot(t *testing.T) {
	cfg := minimalValidConfig(t)
	cfg.Quotas.WakesPerAddress = pushgw.RateLimit{Max: 1} // Window deliberately unset
	if _, err := pushgw.NewServer(cfg); err == nil {
		t.Fatalf("NewServer accepted a Max>0/Window==0 bucket; want a boot-time config error")
	}
}

// TestNewServer_NegativeMax_FailsClosedAtBoot: a negative Max previously disabled the
// bucket outright (limiter.allow's old "Max<=0 means unlimited" reading).
func TestNewServer_NegativeMax_FailsClosedAtBoot(t *testing.T) {
	cfg := minimalValidConfig(t)
	cfg.Quotas.WakesPerAddress = pushgw.RateLimit{Max: -1, Window: time.Minute}
	if _, err := pushgw.NewServer(cfg); err == nil {
		t.Fatalf("NewServer accepted a negative Max; want a boot-time config error")
	}
}

// TestNewServer_ZeroAllocationsPerInstallation_FailsClosedAtBoot covers the one
// non-RateLimit quota field: a negative or zero explicit override must not silently mean
// "unbounded".
func TestNewServer_NegativeAllocationsPerInstallation_FailsClosedAtBoot(t *testing.T) {
	cfg := minimalValidConfig(t)
	cfg.Quotas.AllocationsPerInstallation = -1
	if _, err := pushgw.NewServer(cfg); err == nil {
		t.Fatalf("NewServer accepted a negative AllocationsPerInstallation; want a boot-time config error")
	}
}

// TestNewServer_UnsetQuotas_AppliesSpecDefaults proves the zero-value QuotaConfig (every
// bucket unset) still boots -- withDefaults, not validate, owns that shape.
func TestNewServer_UnsetQuotas_AppliesSpecDefaults(t *testing.T) {
	cfg := minimalValidConfig(t)
	cfg.Quotas = pushgw.QuotaConfig{}
	srv, err := pushgw.NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer rejected the all-unset default QuotaConfig: %v", err)
	}
	_ = srv.Close()
}

// TestNewServer_BadTrustedProxyCIDR_FailsClosedAtBoot: PG-Q-4's trust list is parsed once
// at construction; a malformed CIDR must not silently trust nothing (or panic later).
func TestNewServer_BadTrustedProxyCIDR_FailsClosedAtBoot(t *testing.T) {
	cfg := minimalValidConfig(t)
	cfg.TrustedProxies = []string{"not-a-cidr"}
	if _, err := pushgw.NewServer(cfg); err == nil {
		t.Fatalf("NewServer accepted a malformed trusted-proxy CIDR; want a boot-time config error")
	}
}
