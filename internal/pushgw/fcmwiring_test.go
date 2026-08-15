package pushgw_test

// The FCM leg's REAL sender, wired end to end (task requirement: "the real sender wired
// but tested against a fake"). Every other file in this suite drives the gateway's policy
// (auth, quotas, error mapping, retention) against an in-memory fakeSender so those tests
// stay fast and provider-agnostic; this file is the one place that constructs the actual
// internal/remote/push.FCM sender via pushgw.NewFCMSender and proves the WHOLE pipeline —
// gateway handler -> pushgw.WakeSender -> push.FCM -> HTTP -> "FCM" — forwards the 74
// received octets byte-identical and produces the content-free request shape PG-TEST-7
// requires (one data key, no notification block, high priority, token not topic).
//
// SCOPE HONESTY, same rule as internal/remote/push/fcm_test.go: the fake FCM server below
// is a loopback httptest.Server, not Google. No request in this file, or anywhere else in
// this suite, ever reaches a real endpoint (PB-E2E-5 remains DEFERRED).
//
// RED: package pushgw has no implementation yet; every pushgw.* reference below is an
// undefined symbol.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/pushgw"
	"github.com/Nathandela/swarm/internal/remote/push"
)

// fakeFCMEndpoint is a loopback double for Google's OAuth token endpoint and the FCM v1
// send endpoint, sufficient to drive a real push.FCM sender without ever verifying the
// signed assertion (which is exactly what the real Google endpoint's protocol boundary
// looks like from a sender's point of view; the assertion's own correctness is
// internal/remote/push's test responsibility, not this bead's).
type fakeFCMEndpoint struct {
	srv *httptest.Server

	mu      sync.Mutex
	sendReq map[string]any // the last decoded FCM v1 request body
}

func newFakeFCMEndpoint(t *testing.T) *fakeFCMEndpoint {
	t.Helper()
	f := &fakeFCMEndpoint{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fake-access-token","expires_in":3600}`))
	})
	mux.HandleFunc("/v1/projects/swarm-pushgw-test/messages:send", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.sendReq = body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"projects/swarm-pushgw-test/messages/fake-message-id"}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFCMEndpoint) lastSend() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendReq
}

// testServiceAccountJSON builds a syntactically real, freshly-keyed service-account
// document. It authorizes nothing anywhere: fakeFCMEndpoint never verifies the assertion.
func testServiceAccountJSON(t *testing.T, tokenURI string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	doc := map[string]string{
		"type":           "service_account",
		"project_id":     "swarm-pushgw-test",
		"private_key_id": "kid-1",
		"private_key":    string(pemBytes),
		"client_email":   "pusher@swarm-pushgw-test.iam.gserviceaccount.com",
		"token_uri":      tokenURI,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal service account: %v", err)
	}
	return b
}

// TestFCMSender_RealPipelineForwardsByteIdenticalAndContentFree wires the ACTUAL
// internal/remote/push sender through pushgw.NewFCMSender and asserts, against the
// loopback fake FCM endpoint, both PG-SUB-1 (byte-identical) and PG-TEST-7 (content-free
// schema: one data key, no notification, high priority, token not topic).
func TestFCMSender_RealPipelineForwardsByteIdenticalAndContentFree(t *testing.T) {
	fake := newFakeFCMEndpoint(t)
	acct, err := push.LoadServiceAccount(testServiceAccountJSON(t, fake.srv.URL+"/token"))
	if err != nil {
		t.Fatalf("LoadServiceAccount: %v", err)
	}
	fcmSender, err := push.NewFCM(push.FCMConfig{
		Account:    acct,
		BaseURL:    fake.srv.URL,
		RetryDelay: 0, // no real sleeps in the suite
	})
	if err != nil {
		t.Fatalf("push.NewFCM: %v", err)
	}

	h := newHarness(t, func(cfg *pushgw.Config) {
		cfg.Sender = pushgw.NewFCMSender(fcmSender)
	})
	r := registerInstallation(t, h, "fcm-token-real-pipeline")
	a := allocateAddress(t, h, r)
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())

	resp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, resp, http.StatusOK)

	sent := fake.lastSend()
	if sent == nil {
		t.Fatalf("the fake FCM endpoint received no request")
	}
	message, _ := sent["message"].(map[string]any)
	if message == nil {
		t.Fatalf("request had no top-level \"message\": %#v", sent)
	}
	if message["token"] != r.fcmToken {
		t.Fatalf("message.token = %v, want the registered fcm_token %q", message["token"], r.fcmToken)
	}
	if _, hasNotification := message["notification"]; hasNotification {
		t.Fatalf("message carries a notification block; the wake must be data-only")
	}
	android, _ := message["android"].(map[string]any)
	if android == nil || android["priority"] != "high" {
		t.Fatalf("message.android.priority = %v, want \"high\" (ADR-007 B16)", android)
	}
	data, _ := message["data"].(map[string]any)
	if len(data) != 1 {
		t.Fatalf("message.data has %d keys, want exactly 1: %#v", len(data), data)
	}
	var encoded string
	for _, v := range data {
		encoded, _ = v.(string)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("data value is not base64: %v", err)
	}
	if string(decoded) != string(env[:]) {
		t.Fatalf("FCM received %x, want the byte-identical 74-octet envelope %x", decoded, env[:])
	}
}

