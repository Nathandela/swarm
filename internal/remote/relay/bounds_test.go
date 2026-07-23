// Relay Hardening CR-1 slice 2 — FAILING-FIRST (TDD RED, GG-5) tests for two
// ADDITIVE relay hardenings beyond the already-GREEN global concurrent-connection
// cap and per-read handshake idle window (harden_test.go):
//
//  1. A PER-SOURCE concurrent-connection cap (Quotas.MaxConcurrentConnectionsPerSource)
//     alongside the existing GLOBAL cap (Quotas.MaxConcurrentConnections), so one
//     source cannot monopolize the whole connection pool.
//  2. A CUMULATIVE handshake deadline anchored at accept time, so an
//     unauthenticated connection cannot stay alive forever by sending a harmless
//     frame every interval under HandshakeTimeout (today's readFrame reopens a
//     FRESH per-read window on every call — a slow-loris drip defeats it).
//
// Both are RED against the reviewed relay (commit 8664f3b + harden_test.go's
// already-GREEN CR-1 admission control):
//
//   - COMPILE-LEVEL RED: Quotas.MaxConcurrentConnectionsPerSource does not exist
//     yet. Go builds one test binary per package, so this undefined field fails
//     the whole relay test build.
//   - BEHAVIORAL RED (once the field exists): serveConn never consults it (the
//     (capN+1)th same-source connection is served), and readFrame's handshake
//     deadline is a per-read idle window, not a cumulative one (a drip of
//     harmless frames under HandshakeTimeout keeps the connection alive forever).
//
// This file contains NO implementation.
package relay

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// bucketedSource is a test-controlled WithSourceKeyFunc deriver. It lets a test
// fabricate MULTIPLE distinct sources on loopback (where the default source key —
// the IP host — collapses every connection to one source) by grouping connections
// into named buckets instead of deriving the key from the transport address at
// all. The test mutates the current bucket only BETWEEN sequential, blocking
// dial+round-trip calls, so the server's own (synchronous, pre-read-loop)
// sourceKeyFn call for connection i always observes the bucket set before dialing
// i — there is no data race with concurrent connections.
type bucketedSource struct {
	mu     sync.Mutex
	bucket string
}

func (b *bucketedSource) set(v string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bucket = v
}

func (b *bucketedSource) fn(_ string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bucket
}

// TestRelay_PerSourceConcurrentConnCapEnforced (CR-1) asserts a PER-SOURCE
// concurrent-connection cap, distinct from the existing GLOBAL cap
// (TestRelay_ConcurrentConnCapEnforced in harden_test.go): once
// MaxConcurrentConnectionsPerSource live sockets are held from ONE source, one
// more from THAT SAME source is cleanly refused/closed — while a connection from
// a DIFFERENT source is still admitted, proving the cap is per-source and not a
// second, smaller global cap. Against the reviewed relay (server.go serveConn
// tracks sourceKey via sourceKeyFn but never caps by it — only the global
// MaxConcurrentConnections is enforced) the (capN+1)th same-source connection is
// served, so this FAILS.
func TestRelay_PerSourceConcurrentConnCapEnforced(t *testing.T) {
	const capN = 3

	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode = "off"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	cfg.Quotas.MaxConcurrentConnections = 0 // unlimited global cap: isolate the per-source cap
	cfg.Quotas.MaxConcurrentConnectionsPerSource = capN

	clk := newFakeClock()
	src := &bucketedSource{}
	src.set("source-A")
	srv, err := New(cfg, WithClock(clk), WithAPNsSink(&mockAPNs{}), WithSourceKeyFunc(src.fn))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// Fill the per-source cap with live, usable connections from ONE source
	// (proving the cap is exactly capN, not smaller).
	for i := 0; i < capN; i++ {
		conn := dialRaw(t, srv.URL())
		if _, _, err := conn.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
			t.Fatalf("Hello on in-cap same-source connection #%d: %v", i, err)
		}
	}

	// The (capN+1)th connection from the SAME source must be cleanly refused:
	// either the dial fails, or the relay never serves a frame on it.
	over, err := DialRaw(testCtx(t), srv.URL())
	if err == nil {
		t.Cleanup(func() { _ = over.Close() })
		_, _, err = over.Hello(testCtx(t), ProtocolVersion, nil)
	}
	if err == nil {
		t.Fatalf("a same-source connection beyond MaxConcurrentConnectionsPerSource=%d was served; want a clean refusal (CR-1 per-source cap)", capN)
	}

	// A connection from a DIFFERENT source must still be admitted: the per-source
	// cap must not throttle a source that never touched it.
	src.set("source-B")
	other := dialRaw(t, srv.URL())
	if _, _, err := other.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
		t.Fatalf("connection from a DIFFERENT source after the first source hit its cap: got %v, want success — the cap is per-source, not global (CR-1)", err)
	}
}

// TestRelay_CumulativeHandshakeDeadline (CR-1) asserts HandshakeTimeout bounds
// the CUMULATIVE time-to-authenticate, anchored at accept time — not a per-read
// idle window that resets on every frame. A connection that never authenticates
// but sends a harmless pre-auth round-trip (hello — it satisfies readFrame's
// `!sc.authed && sc.rdvID == ""` handshake-state condition, so it never leaves
// the timed regime) at an interval comfortably UNDER HandshakeTimeout — keeping
// TODAY's per-readFrame window alive on every single read — must still be closed
// once the CUMULATIVE elapsed time since accept exceeds HandshakeTimeout (+
// grace). Against the reviewed relay (readFrame at server.go:407-428 does
// `context.WithTimeout(sc.ctx, to)` fresh on every readFrame call, so each drip
// arrives well inside its own fresh window and resets it) the connection is held
// alive for the whole guard duration and this FAILS.
func TestRelay_CumulativeHandshakeDeadline(t *testing.T) {
	const (
		handshakeTimeout = 300 * time.Millisecond
		dripInterval     = 80 * time.Millisecond // comfortably under handshakeTimeout
		guard            = 2 * time.Second       // hard bound on the whole test
	)
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.HandshakeTimeout = handshakeTimeout
		c.Quotas.MaxConcurrentConnections = 0
	})

	conn := dialRaw(t, srv.URL())

	deadline := time.Now().Add(guard)
	for time.Now().Before(deadline) {
		time.Sleep(dripInterval)
		if _, _, err := conn.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
			// The relay closed the connection even though every individual gap
			// between frames stayed under HandshakeTimeout — exactly the
			// cumulative, accept-time-anchored deadline CR-1 requires.
			return
		}
	}
	t.Fatalf("connection survived a %v-guard hello drip (interval %v, always under HandshakeTimeout=%v); want the relay to close it once CUMULATIVE time since accept exceeds the deadline instead of resetting the window on every read (CR-1)", guard, dripInterval, handshakeTimeout)
}
