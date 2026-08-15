// FAILING-FIRST (TDD RED, GG-5) tests for the trusted-proxy source identity
// feature (playbook 6.5, R2 work package "proxy-quota").
//
// THE PROBLEM (docs/operations/relay-vps-deploy.md's amended _comment_quotas
// note): the relay keys every per-source quota off the raw TCP peer address it
// accepts (defaultSourceKey). Behind the documented Caddy-in-front-of-a-
// loopback-relay deployment, every real client's connection arrives FROM
// CADDY, so every per-source window collapses onto one shared bucket -- one
// client can exhaust another's budget, and MaxConcurrentConnectionsPerSource
// / ConnPerMin stop meaning "per real client" at all.
//
// THE FIX: a trusted_proxies config list of CIDRs (default empty -- today's
// behavior, unchanged). When the TCP peer that reached the relay falls inside
// one of those CIDRs, the relay reads the client identity from the LAST
// (rightmost) X-Forwarded-For hop instead of the peer address -- the
// "rightmost-untrusted" convention: only the entry the trusted proxy itself
// appended is ever consulted, never the leftmost entry a client fully
// controls, and never any part of the header at all from a peer that is not a
// configured trusted proxy.
package relay

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// repoRoot walks up from the test's working directory to the module root. A
// same-named helper exists in tls_test.go, but that file is `package
// relay_test` (external), invisible from here (`package relay`).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// dialRawWithHeader is dialRaw (client.go's dialConn, raw/unpumped) with a
// caller-chosen X-Forwarded-For header, so a test can simulate what a trusted
// reverse proxy appends without standing up a real proxy in front of the test
// relay. It returns the dial error rather than failing the test: same-source
// admission refusal (bounds_test.go's existing pattern) can surface AT the
// dial (a closed handshake) as easily as on the first frame exchanged after
// it, and a cap-probing test needs both outcomes available.
func dialRawWithHeader(ctx context.Context, url, xff string) (*Conn, error) {
	var hdr http.Header
	if xff != "" {
		hdr = http.Header{"X-Forwarded-For": []string{xff}}
	}
	return dialRawWithHTTPHeader(ctx, url, hdr)
}

// dialRawWithHeaderLines is dialRawWithHeader for a caller that needs
// X-Forwarded-For sent as MULTIPLE SEPARATE header lines rather than one
// comma-joined line -- what an add-header-style proxy (HAProxy's default
// `option forwardfor`) emits, and what a client sending the header twice
// through a trusted peer can reproduce on its own (the duplicate-header
// finding this file's TestRelay_TrustedProxy_DuplicateXFFHeaderLinesUseRightmostAcrossAllLines
// exists to close).
func dialRawWithHeaderLines(ctx context.Context, url string, lines []string) (*Conn, error) {
	return dialRawWithHTTPHeader(ctx, url, http.Header{"X-Forwarded-For": lines})
}

// dialRawWithHTTPHeader is dialRaw(t, url) (harness_test.go) with a
// caller-chosen HTTP header and a returned error instead of a t.Fatalf --
// distinct name because harness_test.go already declares dialRaw with a
// different signature (t *testing.T, url string) for its own callers.
func dialRawWithHTTPHeader(ctx context.Context, url string, hdr http.Header) (*Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, DefaultDialTimeout)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, url, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(MaxFrame + 64)
	cctx, ccancel := context.WithCancel(context.Background())
	return &Conn{ws: ws, ctx: cctx, cancel: ccancel, done: make(chan struct{})}, nil
}

// --- pure resolveSourceAddr unit tests -------------------------------------

func TestResolveSourceAddr_UntrustedPeerIgnoresXFF(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	got := resolveSourceAddr("203.0.113.5:4444", "9.9.9.9", trusted)
	if got != "203.0.113.5:4444" {
		t.Fatalf("resolveSourceAddr from a peer NOT in trusted_proxies = %q, want the raw peer "+
			"address unchanged -- X-Forwarded-For must never be consulted from an untrusted peer", got)
	}
}

