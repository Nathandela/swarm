package main

// run(ctx, args) is directly table-testable without binding a port for every fail-fast
// validation path (flag.NewFlagSet, TLS gate, -db requirement), and this file also boots
// the real listener once in each transport mode to prove the wiring beyond the flag checks:
// -insecure-http actually serves plain HTTP and shuts down cleanly on context cancellation,
// and -tls-cert/-tls-key actually terminates TLS with MinVersion pinned at TLS 1.3 (PG-TR-1),
// verified by a live handshake rather than by reading the source.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
)

func dbPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "pushgw.db")
}

// --- fail-fast validation, no port ever bound --------------------------------------------

func TestRun_MissingDB_ReturnsError(t *testing.T) {
	err := run(context.Background(), []string{"-insecure-http"})
	if err == nil {
		t.Fatal("run accepted a missing -db")
	}
}

func TestRun_NeitherTLSNorInsecureHTTP_ReturnsError(t *testing.T) {
	err := run(context.Background(), []string{"-db", dbPath(t)})
	if err == nil {
		t.Fatal("run booted with neither -tls-cert/-tls-key nor -insecure-http set (PG-TR-1 requires HTTPS)")
	}
}

func TestRun_BothTLSAndInsecureHTTP_ReturnsError(t *testing.T) {
	err := run(context.Background(), []string{
		"-db", dbPath(t),
		"-tls-cert", "cert.pem", "-tls-key", "key.pem",
		"-insecure-http",
	})
	if err == nil {
		t.Fatal("run accepted both -tls-cert/-tls-key and -insecure-http")
	}
}

func TestRun_TLSCertWithoutKey_ReturnsError(t *testing.T) {
	err := run(context.Background(), []string{"-db", dbPath(t), "-tls-cert", "cert.pem"})
	if err == nil {
		t.Fatal("run accepted -tls-cert without -tls-key")
	}
}

func TestRun_TLSKeyWithoutCert_ReturnsError(t *testing.T) {
	err := run(context.Background(), []string{"-db", dbPath(t), "-tls-key", "key.pem"})
	if err == nil {
		t.Fatal("run accepted -tls-key without -tls-cert")
	}
}

func TestRun_BadTrustedProxyCIDR_ReturnsError(t *testing.T) {
	err := run(context.Background(), []string{
		"-db", dbPath(t), "-insecure-http", "-trusted-proxies", "not-a-cidr",
	})
	if err == nil {
		t.Fatal("run accepted a malformed -trusted-proxies CIDR")
	}
}

func TestRun_NonLoopbackAdminListen_ReturnsError(t *testing.T) {
	err := run(context.Background(), []string{
		"-db", dbPath(t), "-insecure-http", "-admin-listen", "0.0.0.0:8451",
	})
	if err == nil {
		t.Fatal("run accepted a non-loopback admin listener")
	}
}

func TestHTTPServer_BoundsUnauthenticatedConnections(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("HTTP timeouts are not all bounded: readHeader=%s read=%s write=%s idle=%s",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes <= 0 || srv.MaxHeaderBytes > 32<<10 {
		t.Fatalf("MaxHeaderBytes = %d, want 1..32768", srv.MaxHeaderBytes)
	}
}

func TestBackupRestoreSubcommandsRoundTripDBAndKey(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"-db", source, "-listen", "127.0.0.1:0", "-insecure-http"}); err != nil {
		t.Fatalf("initialize source: %v", err)
	}
	archive := filepath.Join(dir, "backup.tar")
	if err := run(context.Background(), []string{"backup", "-db", source, archive}); err != nil {
		t.Fatalf("backup subcommand: %v", err)
	}
	target := filepath.Join(dir, "target.db")
	if err := run(context.Background(), []string{"restore", "-db", target, archive}); err != nil {
		t.Fatalf("restore subcommand: %v", err)
	}
	for _, path := range []string{target, target + ".key"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat restored %s: %v", path, err)
		}
		if info.Size() == 0 {
			t.Fatalf("restored %s is empty", path)
		}
	}
}

func TestHealthcheckSubcommandRequiresReadyStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	if err := run(context.Background(), []string{"healthcheck", "-url", ts.URL}); err == nil {
		t.Fatal("healthcheck accepted 503")
	}
}

