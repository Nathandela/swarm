// PB-OPS-5 (FAILING FIRST): the certificate pin must survive renewal.
//
// Android's trust-root source is TrustRootsPinned (relay/security.go:65-70), so on the
// handset there is no root-store fallback: the pin is the whole of relay TLS verification.
// A pin over the full leaf DER therefore breaks on every reissue, and Let's Encrypt reissues
// every 60-90 days. That is not a hardening gap, it is a product that stops working every two
// months, so it is pinned here as BEHAVIOUR rather than described in a runbook.
//
// WHAT THESE TESTS ESTABLISH, AND THE ONE THING THEY REFUTE. The requirement says an SPKI
// pin "survives renewal at the same security level". That is true only when the renewal
// REUSES THE KEY. Most ACME clients generate a fresh keypair per renewal by default, and a
// fresh key is a fresh SubjectPublicKeyInfo, which breaks an SPKI pin exactly as a reissue
// breaks a DER pin. So the last test below is deliberately an assertion that the SPKI pin
// FAILS: key reuse is a load-bearing operational requirement, not a footnote, and a runbook
// that omits it leaves the operator to discover it at the first renewal.
package relay_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// renewalKey mints one ECDSA P-256 key. It is the key an operator either REUSES across a
// renewal or lets their ACME client rotate; both cases are exercised below.
func renewalKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate relay key: %v", err)
	}
	return key
}