func TestResolveSourceAddr_NoTrustedProxiesConfiguredIsCurrentBehavior(t *testing.T) {
	// Default empty trusted_proxies: even a peer that WOULD match some CIDR if
	// one were configured must fall back to the raw peer address, unchanged
	// from today.
	got := resolveSourceAddr("127.0.0.1:9440", "9.9.9.9", nil)
	if got != "127.0.0.1:9440" {
		t.Fatalf("resolveSourceAddr with no trusted_proxies configured = %q, want the raw peer "+
			"address (default empty must mean current behavior)", got)
	}
}

func TestResolveSourceAddr_TrustedPeerTakesRightmostHop(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	got := resolveSourceAddr("127.0.0.1:9440", "10.0.0.5, 198.51.100.7", trusted)
	if got != "198.51.100.7" {
		t.Fatalf("resolveSourceAddr from a trusted peer = %q, want the RIGHTMOST X-Forwarded-For "+
			"hop (198.51.100.7): the leftmost hop is entirely client-supplied and must never be "+
			"trusted (rightmost-untrusted algorithm)", got)
	}
}

func TestResolveSourceAddr_TrustedPeerNoHeaderFallsBackToPeer(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	got := resolveSourceAddr("127.0.0.1:9440", "", trusted)
	if got != "127.0.0.1:9440" {
		t.Fatalf("resolveSourceAddr from a trusted peer with no X-Forwarded-For header = %q, "+
			"want the peer address as a safe fallback", got)
	}
}

func TestResolveSourceAddr_SpoofedExtraHopsDoNotMoveTheBucket(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	a := resolveSourceAddr("127.0.0.1:1", "attacker-fake-1, 198.51.100.7", trusted)
	b := resolveSourceAddr("127.0.0.1:2", "attacker-fake-2, attacker-fake-3, attacker-fake-4, 198.51.100.7", trusted)
	if a != "198.51.100.7" || b != "198.51.100.7" {
		t.Fatalf("resolveSourceAddr with differing fabricated leftmost hops = %q, %q, want both "+
			"%q: the rightmost-untrusted rule must ignore every entry left of the trusted proxy's "+
			"own appended hop, so a client cannot choose an arbitrary bucket by padding the header",
			a, b, "198.51.100.7")
	}
}

// TestResolveSourceAddr_NonIPHopFallsBackToPeer: playbook 6.5 requires keying
// source quotas "by the validated external address" -- the rightmost hop must
// be an actual IP, not just whatever bytes sit rightmost of the last comma.
// Without this check, a trusted proxy's own peer address is a fail-safe IP,
// but an attacker able to reach a trusted peer (e.g. any local process on a
// VPS whose loopback listener sits inside trusted_proxies) could otherwise
// pick an arbitrary-length, non-IP string as its own quota bucket key.
func TestResolveSourceAddr_NonIPHopFallsBackToPeer(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	got := resolveSourceAddr("127.0.0.1:9440", "not-an-ip", trusted)
	if got != "127.0.0.1:9440" {
		t.Fatalf("resolveSourceAddr with a non-IP rightmost hop = %q, want the peer address as the "+
			"fail-safe fallback: an unvalidated hop must never become the quota bucket key", got)
	}
}

// TestResolveSourceAddr_HostPortHopIsUnwrappedBeforeValidation: a rightmost
// hop shaped "IP:port" or "[v6]:port" is still a validated external address --
// resolveSourceAddr must strip the port before the IP check, not reject it
// outright, and the bucket key it returns is the bare IP (matching every
// other hop shape this function returns, which all lack a port).
func TestResolveSourceAddr_HostPortHopIsUnwrappedBeforeValidation(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	if got := resolveSourceAddr("127.0.0.1:1", "198.51.100.7:4444", trusted); got != "198.51.100.7" {
		t.Fatalf("resolveSourceAddr(\"198.51.100.7:4444\") = %q, want the bare IP \"198.51.100.7\"", got)
	}
	if got := resolveSourceAddr("127.0.0.1:2", "[2001:db8::1]:4444", trusted); got != "2001:db8::1" {
		t.Fatalf("resolveSourceAddr(\"[2001:db8::1]:4444\") = %q, want the bare IP \"2001:db8::1\"", got)
	}
}

