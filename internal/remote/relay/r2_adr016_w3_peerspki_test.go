package relay

// ADR-016 W3 review-round fix (bd agents-tracker-hggx.3.5): "Conn.PeerSPKI records the
// presented SPKI on every dial it owns, unchanged" and the Blast-radius invariant "B48's
// capture (Conn.PeerSPKI) on both policies ... The mechanism is not removed on either."
//
// tlsConfig's unverifiedTLS branch was the ONLY branch that ever called observer.record --
// the pinned, platform-delegate and system-default branches built a *tls.Config with no
// recording hook at all, so a VERIFIED dial (any of those three branches) always left
// Conn.PeerSPKI empty. That silently dropped B48's capture-then-compare on exactly the
// dials it must cover: a pairing dial that succeeds through the verified attempt (ADR-016
// W3's own two-attempt pairingDial) recorded nothing for a pinned_spki machine's published
// pin to be compared against.
//
// This is a WHITE-BOX test (package relay, not relay_test): it calls Security.tlsConfig
// directly and invokes the returned VerifyPeerCertificate hook with a minted certificate,
// which needs no live dial and no way to fake a system-root chain or an Android GOOS.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

// w3peerspkiCert mints a self-signed leaf covering 127.0.0.1, returning its DER and its
// SHA-256 SPKI digest -- the value B48's observer is supposed to end up holding.
func w3peerspkiCert(t *testing.T) (der []byte, spki [32]byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "w3-peerspki"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return der, sha256.Sum256(cert.RawSubjectPublicKeyInfo)
}

// w3acceptAllDelegate is a PlatformVerifier that approves every chain, standing in for a
// Kotlin X509TrustManagerExtensions that trusts the peer's issuer.
type w3acceptAllDelegate struct{}

func (w3acceptAllDelegate) VerifyRelayChain(string, []byte) error { return nil }

// TestADR016W3_PeerSPKIIsRecordedOnEveryVerifiedBranch is the direct fix-verification test:
// every tlsConfig branch that admits a VERIFIED dial -- the platform-trust-source default,
// the platform delegate, and the pin itself -- must still populate the B48 observer, exactly
// as the unverifiedTLS (pairing) branch always has. A branch that builds a *tls.Config with
// no VerifyPeerCertificate hook at all can never record anything.
func TestADR016W3_PeerSPKIIsRecordedOnEveryVerifiedBranch(t *testing.T) {
	der, spki := w3peerspkiCert(t)

	for _, tc := range []struct {
		name string
		sec  Security
	}{
		// The platform's stated trust-root source (TrustRootsSystem on the desktop this
		// suite runs on): no pin, no delegate installed.
		{"system default", Security{}},
		// ADR-016 W2's delegate branch.
		{"platform delegate", WithPlatformVerifier(Security{}, w3acceptAllDelegate{})},
		// The pin itself -- matching, so the callback both records AND admits the peer,
		// proving the two are not exclusive.
		{"pinned (matching)", Security{PinnedSPKISHA256: spki[:]}},
		// A MISMATCHED pin must still record even though it refuses: the observation is
		// what lets a caller (mobile's pairingDial + checkRelayPin) compare a verified
		// dial's own presented key against the machine's published pin. Recording must not
		// be conditioned on the dial's own verdict.
		{"pinned (mismatched)", Security{PinnedSPKISHA256: sha256Of("not the peer's key")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sec := tc.sec
			obs := &spkiObserver{}
			sec.observer = obs
			cfg, err := sec.tlsConfig("127.0.0.1")
			if err != nil {
				t.Fatalf("tlsConfig: %v", err)
			}
			if cfg.VerifyPeerCertificate == nil {
				t.Fatalf("branch %q built a *tls.Config with no VerifyPeerCertificate hook; "+
					"PeerSPKI can never be recorded on this branch", tc.name)
			}
			_ = cfg.VerifyPeerCertificate([][]byte{der}, nil) // verdict not the point here
			if got := obs.get(); len(got) == 0 {
				t.Fatalf("branch %q: observer recorded nothing; Conn.PeerSPKI() would stay empty "+
					"on a verified dial, exactly the regression ADR-016 W3 requires the capture to "+
					"survive on every policy", tc.name)
			}
		})
	}
}

func sha256Of(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