// issueRelayCert self-signs a server certificate for key, valid until notAfter. Two calls
// with the same key and different validity windows are what a renewal looks like on the wire:
// same SubjectPublicKeyInfo, different serial, different DER.
func issueRelayCert(t *testing.T, key *ecdsa.PrivateKey, notAfter time.Time) tls.Certificate {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("issueRelayCert serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "swarm-relay.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		DNSNames:              []string{"localhost", "swarm-relay.test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("issueRelayCert create: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// spkiPin is the value an operator computes for the runbook:
//
//	openssl x509 -in relay.crt -pubkey -noout |
//	  openssl pkey -pubin -outform der | openssl dgst -sha256 -binary
//
// i.e. SHA-256 over the certificate's SubjectPublicKeyInfo, NOT over the whole leaf.
func spkiPin(t *testing.T, cert tls.Certificate) []byte {
	t.Helper()
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("spkiPin parse: %v", err)
	}
	sum := sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	return sum[:]
}

// startTLSRelayWithCert fronts the REAL relay with a TLS terminator serving cert, and
// returns the wss:// URL. It is startTLSRelay with the certificate under the test's control,
// which is the only way a renewal (two certificates, one key) can be expressed at all.
func startTLSRelayWithCert(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	_, plain := startRelay(t, nil)
	target, err := url.Parse(strings.Replace(plain, "ws://", "http://", 1))
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	front := httptest.NewUnstartedServer(httputil.NewSingleHostReverseProxy(target))
	front.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	front.StartTLS()
	t.Cleanup(front.Close)
	return strings.Replace(front.URL, "https://", "wss://", 1)
}

// TestPBOPS5_DERPinIsBrokenByRenewal is the hazard itself, as a passing assertion rather
// than a sentence: the operator pins the leaf they have today, the certificate is reissued
// from the SAME key, and the handset can no longer reach its own relay.
//
// It asserts the CURRENT behaviour deliberately. PB-OPS-5's acceptance offers "either the pin
// is SPKI-based or the operational consequence is documented and accepted", and a documented
// consequence nothing exercises is the class of claim this phase keeps finding to be false.
func TestPBOPS5_DERPinIsBrokenByRenewal(t *testing.T) {
	key := renewalKey(t)
	today := issueRelayCert(t, key, time.Now().Add(90*24*time.Hour))
	renewed := issueRelayCert(t, key, time.Now().Add(180*24*time.Hour))

	wss := startTLSRelayWithCert(t, renewed)
	pub, priv := newRelayAuthKey(t)

	c, err := relay.DialSecure(testCtx(t), wss, authFor(pub, priv),
		relay.Security{PinnedCert: today.Certificate[0]})
	if c != nil {
		_ = c.Close()
	}
	if !errors.Is(err, relay.ErrPinMismatch) {
		t.Fatalf("a DER pin over the pre-renewal leaf accepted the reissued certificate (err=%v); "+
			"if this now passes, the renewal hazard PB-OPS-5 exists for has been closed somewhere "+
			"else and this test is measuring nothing", err)
	}
}

// TestPBOPS5_SPKIPinSurvivesRenewalWithTheSameKey is the fix. Same relay, same key, reissued
// certificate: the pin the operator wrote down before the renewal still admits the relay, and
// the dial completes all the way through the relay-auth handshake against the real server.
func TestPBOPS5_SPKIPinSurvivesRenewalWithTheSameKey(t *testing.T) {
	key := renewalKey(t)
	today := issueRelayCert(t, key, time.Now().Add(90*24*time.Hour))
	renewed := issueRelayCert(t, key, time.Now().Add(180*24*time.Hour))

	wss := startTLSRelayWithCert(t, renewed)
	pub, priv := newRelayAuthKey(t)

	c, err := relay.DialSecure(testCtx(t), wss, authFor(pub, priv),
		relay.Security{PinnedSPKISHA256: spkiPin(t, today)})
	if err != nil {
		t.Fatalf("SPKI pin taken before the renewal refused the reissued certificate: %v", err)
	}
	_ = c.Close()
}

// TestPBOPS5_SPKIPinRefusesAnUnrelatedCertificate holds the security level constant. Pinning
// is a whitelist of one public key; an equally self-signed impostor with its own key is
// refused exactly as it is under the DER pin, so surviving renewal is not bought by
// weakening verification.
func TestPBOPS5_SPKIPinRefusesAnUnrelatedCertificate(t *testing.T) {
	ours := issueRelayCert(t, renewalKey(t), time.Now().Add(90*24*time.Hour))
	impostor := issueRelayCert(t, renewalKey(t), time.Now().Add(90*24*time.Hour))

	wss := startTLSRelayWithCert(t, impostor)
	pub, priv := newRelayAuthKey(t)

	c, err := relay.DialSecure(testCtx(t), wss, authFor(pub, priv),
		relay.Security{PinnedSPKISHA256: spkiPin(t, ours)})
	if c != nil {
		_ = c.Close()
	}
	if !errors.Is(err, relay.ErrPinMismatch) {
		t.Fatalf("SPKI pin accepted a certificate holding a different key (err=%v)", err)
	}
}

// TestPBOPS5_SPKIPinIsBrokenByARenewalThatROTATESTheKey refutes the unqualified form of
// PB-OPS-5's own claim, and is the reason the runbook has a key-reuse step.
//
// certbot renews with a fresh keypair unless told otherwise (`--reuse-key`, or a
// `reuse_key = True` renewal profile). Under a rotating key an SPKI pin breaks on exactly the
// same 60-90 day cadence as the DER pin it replaced, so "pin the SPKI" is a NECESSARY HALF of
// the fix and not the fix. This test exists so that pairing cannot be quietly dropped.
func TestPBOPS5_SPKIPinIsBrokenByARenewalThatROTATESTheKey(t *testing.T) {
	today := issueRelayCert(t, renewalKey(t), time.Now().Add(90*24*time.Hour))
	rekeyed := issueRelayCert(t, renewalKey(t), time.Now().Add(180*24*time.Hour))

	wss := startTLSRelayWithCert(t, rekeyed)
	pub, priv := newRelayAuthKey(t)

	c, err := relay.DialSecure(testCtx(t), wss, authFor(pub, priv),
		relay.Security{PinnedSPKISHA256: spkiPin(t, today)})
	if c != nil {
		_ = c.Close()
	}
	if !errors.Is(err, relay.ErrPinMismatch) {
		t.Fatalf("SPKI pin accepted a renewal that rotated the key (err=%v): either the pin is "+
			"not over the SubjectPublicKeyInfo, or it is not being checked at all", err)
	}
}

// TestPBOPS5_ErrPinRequiredNamesTheRenewalSafeForm is a documentation assertion with teeth.
//
// On a pinning-only platform an unpinned dial is refused with ErrPinRequired, and that error
// is the ONLY instruction most operators will ever read about how to configure the pin. While
// it named PinnedCert alone it steered every one of them into the form that breaks every 90
// days. The error must name the renewal-safe field.
func TestPBOPS5_ErrPinRequiredNamesTheRenewalSafeForm(t *testing.T) {
	msg := relay.ErrPinRequired.Error()
	if !strings.Contains(msg, "PinnedSPKISHA256") {
		t.Fatalf("ErrPinRequired = %q; it must name PinnedSPKISHA256, the form that survives a "+
			"certificate renewal, or it teaches operators the fragile one", msg)
	}
}

// TestPBOPS5_AnSPKIPinAloneSatisfiesTheAndroidPinRequirement closes the gap between the two
// mechanisms. TrustRootsPinned refuses a dial that supplies no pin; if that check only ever
// looked at PinnedCert, an operator following the renewal-safe advice would configure the SPKI
// pin and still be refused before a packet was sent. Both pins are dialable on their own.
func TestPBOPS5_AnSPKIPinAloneSatisfiesTheAndroidPinRequirement(t *testing.T) {
	cert := issueRelayCert(t, renewalKey(t), time.Now().Add(90*24*time.Hour))
	wss := startTLSRelayWithCert(t, cert)
	pub, priv := newRelayAuthKey(t)

	for name, sec := range map[string]relay.Security{
		"SPKI pin only": {PinnedSPKISHA256: spkiPin(t, cert)},
		"DER pin only":  {PinnedCert: cert.Certificate[0]},
		"both pins":     {PinnedCert: cert.Certificate[0], PinnedSPKISHA256: spkiPin(t, cert)},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := relay.DialSecure(testCtx(t), wss, authFor(pub, priv), sec)
			if err != nil {
				t.Fatalf("%s was refused: %v", name, err)
			}
			_ = c.Close()
		})
	}
}