// TestResolveSourceAddr_CanonicalizesTextualIPVariants: distinct textual
// spellings of the SAME host must resolve to the SAME bucket key, or an
// operator's per-source cap is silently only per-spelling. net.ParseIP
// accepts an IPv4-mapped IPv6 form ("::ffff:198.51.100.7") and an
// uncompressed IPv6 form ("2001:db8:0:0:0:0:0:1") as well as the more
// familiar forms of the same two hosts -- resolveSourceAddr must return the
// same string (net.IP.String()'s canonical form) for both spellings of a
// given host, not the raw text a proxy happened to write.
func TestResolveSourceAddr_CanonicalizesTextualIPVariants(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	plain := resolveSourceAddr("127.0.0.1:1", "198.51.100.7", trusted)
	mapped := resolveSourceAddr("127.0.0.1:2", "::ffff:198.51.100.7", trusted)
	if plain != mapped {
		t.Fatalf("resolveSourceAddr(%q) = %q but resolveSourceAddr(%q) = %q: two spellings of the "+
			"same host must share one quota bucket", "198.51.100.7", plain, "::ffff:198.51.100.7", mapped)
	}

	compressed := resolveSourceAddr("127.0.0.1:3", "2001:DB8::1", trusted)
	expanded := resolveSourceAddr("127.0.0.1:4", "2001:db8:0:0:0:0:0:1", trusted)
	if compressed != expanded {
		t.Fatalf("resolveSourceAddr(%q) = %q but resolveSourceAddr(%q) = %q: two spellings of the "+
			"same host must share one quota bucket", "2001:DB8::1", compressed, "2001:db8:0:0:0:0:0:1", expanded)
	}
}

// --- Config / New validation -----------------------------------------------

func TestDefaultConfig_TrustedProxiesEmptyByDefault(t *testing.T) {
	if got := DefaultConfig().TrustedProxies; len(got) != 0 {
		t.Fatalf("DefaultConfig().TrustedProxies = %v, want empty: trusted-proxy XFF parsing must "+
			"be an explicit opt-in, matching today's behavior until an operator configures it", got)
	}
}

func TestNew_RejectsMalformedTrustedProxyCIDR(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	cfg.TrustedProxies = []string{"not-a-cidr"}
	if _, err := New(cfg); err == nil {
		t.Fatalf("New with a malformed trusted_proxies entry returned a nil error, want a clean " +
			"fail-closed refusal (same convention as a malformed config file failing LoadConfig)")
	}
}

// --- end-to-end quota-bucket tests (the four REQUIRED adversarial cases) ---

