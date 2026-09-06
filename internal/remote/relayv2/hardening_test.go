package relayv2

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
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/coder/websocket"
)

func TestResponseRequiresTypedFieldsAndCanonicalIncarnation(t *testing.T) {
	for _, body := range []string{
		`{"v":2,"type":"APPENDED","request_id":"r","peer_rid":"88564c8ede170d2ed321e21e61354184","generation":"1","cursor":"1"}`,
		`{"v":2,"type":"APPENDED","request_id":"r","peer_rid":"88564c8ede170d2ed321e21e61354184","generation":"1","cursor":"1","deduped":null}`,
		`{"v":2,"v":2,"type":"REVOKED","request_id":"r","peer_rid":"88564c8ede170d2ed321e21e61354184"}`,
		`{"v":2,"type":"ERROR","request_id":"r","code":"` + strings.Repeat("x", 1000) + `"}`,
	} {
		if _, err := decodeFrame([]byte(body)); err == nil {
			t.Fatalf("accepted incomplete/unbounded response: %.80s", body)
		}
	}
	if validIncarnation("AAAAAAAAAAAAAAAAAAAAAB") {
		t.Fatal("accepted non-canonical 16-byte incarnation")
	}
	if !validIncarnation(base64.RawURLEncoding.EncodeToString(make([]byte, 16))) {
		t.Fatal("rejected canonical 16-byte incarnation")
	}
}

func TestSecurityRejectsRoutableCleartext(t *testing.T) {
	if _, _, err := secureHTTPClient(relay.Security{}, "ws://relay.example/v2/ws"); err == nil {
		t.Fatal("zero-value production security accepted routable cleartext")
	}
	if _, _, err := secureHTTPClient(relay.Security{AllowLoopbackCleartext: true}, "ws://127.0.0.1:8790/v2/ws"); err != nil {
		t.Fatalf("explicit test-only loopback policy rejected: %v", err)
	}
}

func TestSecureHTTPClientTLSHardening(t *testing.T) {
	ceremony := strings.Repeat("a", 32)
	var untrustedHits atomic.Int32
	untrusted := newTLSWebsocketServer(t, func() { untrustedHits.Add(1) })
	defer untrusted.Close()
	untrustedWSS := "wss" + strings.TrimPrefix(untrusted.URL, "https")
	if _, err := DialPair(testDialContext(t), Profile{RelayURL: untrustedWSS}, ceremony); err == nil {
		t.Fatal("default trust accepted a self-signed relay")
	}
	if got := untrustedHits.Load(); got != 0 {
		t.Fatalf("HTTP handler ran %d times before self-signed certificate refusal", got)
	}

	t.Run("pairing bootstrap captures peer SPKI", func(t *testing.T) {
		server := newTLSWebsocketServer(t, nil)
		defer server.Close()
		wss := "wss" + strings.TrimPrefix(server.URL, "https")
		transport, err := DialPair(testDialContext(t), Profile{RelayURL: wss, Security: relay.PairingSecurity()}, ceremony)
		if err != nil {
			t.Fatal(err)
		}
		defer transport.Close()
		want := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
		if got := transport.PeerSPKI(); !bytes.Equal(got, want[:]) {
			t.Fatalf("PeerSPKI = %x, want %x", got, want)
		}
	})

	t.Run("TLS redirect cannot downgrade to cleartext", func(t *testing.T) {
		var plaintextHits atomic.Int32
		plaintext := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			plaintextHits.Add(1)
		}))
		defer plaintext.Close()
		redirect := httptest.NewTLSServer(http.RedirectHandler(plaintext.URL, http.StatusFound))
		defer redirect.Close()
		wss := "wss" + strings.TrimPrefix(redirect.URL, "https")
		pin := sha256.Sum256(redirect.Certificate().RawSubjectPublicKeyInfo)
		if _, err := DialPair(testDialContext(t), Profile{RelayURL: wss, Security: relay.Security{PinnedSPKISHA256: pin[:]}}, ceremony); !errors.Is(err, relay.ErrCleartextRefused) {
			t.Fatalf("TLS-to-cleartext redirect = %v, want ErrCleartextRefused", err)
		}
		if got := plaintextHits.Load(); got != 0 {
			t.Fatalf("cleartext redirect target received %d requests", got)
		}
	})

	t.Run("observers are isolated and return copies", func(t *testing.T) {
		servers := []*httptest.Server{newTLSWebsocketServer(t, nil), newTLSWebsocketServer(t, nil)}
		defer servers[0].Close()
		defer servers[1].Close()
		transports := make([]*PairTransport, 2)
		for i, server := range servers {
			wss := "wss" + strings.TrimPrefix(server.URL, "https")
			transport, err := DialPair(testDialContext(t), Profile{RelayURL: wss, Security: relay.PairingSecurity()}, ceremony)
			if err != nil {
				t.Fatal(err)
			}
			transports[i] = transport
			defer transport.Close()
			want := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
			if got := transport.PeerSPKI(); !bytes.Equal(got, want[:]) {
				t.Fatalf("PairTransport %d PeerSPKI = %x, want %x", i, got, want)
			}
		}
		if bytes.Equal(transports[0].PeerSPKI(), transports[1].PeerSPKI()) {
			t.Fatal("distinct TLS keys produced the same observation")
		}
		copy := transports[0].PeerSPKI()
		copy[0] ^= 0xff
		if bytes.Equal(copy, transports[0].PeerSPKI()) {
			t.Fatal("PairTransport.PeerSPKI returned mutable internal storage")
		}
	})

	t.Run("wrong pin", func(t *testing.T) {
		wrong := sha256.Sum256([]byte("wrong relay key"))
		if _, err := DialPair(testDialContext(t), Profile{RelayURL: untrustedWSS, Security: relay.Security{PinnedSPKISHA256: wrong[:]}}, ceremony); !errors.Is(err, relay.ErrPinMismatch) {
			t.Fatalf("wrong SPKI pin = %v, want ErrPinMismatch", err)
		}
	})
}

func newTLSWebsocketServer(t *testing.T, hit func()) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hit != nil {
			hit()
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		<-r.Context().Done()
	}))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	return server
}

func testDialContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSlowConsumerCannotBlockResponsePump(t *testing.T) {
	wrote := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		payload := base64.RawURLEncoding.EncodeToString(make([]byte, maxQueuedEventBytes/2))
		for i := 0; i < 3; i++ {
			body := fmt.Sprintf(`{"v":2,"type":"DELIVER","request_id":"delivery-%d","peer_rid":"88564c8ede170d2ed321e21e61354184","generation":"1","incarnation":"AAAAAAAAAAAAAAAAAAAAAA","cursor":"%d","msg_id":"m%d","ciphertext":"%s"}`, i, i+1, i, payload)
			if err := ws.Write(context.Background(), websocket.MessageText, []byte(body)); err != nil {
				return
			}
		}
		close(wrote)
		<-r.Context().Done()
	}))
	defer server.Close()
	c, err := dialRaw(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case <-wrote:
	case <-time.After(time.Second):
		t.Fatal("server did not write test deliveries")
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("event byte overflow blocked the reader instead of closing")
	}
}

func TestClientBounds(t *testing.T) {
	if defaultCallTimeout <= 0 || DefaultDialTimeout <= 0 || maxPendingRequests <= 0 || maxPendingRequests > 64 {
		t.Fatal("client request/dial bounds are missing or excessive")
	}
}