// realFCMSenderAgainst wires the actual internal/remote/push sender at pushgw.WakeSender
// against a loopback double whose /messages:send handler is sendHandler -- never Google
// (PB-E2E-5 stays deferred).
func realFCMSenderAgainst(t *testing.T, sendHandler http.HandlerFunc) pushgw.WakeSender {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fake-access-token","expires_in":3600}`))
	})
	mux.HandleFunc("/v1/projects/swarm-pushgw-test/messages:send", sendHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	acct, err := push.LoadServiceAccount(testServiceAccountJSON(t, srv.URL+"/token"))
	if err != nil {
		t.Fatalf("LoadServiceAccount: %v", err)
	}
	fcmSender, err := push.NewFCM(push.FCMConfig{Account: acct, BaseURL: srv.URL, RetryDelay: 0})
	if err != nil {
		t.Fatalf("push.NewFCM: %v", err)
	}
	return pushgw.NewFCMSender(fcmSender)
}

// TestFCMSender_NonUnregisteredFourXX_ReturnsErrRefused proves the adapter reserves
// ErrRefused (-> upstream_refused, terminal per §6.4) for FCM's own affirmatively
// identified non-retryable 4xx refusal -- the ONE shape section 4 scopes it to.
func TestFCMSender_NonUnregisteredFourXX_ReturnsErrRefused(t *testing.T) {
	sender := realFCMSenderAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"status":"INVALID_ARGUMENT","message":"bad token"}}`))
	})
	err := sender.Send(context.Background(), "some-fcm-token", make([]byte, 74))
	if !errors.Is(err, pushgw.ErrRefused) {
		t.Fatalf("Send returned %v, want ErrRefused", err)
	}
}

// TestFCMSender_ContextCanceled_DoesNotBecomeATerminalRefusal is the regression this
// finding fixes: a gateway-side transport fault (here, an already-canceled context, which
// push.FCM's Push returns as ctx.Err() before ever dialing) must NOT be misclassified as
// upstream_refused (terminal) or push_token_unregistered -- it must fall through to the
// caller's default `internal` (500, retryable) arm, i.e. match neither sentinel here.
func TestFCMSender_ContextCanceled_DoesNotBecomeATerminalRefusal(t *testing.T) {
	sender := realFCMSenderAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("the send endpoint must never be reached for an already-canceled context")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := sender.Send(ctx, "some-fcm-token", make([]byte, 74))
	if err == nil {
		t.Fatalf("Send returned nil for a canceled context")
	}
	if errors.Is(err, pushgw.ErrRefused) {
		t.Fatalf("canceled context misclassified as ErrRefused (terminal, non-retryable): %v", err)
	}
	if errors.Is(err, pushgw.ErrUnregistered) {
		t.Fatalf("canceled context misclassified as ErrUnregistered: %v", err)
	}
}