// TestRelay_UntrustedPeerXFFSpoofIgnored_BucketIsPeerAddr: a peer that is not
// a configured trusted proxy gets NO benefit from forging X-Forwarded-For --
// every connection it opens, however it labels itself, lands in the one
// bucket its real peer address owns.
func TestRelay_UntrustedPeerXFFSpoofIgnored_BucketIsPeerAddr(t *testing.T) {
	const capN = 3
	srv, _, _, _ := startTestRelay(t, func(cfg *Config) {
		cfg.Quotas.MaxConcurrentConnections = 0 // isolate the per-source cap
		cfg.Quotas.MaxConcurrentConnectionsPerSource = capN
		// A CONFIGURED trusted_proxies list that does NOT contain the test
		// dialer's peer address (127.0.0.1): this exercises the actual
		// trustedContains gate inside resolveSourceAddr, not just the
		// len(trusted)==0 early return -- an empty list here would let the
		// test pass even if the gate itself were deleted outright.
		cfg.TrustedProxies = []string{"192.0.2.0/24"}
	})

	for i := 0; i < capN; i++ {
		conn, err := dialRawWithHeader(testCtx(t), srv.URL(), fmt.Sprintf("203.0.113.%d", i))
		if err != nil {
			t.Fatalf("dial #%d with spoofed XFF from an untrusted peer: %v", i, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if _, _, err := conn.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
			t.Fatalf("Hello on in-cap connection #%d: %v", i, err)
		}
	}

	over, err := dialRawWithHeader(testCtx(t), srv.URL(), "203.0.113.99")
	if err == nil {
		t.Cleanup(func() { _ = over.Close() })
		_, _, err = over.Hello(testCtx(t), ProtocolVersion, nil)
	}
	if err == nil {
		t.Fatalf("a connection beyond MaxConcurrentConnectionsPerSource=%d was served because it "+
			"carried a fresh spoofed X-Forwarded-For identity from an UNTRUSTED peer: the header "+
			"must never be consulted unless the peer is a configured trusted proxy", capN)
	}
}

// TestRelay_TrustedProxy_SpoofedExtraHopsCannotChooseArbitraryBucket: through
// a trusted proxy, a client that pads X-Forwarded-For with fabricated
// leftmost hops still lands in the bucket the proxy's own rightmost hop
// names -- it cannot pick a different bucket per connection to dodge its cap.
func TestRelay_TrustedProxy_SpoofedExtraHopsCannotChooseArbitraryBucket(t *testing.T) {
	const capN = 3
	srv, _, _, _ := startTestRelay(t, func(cfg *Config) {
		cfg.Quotas.MaxConcurrentConnections = 0
		cfg.Quotas.MaxConcurrentConnectionsPerSource = capN
		cfg.TrustedProxies = []string{"127.0.0.1/32"} // the test dialer's own peer address
	})

	for i := 0; i < capN; i++ {
		// The fabricated leftmost hop is a VALID, DISTINCT IP per connection
		// (not a non-IP placeholder): resolveSourceAddr's ParseIP validation
		// would reject a non-IP hop and fall back to the shared peer address
		// regardless of which hop a leftmost-taking bug read, collapsing
		// every connection into one bucket and leaving the cap enforced for
		// the wrong reason. A valid, distinct leftmost IP means a
		// leftmost-hop bug buys a fresh bucket per connection instead.
		xff := fmt.Sprintf("10.0.0.%d, 198.51.100.7", i)
		conn, err := dialRawWithHeader(testCtx(t), srv.URL(), xff)
		if err != nil {
			t.Fatalf("dial #%d: %v", i, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if _, _, err := conn.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
			t.Fatalf("Hello on in-cap connection #%d: %v", i, err)
		}
	}

	over, err := dialRawWithHeader(testCtx(t), srv.URL(), "10.0.0.99, 198.51.100.7")
	if err == nil {
		t.Cleanup(func() { _ = over.Close() })
		_, _, err = over.Hello(testCtx(t), ProtocolVersion, nil)
	}
	if err == nil {
		t.Fatalf("a connection beyond MaxConcurrentConnectionsPerSource=%d was served despite "+
			"sharing the trusted proxy's own rightmost hop (198.51.100.7) with the other %d: a "+
			"fabricated leftmost hop must never select a different bucket", capN, capN)
	}
}

// TestRelay_TrustedProxy_TwoClientsGetTwoBuckets: two DIFFERENT real clients
// through the same trusted proxy must land in two DIFFERENT buckets -- one
// cannot exhaust the other's quota budget.
func TestRelay_TrustedProxy_TwoClientsGetTwoBuckets(t *testing.T) {
	const capN = 2
	srv, _, _, _ := startTestRelay(t, func(cfg *Config) {
		cfg.Quotas.MaxConcurrentConnections = 0
		cfg.Quotas.MaxConcurrentConnectionsPerSource = capN
		cfg.TrustedProxies = []string{"127.0.0.1/32"}
	})

	for i := 0; i < capN; i++ {
		conn, err := dialRawWithHeader(testCtx(t), srv.URL(), "198.51.100.1")
		if err != nil {
			t.Fatalf("clientA dial #%d: %v", i, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if _, _, err := conn.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
			t.Fatalf("clientA Hello on in-cap connection #%d: %v", i, err)
		}
	}

	overA, err := dialRawWithHeader(testCtx(t), srv.URL(), "198.51.100.1")
	if err == nil {
		t.Cleanup(func() { _ = overA.Close() })
		_, _, err = overA.Hello(testCtx(t), ProtocolVersion, nil)
	}
	if err == nil {
		t.Fatalf("clientA exceeded its own MaxConcurrentConnectionsPerSource=%d and was still served", capN)
	}

	// clientB, a genuinely different client behind the same proxy, must be
	// wholly unaffected by clientA having exhausted ITS OWN bucket.
	connB, err := dialRawWithHeader(testCtx(t), srv.URL(), "198.51.100.2")
	if err != nil {
		t.Fatalf("clientB dial after clientA exhausted its own budget: %v", err)
	}
	t.Cleanup(func() { _ = connB.Close() })
	if _, _, err := connB.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
		t.Fatalf("clientB refused after clientA exhausted ITS OWN budget through the same proxy: "+
			"%v -- quota isolation is broken, one client can exhaust another's bucket", err)
	}
}

// TestRelay_TrustedProxy_DuplicateXFFHeaderLinesUseRightmostAcrossAllLines:
// r.Header.Get("X-Forwarded-For") returns only the FIRST header line -- a
// client that sends X-Forwarded-For as TWO SEPARATE header lines through a
// trusted peer (what an add-header-style proxy like HAProxy's default
// `option forwardfor` emits) could otherwise put an attacker-chosen value
// (the first line, entirely client-supplied) in front of the trusted proxy's
// genuine rightmost hop and have it read as if it were the only line. The
// relay must read EVERY X-Forwarded-For line and take the rightmost entry
// across all of them combined, so two lines sharing the same real rightmost
// hop still land in ONE bucket -- proven here by dialing capN+1 connections
// that each vary only the (irrelevant, attacker-controlled) first line.
func TestRelay_TrustedProxy_DuplicateXFFHeaderLinesUseRightmostAcrossAllLines(t *testing.T) {
	const capN = 2
	srv, _, _, _ := startTestRelay(t, func(cfg *Config) {
		cfg.Quotas.MaxConcurrentConnections = 0
		cfg.Quotas.MaxConcurrentConnectionsPerSource = capN
		cfg.TrustedProxies = []string{"127.0.0.1/32"}
	})

	for i := 0; i < capN; i++ {
		// The attacker-controlled first line is a VALID, DISTINCT IP per
		// connection, not a non-IP placeholder: under the pre-fix
		// r.Header.Get (first line only), a non-IP first line would fail
		// resolveSourceAddr's ParseIP check and fall back to the shared peer
		// address, collapsing every connection into one bucket and leaving
		// the cap enforced for the wrong reason -- the bug this test exists
		// to catch would go undetected. A valid, distinct first-line IP
		// means the pre-fix code buys a fresh, ACCEPTED bucket per
		// connection instead, which is what must be prevented.
		conn, err := dialRawWithHeaderLines(testCtx(t), srv.URL(), []string{fmt.Sprintf("10.0.0.%d", i), "198.51.100.7"})
		if err != nil {
			t.Fatalf("dial #%d with two X-Forwarded-For header lines: %v", i, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if _, _, err := conn.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
			t.Fatalf("Hello on in-cap connection #%d: %v", i, err)
		}
	}

	over, err := dialRawWithHeaderLines(testCtx(t), srv.URL(), []string{"10.0.0.99", "198.51.100.7"})
	if err == nil {
		t.Cleanup(func() { _ = over.Close() })
		_, _, err = over.Hello(testCtx(t), ProtocolVersion, nil)
	}
	if err == nil {
		t.Fatalf("a connection beyond MaxConcurrentConnectionsPerSource=%d was served despite "+
			"sharing the same rightmost hop (198.51.100.7) as the other %d, spread across TWO "+
			"X-Forwarded-For header LINES: only the first line was read, so a distinct first-line "+
			"value bought a fresh bucket per connection -- exactly the spoofable-quota-bucket defect",
			capN, capN)
	}
}

// TestRelay_ComposeDefaultConfigSetsLoopbackProxyCIDR: the shipped example
// config for the documented Caddy-in-front-of-a-loopback-relay deployment
// (docs/operations/relay-vps-deploy.md) must configure trusted_proxies to
// cover that loopback address -- otherwise every real client behind it still
// collapses into Caddy's one shared bucket, the exact defect this feature
// exists to close.
func TestRelay_ComposeDefaultConfigSetsLoopbackProxyCIDR(t *testing.T) {
	path := filepath.Join(repoRoot(t), "deploy", "relay", "relay.config.example")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s): %v", path, err)
	}
	if len(cfg.TrustedProxies) == 0 {
		t.Fatalf("shipped relay.config.example sets no trusted_proxies: the documented Caddy-on-" +
			"loopback topology needs the loopback CIDR configured so per-source quotas key by the " +
			"real client rather than by Caddy")
	}
	loopback := net.ParseIP("127.0.0.1")
	found := false
	for _, cidr := range cfg.TrustedProxies {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatalf("shipped relay.config.example trusted_proxies entry %q is not a valid CIDR: %v", cidr, err)
		}
		if ipnet.Contains(loopback) {
			found = true
		}
	}
	if !found {
		t.Fatalf("shipped relay.config.example trusted_proxies %v does not cover 127.0.0.1, the "+
			"address Caddy connects from under listen=%q", cfg.TrustedProxies, cfg.Listen)
	}
}

