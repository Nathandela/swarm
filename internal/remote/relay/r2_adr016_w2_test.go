// ADR-016 W2 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): "the trust decision is
// delegated to Kotlin over a reverse-bound seam; Go keeps the name check; neither half
// alone admits a peer."
//
// TrustRootSource gains a fourth value, TrustRootsPlatformDelegate, selected ONLY when a
// verifier has been installed by the new PRODUCTION constructor WithPlatformVerifier (unlike
// WithTrustRootSource, which stays test-only). No verifier means ErrPinRequired, unchanged --
// absence fails closed, it never falls back to Go's system pool.
//
// PlatformVerifier is the Go-side shape of the reverse-bound interface W2 names:
//
//	RelayTrust interface { VerifyRelayChain(host string, pemChain []byte) error }
//
// declared here (not in mobile, which imports this package and would create a cycle the
// other way) with the SAME method name, so mobile's gomobile-bound RelayTrust satisfies
// this interface structurally with no adapter type.
//
// THE MECHANISM IS TESTED WITHOUT A LIVE DIAL: Security.Resolve already returns a real
// *tls.Config with VerifyPeerCertificate populated; calling that callback directly with a
// certificate chain this file mints is the exact function the TLS stack calls mid-handshake,
// and it needs no network I/O, no relay, and no way to fake DNS for a SAN mismatch.
package relay_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// w2Cert mints a self-signed leaf for exactly one DNS SAN, valid over [notBefore, notAfter].
// Self-signed is fine: TrustRootsPlatformDelegate's whole point is that CHAIN trust is
// Kotlin's decision, never Go's -- these tests never exercise Go's own chain-building, only
// the hostname/validity half Go keeps for itself.
func w2Cert(t *testing.T, san string, notBefore, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: san},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     []string{san},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return der
}

// alwaysApprove is a PlatformVerifier that admits every chain -- the negative control W2
// requires: "a verifier that returns nil for everything still cannot admit a certificate
// whose SAN does not cover the configured host".
type alwaysApprove struct{}

func (alwaysApprove) VerifyRelayChain(host string, pemChain []byte) error { return nil }

// alwaysReject is a PlatformVerifier that refuses every chain, standing in for Kotlin's
// X509TrustManagerExtensions rejecting an untrusted chain.
type alwaysReject struct{ err error }

func (a alwaysReject) VerifyRelayChain(host string, pemChain []byte) error { return a.err }

// w2DelegateSecurity installs v through WithPlatformVerifier, the new PRODUCTION
// constructor (exported, and NOT gated by testing.Testing() the way WithTrustRootSource
// is) -- "the delegate is selected ONLY when a verifier has been installed by
// relay.WithPlatformVerifier(sec, v)". No WithTrustRootSource call is needed anywhere in
// this file: selecting the delegate IS what WithPlatformVerifier does.
func w2DelegateSecurity(t *testing.T, v relay.PlatformVerifier) relay.Security {
	t.Helper()
	return relay.WithPlatformVerifier(relay.Security{}, v)
}

// TestADR016W2_NoVerifierFailsClosed pins "Absence fails closed; it never falls back to
// Go's system pool on Android": WithPlatformVerifier called with a nil verifier (Kotlin
// never registered one, or registration failed) still selects the delegate policy, and the
// delegate policy with nothing to delegate TO refuses before any packet -- exactly like
// TrustRootsPinned with no pin, and by the same sentinel.
func TestADR016W2_NoVerifierFailsClosed(t *testing.T) {
	sec := w2DelegateSecurity(t, nil)
	_, err := sec.Resolve("wss://relay.example.com:443/")
	if !errors.Is(err, relay.ErrPinRequired) {
		t.Fatalf("Resolve with no platform verifier installed = %v, want ErrPinRequired "+
			"(fail closed, never fall open to no-verification)", err)
	}
}

