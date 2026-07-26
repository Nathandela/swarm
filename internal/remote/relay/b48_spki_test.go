package relay

// ADR-007 B48: B45's ruling is amended. The pairing dial may run UNVERIFIED -- it is the
// dial that fetches the pin that would verify it -- but leaving the certificate unchecked
// also lowered the cost of B46's consent harvest from "hold a certificate valid for the
// operator's relay" to "be on the path". Unverified must therefore not mean UNOBSERVED:
// the presented certificate is recorded, and msg2 carries the machine's own RelaySPKIPin
// to compare it against.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// b48TLSFrontedRelay puts a TLS terminator in front of a plain relay, which is both the
// runbook's own topology and, from the phone's side, exactly the shape of an interceptor:
// a certificate the pairing dial has no way to check.
func b48TLSFrontedRelay(t *testing.T) (wssURL string, cert *x509.Certificate) {
	t.Helper()
	return b48Front(t, nil)
}

// b48OwnKeyFront is b48TLSFrontedRelay with a freshly minted key, because httptest's
// built-in TLS server reuses one certificate for every instance -- which would make an
// isolation test pass on two identical observations and prove nothing.
func b48OwnKeyFront(t *testing.T) (wssURL string, cert *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(48),
		Subject:      pkix.Name{CommonName: "b48-front"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	wss, _ := b48Front(t, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}},
	})
	return wss, leaf
}

func b48Front(t *testing.T, tlsCfg *tls.Config) (wssURL string, cert *x509.Certificate) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode = "off"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	target, err := url.Parse(strings.Replace(srv.URL(), "ws://", "http://", 1))
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	front := httptest.NewUnstartedServer(httputil.NewSingleHostReverseProxy(target))
	front.TLS = tlsCfg
	front.StartTLS()
	t.Cleanup(front.Close)
	return strings.Replace(front.URL, "https://", "wss://", 1), front.Certificate()
}

// TestB48_ThePairingDialRecordsTheCertificateItDidNotVerify. Without this the phone has
// nothing to compare MachinePayload.RelaySPKIPin against, and B48's amendment has no
// input at all.
func TestB48_ThePairingDialRecordsTheCertificateItDidNotVerify(t *testing.T) {
	wss, cert := b48TLSFrontedRelay(t)

	conn, err := DialRawSecure(testCtx(t), wss, PairingSecurity())
	if err != nil {
		t.Fatalf("pairing dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	want := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	got := conn.PeerSPKI()
	if !bytes.Equal(got, want[:]) {
		t.Fatalf("Conn.PeerSPKI() = %x, want %x.\n"+
			"  The pairing dial cannot VERIFY this certificate -- it is the dial that fetches the "+
			"pin that would -- but it must RECORD it, or the machine-authored pin in msg2 has "+
			"nothing to be compared with and B45's widening stands unmitigated.", got, want[:])
	}
}

// TestB48_TwoPairingDialsDoNotShareAnObservation. The policy value is passed by value and
// reused; the observation must belong to the CONNECTION. A shared slot would let one
// dial's certificate answer for another's, which is the same confusion the pin exists to
// prevent.
func TestB48_TwoPairingDialsDoNotShareAnObservation(t *testing.T) {
	wssA, certA := b48TLSFrontedRelay(t)
	wssB, certB := b48OwnKeyFront(t)
	sec := PairingSecurity()

	a, err := DialRawSecure(testCtx(t), wssA, sec)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := DialRawSecure(testCtx(t), wssB, sec)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	sumA := sha256.Sum256(certA.RawSubjectPublicKeyInfo)
	sumB := sha256.Sum256(certB.RawSubjectPublicKeyInfo)
	if bytes.Equal(sumA[:], sumB[:]) {
		t.Fatal("the two fronts share a key, so this test could not tell a crossed observation " +
			"from a correct one; b48OwnKeyFront exists to prevent exactly that")
	}
	if !bytes.Equal(a.PeerSPKI(), sumA[:]) || !bytes.Equal(b.PeerSPKI(), sumB[:]) {
		t.Fatalf("observations crossed between dials: A=%x (want %x) B=%x (want %x)",
			a.PeerSPKI(), sumA[:], b.PeerSPKI(), sumB[:])
	}
}

// TestB48_APinnedDialIsUnaffected. The recording arm belongs to the unverified branch
// only: a policy carrying a pin already REFUSES a mismatch, and must keep doing so rather
// than record and continue.
func TestB48_APinnedDialIsUnaffected(t *testing.T) {
	wss, cert := b48TLSFrontedRelay(t)

	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	wrong := sha256.Sum256([]byte("not the relay's key"))

	ok := Security{PinnedSPKISHA256: sum[:]}
	conn, err := DialRawSecure(testCtx(t), wss, ok)
	if err != nil {
		t.Fatalf("pinned dial with the matching pin: %v", err)
	}
	_ = conn.Close()

	bad := Security{PinnedSPKISHA256: wrong[:]}
	if _, err := DialRawSecure(testCtx(t), wss, bad); err == nil {
		t.Fatal("a pinned dial accepted a certificate that does not match its pin")
	}
}