func TestRun_UnreadableFCMCredentials_ReturnsError(t *testing.T) {
	err := run(context.Background(), []string{
		"-db", dbPath(t), "-insecure-http",
		"-fcm-credentials", filepath.Join(t.TempDir(), "does-not-exist.json"),
	})
	if err == nil {
		t.Fatal("run accepted an unreadable -fcm-credentials path")
	}
}

// --- boot + clean shutdown, plain HTTP ----------------------------------------------------

// TestRun_InsecureHTTP_BootsAndShutsDownCleanly cancels its context before run is even
// called: run must still parse, build the store and sender, start the listener goroutine,
// reach the select, see ctx already Done, and return a nil error from a graceful
// http.Server.Shutdown -- deterministically, with no reliance on timing.
func TestRun_InsecureHTTP_BootsAndShutsDownCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run(ctx, []string{
		"-db", dbPath(t),
		"-listen", "127.0.0.1:0",
		"-insecure-http",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
}

// --- boot + clean shutdown, TLS mode -------------------------------------------------------

// generateSelfSignedCert writes an ECDSA P-256 self-signed leaf for 127.0.0.1 to dir,
// returning the cert and key PEM paths.
func generateSelfSignedCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("x509.CreateCertificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("x509.MarshalECPrivateKey: %v", err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

// freeTCPAddr picks a currently-free 127.0.0.1 port by binding and immediately releasing
// one, so -listen can name a concrete address the test can then dial.
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// dialTLSWithRetry waits for the gateway's listener to come up (ListenAndServeTLS runs in
// its own goroutine, asynchronously with run's caller) and returns the completed
// connection state, or fails the test after a bounded number of attempts.
func dialTLSWithRetry(t *testing.T, addr string, cfg *tls.Config) tls.ConnectionState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := tls.Dial("tcp", addr, cfg)
		if err == nil {
			state := conn.ConnectionState()
			_ = conn.Close()
			return state
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dial %s over TLS: %v", addr, lastErr)
	return tls.ConnectionState{}
}

// TestRun_TLSMode_PinsMinimumVersionAtTLS13 is PG-TR-1's floor, verified by a live
// handshake: a client offering only up to TLS 1.2 SHALL be refused, and a client offering
// TLS 1.3 SHALL be accepted -- against the gateway's REAL http.Server, not a config literal
// read off the source.
func TestRun_TLSMode_PinsMinimumVersionAtTLS13(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, dir)
	addr := freeTCPAddr(t)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, []string{
			"-db", dbPath(t),
			"-listen", addr,
			"-tls-cert", certPath,
			"-tls-key", keyPath,
		})
	}()

	state := dialTLSWithRetry(t, addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS13})
	if state.Version != tls.VersionTLS13 {
		t.Fatalf("negotiated TLS version = %#x, want TLS 1.3 (%#x)", state.Version, tls.VersionTLS13)
	}

	if _, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}); err == nil {
		t.Fatal("a client offering at most TLS 1.2 completed a handshake; PG-TR-1's floor is not enforced")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after context cancellation")
	}
}

// --- pure-function seams: fail-closed attestation and dev-mode sender ---------------------

// TestNotImplementedAttestor_AlwaysFailsClosed is PG-AUTH-13: with no Play Integrity client
// wired, every registration is refused retryably rather than an unattested installation
// ever being enrolled.
func TestNotImplementedAttestor_AlwaysFailsClosed(t *testing.T) {
	_, err := (notImplementedAttestor{}).Verify(context.Background(), "any-token")
	if err != pushgw.ErrAttestationUnavailable {
		t.Fatalf("Verify error = %v, want ErrAttestationUnavailable", err)
	}
}

// TestDevSender_AlwaysReturnsUpstreamUnavailable is the unset -fcm-credentials dev-mode
// contract: a wake attempted with no provider wired gets an honest, retryable refusal
// rather than a fake accept.
func TestDevSender_AlwaysReturnsUpstreamUnavailable(t *testing.T) {
	err := (devSender{}).Send(context.Background(), "any-token", []byte("any-envelope"))
	if err != pushgw.ErrUnavailable {
		t.Fatalf("Send error = %v, want ErrUnavailable", err)
	}
}
