package swarmmobile

// ADR-016 W3 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): "Under webpki, the
// pairing dial is an ordinary verified dial ... B45's deadlock does not exist under the
// default policy and its exemption is therefore withdrawn from it" -- reached only as a
// fallback (see pairingDial's own doc comment in mobile/pairing.go).
//
// This file proves the two-attempt shape end to end against a REAL relay, rather than only
// against the Security-value logic mobile/r2_adr016_w3_test.go already covers: a self-signed
// relay (the pinned_spki, expert/opt-in shape) still admits the pairing dial via B45's
// preserved fallback, and a relay whose chain a real platform delegate approves is admitted
// through the VERIFIED attempt -- proven by Conn.PeerSPKI() staying EMPTY, since only the
// unverified branch's VerifyPeerCertificate records into the B48 observer (security.go).
//
// Internal test file (package swarmmobile) because pairingDial is unexported.

import (
	"context"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

type w3NoopPushSink struct{}

func (w3NoopPushSink) Push(context.Context, string, relay.PushPayload) error { return nil }

// w3StartRelay boots a real relay on 127.0.0.1:0 over plain ws://.
func w3StartRelay(t *testing.T) string {
	t.Helper()
	cfg := relay.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode = "off"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(cfg, relay.WithPushSink(w3NoopPushSink{}))
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv.URL()
}

// w3TLSFrontedRelay fronts a real ws:// relay with a self-signed httptest TLS server --
// untrusted by any system root pool, exactly the pinned_spki (expert, self-signed) shape.
func w3TLSFrontedRelay(t *testing.T) string {
	t.Helper()
	wsURL := w3StartRelay(t)
	target, err := url.Parse(strings.Replace(wsURL, "ws://", "http://", 1))
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	front := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(front.Close)
	return strings.Replace(front.URL, "https://", "wss://", 1)
}

// alwaysTrustingRelayTrust is a stub W2 delegate that approves every chain -- standing in
// for a real X509TrustManagerExtensions that has the peer's issuer in its store. Go still
// enforces hostname and validity independently (security.go's verifyPlatformDelegate), so
// this alone does not admit an unrelated or expired certificate; it only removes the "does
// Kotlin approve the chain" half of the question this test is not about.
type alwaysTrustingRelayTrust struct{}

func (alwaysTrustingRelayTrust) VerifyRelayChain(string, []byte) error { return nil }

func w3TestApp(t *testing.T) *App {
	t.Helper()
	core, err := phonecore.Resume(phonecore.Config{})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}
	a := &App{core: core, events: newDispatcher()}
	t.Cleanup(a.events.close)
	return a
}

// TestADR016W3_PairingDialFallsBackToUnverifiedAgainstASelfSignedRelay is the
// pinned_spki-shaped case: no platform delegate installed (the fresh-phone default), the
// relay's certificate chains to nothing a system pool trusts -- so the verified attempt must
// fail and the ORIGINAL B45 fallback must still admit the dial, with B48's capture still
// recording what was presented.
func TestADR016W3_PairingDialFallsBackToUnverifiedAgainstASelfSignedRelay(t *testing.T) {
	wss := w3TLSFrontedRelay(t)
	a := w3TestApp(t)

	conn, err := a.pairingDial(context.Background(), wss)
	if err != nil {
		t.Fatalf("pairingDial against a self-signed relay with no platform delegate: %v.\n"+
			"B45's fallback must still admit a pinned_spki-shaped relay, or a fresh phone can "+
			"never pair with one over wss:// at all", err)
	}
	defer func() { _ = conn.Close() }()
	if len(conn.PeerSPKI()) == 0 {
		t.Fatalf("PeerSPKI() is empty after the fallback dial; B48's capture must still record " +
			"what an unverified dial presented")
	}
}

// TestADR016W3_PairingDialUsesTheVerifiedAttemptWhenAPlatformDelegateTrustsTheChain is the
// default-policy case: a platform delegate is installed (SetRelayTrust) and approves the
// chain, so the VERIFIED attempt must succeed -- proven by PeerSPKI() staying empty, since
// only the unverified fallback branch's VerifyPeerCertificate ever calls the B48 observer.
// This is the property ADR-016 W3 claims: "interception cost returns from 'be on the path'
// to 'hold a valid certificate'" -- the fallback (which any presented certificate can force)
// is never reached at all when the first attempt succeeds.
func TestADR016W3_PairingDialUsesTheVerifiedAttemptWhenAPlatformDelegateTrustsTheChain(t *testing.T) {
	wss := w3TLSFrontedRelay(t)
	a := w3TestApp(t)
	if err := a.SetRelayTrust(alwaysTrustingRelayTrust{}); err != nil {
		t.Fatalf("SetRelayTrust: %v", err)
	}

	conn, err := a.pairingDial(context.Background(), wss)
	if err != nil {
		t.Fatalf("pairingDial with a trusting platform delegate installed: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if len(conn.PeerSPKI()) != 0 {
		t.Fatalf("PeerSPKI() = %x, want empty: a non-empty value means the UNVERIFIED fallback "+
			"was used even though the verified attempt should have succeeded -- the exact "+
			"regression ADR-016 W3 exists to close", conn.PeerSPKI())
	}
}
