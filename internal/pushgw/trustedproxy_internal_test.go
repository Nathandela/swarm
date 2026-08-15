package pushgw

// Internal (package pushgw) behavioral tests for resolveSourceAddr (trustedproxy.go,
// PG-Q-4), scoped to the three properties the MEDIUM verification-gap finding named: an
// untrusted peer's X-Forwarded-For is never consulted, only the rightmost hop of a trusted
// peer's header is honoured, and an unparseable hop falls back to the peer address rather
// than being trusted blind. internal/remote/relay/trustedproxy_test.go tests the identical
// shape for that package's own (deliberately duplicated -- see trustedproxy.go's header
// comment) copy of this logic; this file is pushgw's equivalent.

import "testing"

func TestResolveSourceAddr_UntrustedPeerXFFIgnored(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	got := resolveSourceAddr("203.0.113.5:4444", "198.51.100.7", trusted)
	if got != "203.0.113.5:4444" {
		t.Fatalf("resolveSourceAddr from a peer NOT in trusted_proxies = %q, want the raw peer "+
			"address unchanged: X-Forwarded-For must never be consulted from an untrusted peer", got)
	}
}

func TestResolveSourceAddr_TrustedPeerHonoursOnlyTheRightmostHop(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	got := resolveSourceAddr("127.0.0.1:9440", "10.0.0.5, 198.51.100.7", trusted)
	if got != "198.51.100.7" {
		t.Fatalf("resolveSourceAddr from a trusted peer = %q, want the RIGHTMOST hop "+
			"198.51.100.7: the leftmost hop is entirely client-supplied and must never be trusted", got)
	}
}

func TestResolveSourceAddr_UnparseableHopFallsBackToPeer(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	got := resolveSourceAddr("127.0.0.1:9440", "not-an-ip", trusted)
	if got != "127.0.0.1:9440" {
		t.Fatalf("resolveSourceAddr with an unparseable rightmost hop = %q, want the peer "+
			"address as the fail-safe fallback: an unvalidated hop must never become the quota "+
			"bucket key", got)
	}
}