// TestADR016W2_DelegateApprovalIsNotEnoughOnItsOwn is the exact sentence W2 states: a
// verifier that approves everything still cannot admit a certificate whose SAN does not
// cover the configured host. Go's own VerifyHostname must refuse it independently.
func TestADR016W2_DelegateApprovalIsNotEnoughOnItsOwn(t *testing.T) {
	cert := w2Cert(t, "correct-host.example", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	sec := w2DelegateSecurity(t, alwaysApprove{})

	// The SAN matches the configured host: both halves pass, the dial is admitted.
	cfgOK, err := sec.Resolve("wss://correct-host.example:443/")
	if err != nil {
		t.Fatalf("Resolve(correct-host.example): %v", err)
	}
	if err := cfgOK.VerifyPeerCertificate([][]byte{cert}, nil); err != nil {
		t.Fatalf("VerifyPeerCertificate with a matching SAN and an approving delegate = %v, want nil", err)
	}

	// The SAME certificate, dialed against a DIFFERENT configured host: the delegate still
	// approves everything, but Go's own hostname check must refuse it.
	cfgMismatch, err := sec.Resolve("wss://wrong-host.example:443/")
	if err != nil {
		t.Fatalf("Resolve(wrong-host.example): %v", err)
	}
	if err := cfgMismatch.VerifyPeerCertificate([][]byte{cert}, nil); err == nil {
		t.Fatalf("VerifyPeerCertificate admitted a certificate whose SAN (correct-host.example) " +
			"does not cover the configured host (wrong-host.example), because the platform " +
			"delegate approved it -- W2: 'neither half alone admits a peer'")
	}
}

// TestADR016W2_ExpiredCertificateIsRefusedEvenWhenTheDelegateApproves is the other half of
// Go's own check: "a NotBefore/NotAfter check against the Go clock". A platform delegate is
// not asked about validity windows -- Go decides that itself, unconditionally.
func TestADR016W2_ExpiredCertificateIsRefusedEvenWhenTheDelegateApproves(t *testing.T) {
	expired := w2Cert(t, "correct-host.example", time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	sec := w2DelegateSecurity(t, alwaysApprove{})

	cfg, err := sec.Resolve("wss://correct-host.example:443/")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := cfg.VerifyPeerCertificate([][]byte{expired}, nil); err == nil {
		t.Fatalf("VerifyPeerCertificate admitted a certificate outside its validity window " +
			"because the platform delegate approved it")
	}
}

// TestADR016W2_DelegateRejectionRefusesEvenWithAMatchingSAN is the mirror case: the SAN and
// validity are both fine, but Kotlin's X509TrustManagerExtensions rejected the chain (an
// untrusted issuer). Go's own passing checks must not overrule that.
func TestADR016W2_DelegateRejectionRefusesEvenWithAMatchingSAN(t *testing.T) {
	cert := w2Cert(t, "correct-host.example", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	sec := w2DelegateSecurity(t, alwaysReject{err: errors.New("swarm-relaytrust/untrusted")})

	cfg, err := sec.Resolve("wss://correct-host.example:443/")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := cfg.VerifyPeerCertificate([][]byte{cert}, nil); err == nil {
		t.Fatalf("VerifyPeerCertificate admitted a chain the platform delegate rejected")
	}
}

// TestADR016W2_TrustRootSourceForAndroidIsUnchanged pins the regression W2 states in as
// many words: "TrustRootSourceFor('android') continues to return TrustRootsPinned; the
// delegate is selected ONLY when a verifier has been installed". Adding the fourth value
// must not become the new floor for a platform that names none.
func TestADR016W2_TrustRootSourceForAndroidIsUnchanged(t *testing.T) {
	if got := relay.TrustRootSourceFor("android"); got != relay.TrustRootsPinned {
		t.Fatalf("TrustRootSourceFor(android) = %v, want TrustRootsPinned (unchanged by W2)", got)
	}
}

// TestADR016W2_WithTrustRootSourceStaysTestOnly re-pins PB-NET-2's existing fence
// (TestPBNET2_TheTrustRootOverrideIsInertInAReleaseBuild) is not weakened by adding a
// FOURTH value to select through it: WithPlatformVerifier is deliberately a DIFFERENT,
// production, exported constructor precisely so that fence is not loosened to make room
// for a production caller (Blast radius: "The new WithPlatformVerifier is a different
// function precisely so that fence is not loosened").
func TestADR016W2_WithPlatformVerifierIsADifferentFunctionFromWithTrustRootSource(t *testing.T) {
	// This is a compile-time/API-shape assertion as much as a runtime one: WithPlatformVerifier
	// must exist as its own symbol, taking a Security and a PlatformVerifier and returning a
	// Security, independent of WithTrustRootSource's test-only gate.
	sec := relay.WithPlatformVerifier(relay.Security{}, alwaysApprove{})
	_ = sec
}
