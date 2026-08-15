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
	"errors"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/pairing"
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
// chain, so the VERIFIED attempt must succeed -- proven by only ONE dial ever reaching the
// relay (the fallback, which any presented certificate can force, is never attempted once
// the first succeeds). This is the property ADR-016 W3 claims: "interception cost returns
// from 'be on the path' to 'hold a valid certificate'".
//
// PeerSPKI() is asserted NON-EMPTY, not empty: the review-round fix that closed the HIGH
// finding on this file records B48's observation on every tlsConfig branch, verified or not
// ("Conn.PeerSPKI records the presented SPKI on every dial it owns, unchanged") -- so a
// pinned_spki machine's pin is still compared even when the pairing dial succeeds through
// the verified attempt. The old assertion here (PeerSPKI() empty) enshrined the opposite:
// see security.go's tlsConfig for where the fix lives.
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
	if len(conn.PeerSPKI()) == 0 {
		t.Fatalf("PeerSPKI() is empty after the VERIFIED attempt succeeded; B48's capture must " +
			"be recorded on every branch, not only the unverified fallback, or a pinned_spki " +
			"machine's pin is never compared when the verified attempt happens to succeed")
	}
}

// TestADR016W3_PairingDialDoesNotFallBackForAPublicHost is the HIGH finding's spec-drift
// fix: the ADR scopes B45's unverified exemption to "the machine's published policy is
// pinned_spki" -- a claim the phone cannot read before this very dial, so it is approximated
// by W6's own assignment instead: IP-literal and self-signed relays are the expert-policy,
// development population; an ordinary public DNS-named relay is not. A public host whose
// verified attempt fails is therefore refused OUTRIGHT, not silently retried unverified --
// otherwise an on-path attacker forces the same fallback by presenting ANY certificate that
// fails verification, against every machine regardless of its actual policy.
//
// relayDialRawSecure is a package variable exactly so this can be proven without a live
// network dial or a way to fake a "public" DNS name resolving anywhere.
func TestADR016W3_PairingDialDoesNotFallBackForAPublicHost(t *testing.T) {
	a := w3TestApp(t)
	verifiedErr := errors.New("wrong-name or untrusted certificate")
	var calls []relay.Security
	orig := relayDialRawSecure
	t.Cleanup(func() { relayDialRawSecure = orig })
	relayDialRawSecure = func(_ context.Context, _ string, sec relay.Security) (*relay.Conn, error) {
		calls = append(calls, sec)
		return nil, verifiedErr
	}

	_, err := a.pairingDial(context.Background(), "wss://relay.example.com:443/")
	if !errors.Is(err, verifiedErr) {
		t.Fatalf("pairingDial(public host) = %v, want the verified attempt's own error "+
			"propagated -- a public host must never fall back to B45's unverified policy", err)
	}
	if len(calls) != 1 {
		t.Fatalf("relayDialRawSecure called %d time(s), want exactly 1: a public host's "+
			"verified failure must not trigger the unverified fallback", len(calls))
	}
}

// TestADR016W3_PairingDialUsesTheVerifiedDialForAWebPKIMachineOnAPublicHost is the
// webpki punch list's MEDIUM B45-scope finding: the missing success-path complement to
// TestADR016W3_PairingDialDoesNotFallBackForAPublicHost above. An ordinary webpki machine
// on a public DNS-named host completes pairing through the VERIFIED attempt alone -- the
// unverified fallback is never even attempted, so the private-host residual ADR-016 W3's
// amendment records (2026-08-15) never applies to this population at all: nothing here
// falls back to a certificate an on-path attacker could present.
func TestADR016W3_PairingDialUsesTheVerifiedDialForAWebPKIMachineOnAPublicHost(t *testing.T) {
	a := w3TestApp(t)
	want := &relay.Conn{}
	var calls []relay.Security
	orig := relayDialRawSecure
	t.Cleanup(func() { relayDialRawSecure = orig })
	relayDialRawSecure = func(_ context.Context, _ string, sec relay.Security) (*relay.Conn, error) {
		calls = append(calls, sec)
		return want, nil
	}

	conn, err := a.pairingDial(context.Background(), "wss://relay.example.com:443/")
	if err != nil {
		t.Fatalf("pairingDial(public host, verified succeeds) = %v, want nil", err)
	}
	if conn != want {
		t.Fatalf("pairingDial returned %v, want the verified attempt's own connection", conn)
	}
	if len(calls) != 1 {
		t.Fatalf("relayDialRawSecure called %d time(s), want exactly 1: a successful verified "+
			"attempt on a public host must never also try the unverified fallback", len(calls))
	}
}

// TestADR016W3_PairingDialStillFallsBackForAPrivateHost is the symmetric case, proven the
// same seamed way: an IP-literal (or otherwise private-classified) origin still gets the
// fallback W6 assigns to it -- a self-signed pinned_spki relay must still be pairable.
func TestADR016W3_PairingDialStillFallsBackForAPrivateHost(t *testing.T) {
	a := w3TestApp(t)
	verifiedErr := errors.New("self-signed relay: verified attempt cannot chain")
	var calls []relay.Security
	orig := relayDialRawSecure
	t.Cleanup(func() { relayDialRawSecure = orig })
	relayDialRawSecure = func(_ context.Context, _ string, sec relay.Security) (*relay.Conn, error) {
		calls = append(calls, sec)
		if len(calls) == 1 {
			return nil, verifiedErr
		}
		return &relay.Conn{}, nil
	}

	conn, err := a.pairingDial(context.Background(), "wss://192.168.1.5:8443/")
	if err != nil {
		t.Fatalf("pairingDial(private host) = %v, want the fallback dial to succeed", err)
	}
	if conn == nil {
		t.Fatal("pairingDial(private host) returned a nil connection with no error")
	}
	if len(calls) != 2 {
		t.Fatalf("relayDialRawSecure called %d time(s), want exactly 2: a private origin must "+
			"still retry unverified after a failed verified attempt", len(calls))
	}
}

// TestADR016W3_AMismatchedPinStillRefusesOnTheVerifiedPath is the HIGH finding's own
// reproduction, inverted: a pinned_spki machine's published pin must still be compared even
// when the pairing dial succeeds through the VERIFIED attempt (a trusting platform delegate,
// or -- on Android -- any enterprise/MDM root the platform store accepts). Before the
// review-round fix, a successful verified attempt left Conn.PeerSPKI() empty, so
// checkRelayPin(machinePin, nil) passed unconditionally: a pinned_spki machine's pin was
// never consulted whenever the presented certificate happened to verify. This proves the
// mismatch is still caught now that every tlsConfig branch records (security.go).
func TestADR016W3_AMismatchedPinStillRefusesOnTheVerifiedPath(t *testing.T) {
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

	wrongPin := make([]byte, 32)
	for i := range wrongPin {
		wrongPin[i] = 0xEE
	}
	machine := pairing.MachinePayload{RelayTLSPolicy: "pinned_spki", RelaySPKIPin: wrongPin}
	if err := checkRelayPin(effectiveRelayPin(machine), conn.PeerSPKI()); err == nil {
		t.Fatal("checkRelayPin accepted a wrong pin on the VERIFIED path: a pinned_spki " +
			"machine's published pin must still be the whole defense, whichever of " +
			"pairingDial's two attempts happened to succeed")
	}
}
