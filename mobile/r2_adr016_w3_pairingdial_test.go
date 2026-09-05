package swarmmobile

// ADR-016 W3 (bd agents-tracker-hggx.3.5): "Under webpki, the
// pairing dial is an ordinary verified dial ... B45's deadlock does not exist under the
// default policy and its exemption is therefore withdrawn from it" -- reached only as a
// fallback (see pairingDial's own doc comment in mobile/pairing.go).
//
// This file proves the two-attempt dial policy against a TLS websocket and a narrow dial seam.
// The full Noise ceremony against workerd lives in pairing_relay_v2_test.go. A self-signed
// endpoint still admits the private-origin fallback, while a platform-approved chain uses the
// verified attempt. Both paths retain the presented SPKI for the later pin check.
//
// Internal test file (package swarmmobile) because pairingDial is unexported.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
	"github.com/coder/websocket"
)

const w3Ceremony = "11111111111111111111111111111111"

// w3TLSFrontedRelay accepts the relay-v2 websocket endpoint with a self-signed certificate.
// pairingDial only opens the socket; the Noise exchange is covered by the workerd test.
func w3TLSFrontedRelay(t *testing.T) string {
	t.Helper()
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		<-r.Context().Done()
	}))
	t.Cleanup(front.Close)
	return strings.Replace(front.URL, "https://", "wss://", 1)
}

// w3PairTransport is only the dial-policy seam; no test here drives its Noise methods.
type w3PairTransport struct{ peerSPKI []byte }

func (*w3PairTransport) Create(context.Context, string) error   { return nil }
func (*w3PairTransport) Claim(context.Context, string) error    { return nil }
func (*w3PairTransport) Send(context.Context, []byte) error     { return nil }
func (*w3PairTransport) Recv(context.Context) ([]byte, error)   { return nil, nil }
func (*w3PairTransport) Complete(context.Context, string) error { return nil }
func (*w3PairTransport) Close()                                 {}
func (p *w3PairTransport) PeerSPKI() []byte                     { return p.peerSPKI }

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

	conn, err := a.pairingDial(context.Background(), wss, w3Ceremony)
	if err != nil {
		t.Fatalf("pairingDial against a self-signed relay with no platform delegate: %v.\n"+
			"B45's fallback must still admit a pinned_spki-shaped relay, or a fresh phone can "+
			"never pair with one over wss:// at all", err)
	}
	defer conn.Close()
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

	conn, err := a.pairingDial(context.Background(), wss, w3Ceremony)
	if err != nil {
		t.Fatalf("pairingDial with a trusting platform delegate installed: %v", err)
	}
	defer conn.Close()
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
// relayV2DialPair is a package variable exactly so this can be proven without a live
// network dial or a way to fake a "public" DNS name resolving anywhere.
func TestADR016W3_PairingDialDoesNotFallBackForAPublicHost(t *testing.T) {
	a := w3TestApp(t)
	verifiedErr := errors.New("wrong-name or untrusted certificate")
	var calls []relayv2.Profile
	orig := relayV2DialPair
	t.Cleanup(func() { relayV2DialPair = orig })
	relayV2DialPair = func(_ context.Context, profile relayv2.Profile, _ string) (pairTransport, error) {
		calls = append(calls, profile)
		return nil, verifiedErr
	}

	_, err := a.pairingDial(context.Background(), "wss://relay.example.com:443/", w3Ceremony)
	if !errors.Is(err, verifiedErr) {
		t.Fatalf("pairingDial(public host) = %v, want the verified attempt's own error "+
			"propagated -- a public host must never fall back to B45's unverified policy", err)
	}
	if len(calls) != 1 {
		t.Fatalf("relayV2DialPair called %d time(s), want exactly 1: a public host's "+
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
	want := &w3PairTransport{}
	var calls []relayv2.Profile
	var ceremonies []string
	orig := relayV2DialPair
	t.Cleanup(func() { relayV2DialPair = orig })
	relayV2DialPair = func(_ context.Context, profile relayv2.Profile, ceremony string) (pairTransport, error) {
		calls = append(calls, profile)
		ceremonies = append(ceremonies, ceremony)
		return want, nil
	}

	conn, err := a.pairingDial(context.Background(), "wss://relay.example.com:443/", w3Ceremony)
	if err != nil {
		t.Fatalf("pairingDial(public host, verified succeeds) = %v, want nil", err)
	}
	if conn != want {
		t.Fatalf("pairingDial returned %v, want the verified attempt's own connection", conn)
	}
	if len(calls) != 1 {
		t.Fatalf("relayV2DialPair called %d time(s), want exactly 1: a successful verified "+
			"attempt on a public host must never also try the unverified fallback", len(calls))
	}
	if len(ceremonies) != 1 || ceremonies[0] != w3Ceremony {
		t.Fatalf("DialPair ceremonies = %q, want [%q]", ceremonies, w3Ceremony)
	}
}

// TestADR016W3_PairingDialStillFallsBackForAPrivateHost is the symmetric case, proven the
// same seamed way: an IP-literal (or otherwise private-classified) origin still gets the
// fallback W6 assigns to it -- a self-signed pinned_spki relay must still be pairable.
func TestADR016W3_PairingDialStillFallsBackForAPrivateHost(t *testing.T) {
	a := w3TestApp(t)
	verifiedErr := errors.New("self-signed relay: verified attempt cannot chain")
	var calls []relayv2.Profile
	var ceremonies []string
	orig := relayV2DialPair
	t.Cleanup(func() { relayV2DialPair = orig })
	relayV2DialPair = func(_ context.Context, profile relayv2.Profile, ceremony string) (pairTransport, error) {
		calls = append(calls, profile)
		ceremonies = append(ceremonies, ceremony)
		if len(calls) == 1 {
			return nil, verifiedErr
		}
		return &w3PairTransport{}, nil
	}

	conn, err := a.pairingDial(context.Background(), "wss://192.168.1.5:8443/", w3Ceremony)
	if err != nil {
		t.Fatalf("pairingDial(private host) = %v, want the fallback dial to succeed", err)
	}
	if conn == nil {
		t.Fatal("pairingDial(private host) returned a nil connection with no error")
	}
	if len(calls) != 2 {
		t.Fatalf("relayV2DialPair called %d time(s), want exactly 2: a private origin must "+
			"still retry unverified after a failed verified attempt", len(calls))
	}
	if len(ceremonies) != 2 || ceremonies[0] != w3Ceremony || ceremonies[1] != w3Ceremony {
		t.Fatalf("DialPair ceremonies = %q, want [%q %q]", ceremonies, w3Ceremony, w3Ceremony)
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

	conn, err := a.pairingDial(context.Background(), wss, w3Ceremony)
	if err != nil {
		t.Fatalf("pairingDial with a trusting platform delegate installed: %v", err)
	}
	defer conn.Close()

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