// --- pre-auth DoS surface (reviewer finding, R2 "proxy-quota" BLOCKING) ----
//
// server.go's handleHTTP joins every X-Forwarded-For header line and
// resolveSourceAddr (trustedproxy.go) then Splits that joined string on ",":
// on the trusted-proxy path, both run BEFORE authentication and BEFORE
// serveConn's MaxConcurrentConnections admission check, so an attacker-sized
// header costs allocation proportional to its own length, unauthenticated,
// per connection. Two independent bounds close this: resolveSourceAddr's own
// rightmost-hop extraction must be O(1) allocation regardless of xff's
// length (below), and http.Server.MaxHeaderBytes must replace net/http's
// 1 MiB default, which otherwise retains a same-order buffer while parsing
// the header before resolveSourceAddr ever runs (TestServer_...).

// TestResolveSourceAddr_LargeXFFExtractionIsBoundedNotProportional: the
// rightmost-hop extraction inside resolveSourceAddr must not allocate memory
// proportional to xff's length. strings.Split(xff, ",") materializes one
// slice element per comma-separated hop -- 200,000 fabricated leftmost hops
// (a header shape open to anyone behind a configured trusted proxy) must not
// cost 200,000 allocations' worth of memory to discard.
func TestResolveSourceAddr_LargeXFFExtractionIsBoundedNotProportional(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	hops := make([]string, 200_000)
	for i := range hops {
		hops[i] = "9.9.9.9"
	}
	xff := strings.Join(append(hops, "198.51.100.7"), ",") // ~1.6 MB, built OUTSIDE the measured call

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got := resolveSourceAddr("127.0.0.1:1", xff, trusted)
	runtime.ReadMemStats(&after)

	if got != "198.51.100.7" {
		t.Fatalf("resolveSourceAddr with a %d-byte X-Forwarded-For = %q, want the rightmost hop "+
			"198.51.100.7", len(xff), got)
	}
	const bound = 4096 // O(1): must not scale with len(xff) (~1.6 MB here)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > bound {
		t.Fatalf("resolveSourceAddr allocated %d bytes extracting the rightmost hop from a %d-byte "+
			"X-Forwarded-For value, want under %d bytes (O(1), not proportional to header size): "+
			"strings.Split(xff, \",\") materializes one slice element per comma-separated hop, so an "+
			"attacker-sized header on the trusted-proxy path costs allocation proportional to its "+
			"length -- pre-authentication, before serveConn's MaxConcurrentConnections admission "+
			"check ever runs", allocated, len(xff), bound)
	}
}

// TestServer_OversizedForwardedHeaderRejectedBeforeUpgrade is the other half
// of the same finding: net/http's own DefaultMaxHeaderBytes (1 MiB) retains a
// buffer of up to that size while parsing headers -- BEFORE any relay code,
// auth, or admission check ever runs -- regardless of how efficiently
// resolveSourceAddr itself processes the header once handed it. The relay
// must set its own, materially tighter, http.Server.MaxHeaderBytes.
func TestServer_OversizedForwardedHeaderRejectedBeforeUpgrade(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, func(cfg *Config) {
		cfg.TrustedProxies = []string{"127.0.0.1/32"}
	})

	// ~200 KB: comfortably under net/http's 1 MiB DEFAULT (so a failure here
	// proves the relay set its OWN, tighter bound, not net/http's stock one),
	// and far past any legitimate X-Forwarded-For chain.
	huge := strings.Repeat("9,", 100_000) + "198.51.100.7"
	if _, err := dialRawWithHeader(testCtx(t), srv.URL(), huge); err == nil {
		t.Fatalf("dial with a %d-byte X-Forwarded-For header succeeded; want the relay's own "+
			"http.Server.MaxHeaderBytes bound to refuse it before any websocket upgrade or "+
			"admission check runs", len(huge))
	}
}
