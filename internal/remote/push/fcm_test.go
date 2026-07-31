package push

// Failing-first tests for PB-PUSH-2 (an FCM v1 sender implementing the relay's push
// seam) and the credential half of PB-PUSH-5 (missing/invalid credentials degrade
// gracefully and loudly).
//
// SCOPE HONESTY -- READ THIS BEFORE ADDING ANYTHING HERE. There is no Google account in
// this project and PB-E2E-5, the physical-handset gate, is DEFERRED and may NOT be
// reclassified. Every request below goes to an httptest.Server on loopback. These tests
// model the FCM v1 PROTOCOL -- the request the sender emits, the OAuth exchange it
// performs, which failures it retries, and which response prunes a token. They model
// NOTHING about delivery: not that Google would accept the request, not that a handset
// would receive it, not that Doze would let it through. No test, comment or evidence
// line derived from this file may claim otherwise.
//
// RED is undefined-only: package push does not exist yet.

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// --- fake provider -----------------------------------------------------------

// fakeFCM is a loopback stand-in for the two Google endpoints the sender talks to: the
// OAuth token endpoint and the FCM v1 send endpoint. It is a PROTOCOL fixture, not a
// delivery fixture -- it records requests and answers with canned responses.
type fakeFCM struct {
	mu sync.Mutex

	tokenRequests []map[string]string // form values of each OAuth exchange
	sendRequests  []sentRequest

	// sendStatus is consumed one per send attempt; the last entry repeats.
	sendStatus []int
	sendBody   map[int]string // status -> response body
	accessTTL  time.Duration
	tokenSeq   int

	srv *httptest.Server
}

type sentRequest struct {
	path   string
	method string
	bearer string
	body   map[string]any
}

func newFakeFCM(t *testing.T) *fakeFCM {
	t.Helper()
	f := &fakeFCM{accessTTL: time.Hour, sendBody: map[int]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		vals := map[string]string{}
		for k := range r.Form {
			vals[k] = r.Form.Get(k)
		}
		f.mu.Lock()
		f.tokenRequests = append(f.tokenRequests, vals)
		f.tokenSeq++
		access := "access-token-" + string(rune('0'+f.tokenSeq))
		ttl := int(f.accessTTL.Seconds())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + access + `","token_type":"Bearer","expires_in":` + itoa(ttl) + `}`))
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.sendRequests = append(f.sendRequests, sentRequest{
			path:   r.URL.Path,
			method: r.Method,
			bearer: r.Header.Get("Authorization"),
			body:   body,
		})
		status := http.StatusOK
		if n := len(f.sendStatus); n > 0 {
			i := len(f.sendRequests) - 1
			if i >= n {
				i = n - 1
			}
			status = f.sendStatus[i]
		}
		respBody := f.sendBody[status]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if respBody == "" {
			respBody = `{}`
		}
		_, _ = w.Write([]byte(respBody))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func (f *fakeFCM) sends() []sentRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentRequest(nil), f.sendRequests...)
}

func (f *fakeFCM) tokenExchanges() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.tokenRequests...)
}

// testServiceAccount builds a syntactically real service-account document backed by a
// freshly generated RSA key. It authorises nothing anywhere: the fake endpoint never
// verifies the assertion, and no Google project exists.
func testServiceAccount(t *testing.T, tokenURI string) []byte {
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
		"project_id":     "swarm-test-project",
		"private_key_id": "kid-1",
		"private_key":    string(pemBytes),
		"client_email":   "pusher@swarm-test-project.iam.gserviceaccount.com",
		"token_uri":      tokenURI,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal service account: %v", err)
	}
	return b
}

type fcmHarness struct {
	sender *FCM
	fake   *fakeFCM
	clk    *manualClock
}

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newFCMHarness(t *testing.T, fake *fakeFCM) *fcmHarness {
	t.Helper()
	acct, err := LoadServiceAccount(testServiceAccount(t, fake.srv.URL+"/token"))
	if err != nil {
		t.Fatalf("LoadServiceAccount: %v", err)
	}
	clk := &manualClock{now: time.Unix(1_700_000_000, 0)}
	sender, err := NewFCM(FCMConfig{
		Account: acct,
		BaseURL: fake.srv.URL,
		Now:     clk.Now,
		// Retries must not put a real sleep in the test suite.
		RetryDelay: 0,
	})
	if err != nil {
		t.Fatalf("NewFCM: %v", err)
	}
	return &fcmHarness{sender: sender, fake: fake, clk: clk}
}

func testPayload() relay.PushPayload {
	return relay.PushPayload{
		Alert:      relay.GenericPushAlert,
		Ciphertext: []byte{0xDE, 0xAD, 0xBE, 0xEF},
	}
}

// --- PB-PUSH-2: the request the sender emits --------------------------------

// TestPBPUSH2_SendPostsAHighPriorityDataOnlyMessage pins the FCM v1 request shape.
//
// Three properties are load-bearing rather than cosmetic:
//   - DATA-ONLY. A `notification` block is rendered by the system, on the lock screen,
//     from text the provider composed -- which is exactly the rendering PB-PUSH-4 puts
//     under the app's control and PB-KEY-2 gates on authentication. Data-only is also
//     what hands the message to the app's own handler.
//   - android.priority HIGH. ADR-007 B16 makes high-priority FCM the SOLE background
//     wake path; a normal-priority message is deferred under Doze, which is precisely
//     the state the wake exists to escape.
//   - the device token in `message.token`, not a topic. A topic is a fan-out channel
//     anyone who learns its name can subscribe to.
func TestPBPUSH2_SendPostsAHighPriorityDataOnlyMessage(t *testing.T) {
	h := newFCMHarness(t, newFakeFCM(t))

	if err := h.sender.Push(context.Background(), "device-token-abc", testPayload()); err != nil {
		t.Fatalf("Push: %v", err)
	}
	sends := h.fake.sends()
	if len(sends) != 1 {
		t.Fatalf("send count: got %d, want 1", len(sends))
	}
	req := sends[0]
	if req.method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.method)
	}
	if want := "/v1/projects/swarm-test-project/messages:send"; req.path != want {
		t.Fatalf("path = %q, want %q", req.path, want)
	}
	if !strings.HasPrefix(req.bearer, "Bearer ") {
		t.Fatalf("Authorization = %q, want a Bearer access token", req.bearer)
	}

	msg, ok := req.body["message"].(map[string]any)
	if !ok {
		t.Fatalf("request body has no message object: %#v", req.body)
	}
	if got := msg["token"]; got != "device-token-abc" {
		t.Fatalf("message.token = %v, want the device token", got)
	}
	if _, present := msg["notification"]; present {
		t.Fatalf("message carries a notification block: %#v -- rendering belongs to the app (PB-PUSH-4), and a system-rendered alert is composed by the provider", msg)
	}
	if _, present := msg["topic"]; present {
		t.Fatal("message is addressed to a topic: a wake must target exactly one registered device")
	}
	android, ok := msg["android"].(map[string]any)
	if !ok {
		t.Fatalf("message has no android block: %#v", msg)
	}
	if got := android["priority"]; got != "high" {
		t.Fatalf("android.priority = %v, want \"high\" (the sole background wake path under Doze)", got)
	}
}

// TestPBPUSH3_SenderCarriesTheOpaqueCiphertextAndNothingElse is PB-PUSH-3 enforced at
// the transport. The sender is handed a relay.PushPayload and must not enrich it: the
// only variable content that may leave this process is the opaque envelope the gateway
// sealed.
func TestPBPUSH3_SenderCarriesTheOpaqueCiphertextAndNothingElse(t *testing.T) {
	h := newFCMHarness(t, newFakeFCM(t))
	if err := h.sender.Push(context.Background(), "device-token-abc", testPayload()); err != nil {
		t.Fatalf("Push: %v", err)
	}
	sends := h.fake.sends()
	if len(sends) != 1 {
		t.Fatalf("send count: got %d, want 1", len(sends))
	}
	msg, ok := sends[0].body["message"].(map[string]any)
	if !ok {
		t.Fatalf("request body has no message object: %#v", sends[0].body)
	}
	data, ok := msg["data"].(map[string]any)
	if !ok {
		t.Fatalf("message has no data block: %#v", msg)
	}
	if len(data) != 1 {
		t.Fatalf("message.data carries %d keys (%#v), want exactly one: every additional key is metadata handed to the provider", len(data), data)
	}
	raw, ok := data["e"].(string)
	if !ok {
		t.Fatalf("message.data has no \"e\" (envelope) key: %#v", data)
	}
	got, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("message.data.e is not base64: %v", err)
	}
	if string(got) != string(testPayload().Ciphertext) {
		t.Fatalf("delivered ciphertext = %x, want the sealed envelope %x", got, testPayload().Ciphertext)
	}
	// The generic alert is a constant the APP renders; it must not be shipped as
	// provider-visible text.
	body, _ := json.Marshal(sends[0].body)
	if strings.Contains(string(body), relay.GenericPushAlert) {
		t.Fatalf("the request carries the alert text: %s", body)
	}
}

// --- PB-PUSH-2: OAuth acquisition and refresh -------------------------------

// TestPBPUSH2_AccessTokenIsAcquiredOnceAndReused pins caching. An OAuth exchange per
// push is a second round trip on every wake and a self-inflicted rate limit.
func TestPBPUSH2_AccessTokenIsAcquiredOnceAndReused(t *testing.T) {
	h := newFCMHarness(t, newFakeFCM(t))
	for i := 0; i < 3; i++ {
		if err := h.sender.Push(context.Background(), "tok", testPayload()); err != nil {
			t.Fatalf("Push(%d): %v", i, err)
		}
	}
	if got := len(h.fake.tokenExchanges()); got != 1 {
		t.Fatalf("OAuth exchanges for 3 pushes: got %d, want 1", got)
	}
	sends := h.fake.sends()
	if len(sends) != 3 {
		t.Fatalf("send count: got %d, want 3", len(sends))
	}
	if sends[0].bearer != sends[2].bearer {
		t.Fatalf("bearer changed without a refresh: %q then %q", sends[0].bearer, sends[2].bearer)
	}
}

// TestPBPUSH2_ExpiredAccessTokenIsRefreshed pins the other half. A cached token that is
// never refreshed produces a sender that works for an hour after every restart and then
// fails permanently -- and the failure is invisible in any test that does not move the
// clock.
func TestPBPUSH2_ExpiredAccessTokenIsRefreshed(t *testing.T) {
	fake := newFakeFCM(t)
	fake.accessTTL = 10 * time.Minute
	h := newFCMHarness(t, fake)

	if err := h.sender.Push(context.Background(), "tok", testPayload()); err != nil {
		t.Fatalf("Push(1): %v", err)
	}
	h.clk.advance(11 * time.Minute)
	if err := h.sender.Push(context.Background(), "tok", testPayload()); err != nil {
		t.Fatalf("Push(2): %v", err)
	}
	if got := len(h.fake.tokenExchanges()); got != 2 {
		t.Fatalf("OAuth exchanges after the token expired: got %d, want 2", got)
	}
	sends := h.fake.sends()
	if len(sends) != 2 {
		t.Fatalf("send count: got %d, want 2", len(sends))
	}
	if sends[0].bearer == sends[1].bearer {
		t.Fatalf("the second send reused the EXPIRED bearer %q", sends[0].bearer)
	}
}

// TestPBPUSH2_TokenIsRefreshedBeforeItExpiresNotAfter pins a skew margin. A token that
// is used until the exact instant of expiry is a token that is in flight when it
// expires; every clock difference between this host and Google's becomes an
// intermittent 401 that looks like a delivery bug.
func TestPBPUSH2_TokenIsRefreshedBeforeItExpiresNotAfter(t *testing.T) {
	fake := newFakeFCM(t)
	fake.accessTTL = 10 * time.Minute
	h := newFCMHarness(t, fake)

	if err := h.sender.Push(context.Background(), "tok", testPayload()); err != nil {
		t.Fatalf("Push(1): %v", err)
	}
	h.clk.advance(10*time.Minute - 30*time.Second) // inside the expiry, but only just
	if err := h.sender.Push(context.Background(), "tok", testPayload()); err != nil {
		t.Fatalf("Push(2): %v", err)
	}
	if got := len(h.fake.tokenExchanges()); got != 2 {
		t.Fatalf("OAuth exchanges 30s before expiry: got %d, want 2 (refresh early, not at the boundary)", got)
	}
}

// TestPBPUSH2_OAuthAssertionRequestsMessagingScopeOnly keeps the credential's blast
// radius to what it needs. A service account asserted with a broad scope is a
// credential on a network-facing relay host that can do more than push.
func TestPBPUSH2_OAuthAssertionRequestsMessagingScopeOnly(t *testing.T) {
	h := newFCMHarness(t, newFakeFCM(t))
	if err := h.sender.Push(context.Background(), "tok", testPayload()); err != nil {
		t.Fatalf("Push: %v", err)
	}
	ex := h.fake.tokenExchanges()
	if len(ex) != 1 {
		t.Fatalf("OAuth exchanges: got %d, want 1", len(ex))
	}
	if got := ex[0]["grant_type"]; got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		t.Fatalf("grant_type = %q, want the JWT bearer grant", got)
	}
	assertion := ex[0]["assertion"]
	if assertion == "" {
		t.Fatal("no signed assertion in the OAuth exchange")
	}
	claims := decodeJWTClaims(t, assertion)
	scope, _ := claims["scope"].(string)
	if scope != "https://www.googleapis.com/auth/firebase.messaging" {
		t.Fatalf("assertion scope = %q, want the messaging scope alone", scope)
	}
	if got, _ := claims["iss"].(string); got != "pusher@swarm-test-project.iam.gserviceaccount.com" {
		t.Fatalf("assertion iss = %q, want the service account client_email", got)
	}
}

// --- PB-PUSH-2: retry and pruning -------------------------------------------

// TestPBPUSH2_ServerErrorsAreRetriedAndThenSucceed pins that a 5xx is transient.
func TestPBPUSH2_ServerErrorsAreRetriedAndThenSucceed(t *testing.T) {
	fake := newFakeFCM(t)
	fake.sendStatus = []int{http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusOK}
	h := newFCMHarness(t, fake)

	if err := h.sender.Push(context.Background(), "tok", testPayload()); err != nil {
		t.Fatalf("Push: %v (a 5xx must be retried)", err)
	}
	if got := len(h.fake.sends()); got != 3 {
		t.Fatalf("send attempts: got %d, want 3", got)
	}
}

// TestPBPUSH2_RetriesAreBounded pins that a provider outage ends in an error rather than
// in an unbounded loop holding the relay's push path.
func TestPBPUSH2_RetriesAreBounded(t *testing.T) {
	fake := newFakeFCM(t)
	fake.sendStatus = []int{http.StatusServiceUnavailable}
	h := newFCMHarness(t, fake)

	err := h.sender.Push(context.Background(), "tok", testPayload())
	if err == nil {
		t.Fatal("Push returned nil against a permanently failing provider")
	}
	if errors.Is(err, relay.ErrPushUnregistered) {
		t.Fatal("a 5xx was classified as UNREGISTERED: a transient outage would prune every token the relay holds")
	}
	n := len(h.fake.sends())
	if n < 2 {
		t.Fatalf("send attempts: got %d, want more than one (5xx is retryable)", n)
	}
	if n > DefaultMaxAttempts {
		t.Fatalf("send attempts: got %d, want at most DefaultMaxAttempts (%d)", n, DefaultMaxAttempts)
	}
}

// TestPBPUSH2_ClientErrorsAreNotRetried pins the opposite classification. A malformed
// request retried is quota burned to reproduce the same refusal.
func TestPBPUSH2_ClientErrorsAreNotRetried(t *testing.T) {
	fake := newFakeFCM(t)
	fake.sendStatus = []int{http.StatusBadRequest}
	fake.sendBody[http.StatusBadRequest] = `{"error":{"status":"INVALID_ARGUMENT"}}`
	h := newFCMHarness(t, fake)

	if err := h.sender.Push(context.Background(), "tok", testPayload()); err == nil {
		t.Fatal("Push returned nil for a 400")
	}
	if got := len(h.fake.sends()); got != 1 {
		t.Fatalf("send attempts for a 400: got %d, want 1", got)
	}
}

// TestPBPUSH2_UnregisteredMapsToThePruningSentinel is the classification the relay acts
// on. It must be distinguishable from every other failure by errors.Is, because pruning
// on the wrong one silently disables push for a live handset.
func TestPBPUSH2_UnregisteredMapsToThePruningSentinel(t *testing.T) {
	fake := newFakeFCM(t)
	fake.sendStatus = []int{http.StatusNotFound}
	fake.sendBody[http.StatusNotFound] = `{"error":{"code":404,"status":"NOT_FOUND","details":[` +
		`{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`
	h := newFCMHarness(t, fake)

	err := h.sender.Push(context.Background(), "dead-token", testPayload())
	if !errors.Is(err, relay.ErrPushUnregistered) {
		t.Fatalf("Push on an UNREGISTERED token = %v, want relay.ErrPushUnregistered", err)
	}
	if got := len(h.fake.sends()); got != 1 {
		t.Fatalf("send attempts for UNREGISTERED: got %d, want 1 (never retried)", got)
	}
}

// TestPBPUSH2_OtherNotFoundIsNotAPruningSignal guards the mutation that would make the
// test above pass for the wrong reason: classifying every 404 as UNREGISTERED. A 404
// from a misconfigured project id would then prune every token the relay holds.
func TestPBPUSH2_OtherNotFoundIsNotAPruningSignal(t *testing.T) {
	fake := newFakeFCM(t)
	fake.sendStatus = []int{http.StatusNotFound}
	fake.sendBody[http.StatusNotFound] = `{"error":{"code":404,"status":"NOT_FOUND","message":"project not found"}}`
	h := newFCMHarness(t, fake)

	err := h.sender.Push(context.Background(), "live-token", testPayload())
	if err == nil {
		t.Fatal("Push returned nil for a 404")
	}
	if errors.Is(err, relay.ErrPushUnregistered) {
		t.Fatal("a 404 without an UNREGISTERED errorCode was classified as a dead token: a project misconfiguration would prune every registered handset")
	}
}

// --- PB-PUSH-5: credentials degrade gracefully and loudly -------------------

// TestPBPUSH5_InvalidCredentialsFailLoudlyAtConstruction pins where the failure lands.
// A sender that constructs happily and fails on every send is a relay that looks healthy
// while push is dead; the operator finds out from a user who missed a hand-off.
func TestPBPUSH5_InvalidCredentialsFailLoudlyAtConstruction(t *testing.T) {
	// POSITIVE CONTROL. A LoadServiceAccount that rejects everything satisfies every case
	// below for free; prove a well-formed document is accepted first.
	fake := newFakeFCM(t)
	good, err := LoadServiceAccount(testServiceAccount(t, fake.srv.URL+"/token"))
	if err != nil {
		t.Fatalf("control: a well-formed service account was rejected: %v", err)
	}
	if _, err := NewFCM(FCMConfig{Account: good, BaseURL: fake.srv.URL}); err != nil {
		t.Fatalf("control: NewFCM refused a valid credential: %v", err)
	}

	cases := map[string][]byte{
		"empty":               nil,
		"not json":            []byte("this is not a service account"),
		"missing private key": []byte(`{"type":"service_account","project_id":"p","client_email":"e@x","token_uri":"https://t"}`),
		"unparseable key":     []byte(`{"type":"service_account","project_id":"p","client_email":"e@x","token_uri":"https://t","private_key":"-----BEGIN PRIVATE KEY-----\nnope\n-----END PRIVATE KEY-----\n"}`),
		"missing project":     []byte(`{"type":"service_account","client_email":"e@x","token_uri":"https://t","private_key":"x"}`),
	}
	// DISCRIMINATING CASES. Every fixture above carries a private key that is absent or
	// unparseable, so every one is refused by parseRSAPrivateKey and NONE of them exercises
	// the required-field validation at all: deleting that validation from BOTH layers
	// (LoadServiceAccount's field loop AND NewFCM's completeness check) left
	// internal/remote/push and internal/remote/relay entirely green. A fixture that cannot
	// tell a correct implementation from one with no field checking passes both.
	//
	// These carry a WELL-FORMED key and omit exactly one required field each, which is the
	// shape a real misconfiguration takes -- a hand-edited or templated credential. Without
	// them, such a credential constructs a sender that posts to `/projects//messages:send`
	// and 404s on every wake, which is the silent-until-a-user-misses-a-handoff failure this
	// requirement exists to convert into a loud one.
	for _, field := range []string{"project_id", "client_email", "token_uri"} {
		var doc map[string]string
		if err := json.Unmarshal(testServiceAccount(t, fake.srv.URL+"/token"), &doc); err != nil {
			t.Fatalf("control: the well-formed service account does not unmarshal: %v", err)
		}
		delete(doc, field)
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal %s-less service account: %v", field, err)
		}
		cases["valid key, no "+field] = b
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			acct, err := LoadServiceAccount(doc)
			if err != nil {
				return // refused at load: loud enough
			}
			if _, err := NewFCM(FCMConfig{Account: acct}); err == nil {
				t.Fatal("NewFCM accepted invalid credentials: the sender must refuse to construct rather than fail on every wake")
			}
		})
	}
}

// TestPBPUSH5_ConstructionErrorNamesTheProblem pins that the loud failure is also
// legible: an operator reading it must know it is the push credential.
func TestPBPUSH5_ConstructionErrorNamesTheProblem(t *testing.T) {
	_, err := LoadServiceAccount([]byte("garbage"))
	if err == nil {
		t.Fatal("LoadServiceAccount accepted garbage")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "service account") {
		t.Fatalf("error %q does not name the service account: an operator cannot tell which credential failed", err)
	}
}

// TestPBPUSH5_UnreachableProviderReturnsAnErrorAndNeverPanics pins the runtime
// degradation path. The relay calls this on its push path; a panic there takes the relay
// down and with it the mailbox every session depends on.
func TestPBPUSH5_UnreachableProviderReturnsAnErrorAndNeverPanics(t *testing.T) {
	fake := newFakeFCM(t)
	h := newFCMHarness(t, fake)
	fake.srv.Close() // the provider vanishes mid-life

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Push panicked against an unreachable provider: %v", r)
		}
	}()
	if err := h.sender.Push(context.Background(), "tok", testPayload()); err == nil {
		t.Fatal("Push returned nil against an unreachable provider")
	}
}

// TestPBPUSH5_ContextCancellationIsHonoured pins that a push cannot outlive its caller:
// the relay hands its connection context in, and a sender that ignores it keeps
// retrying against a dead deadline while holding the caller.
func TestPBPUSH5_ContextCancellationIsHonoured(t *testing.T) {
	fake := newFakeFCM(t)
	fake.sendStatus = []int{http.StatusServiceUnavailable}
	h := newFCMHarness(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.sender.Push(ctx, "tok", testPayload()); err == nil {
		t.Fatal("Push returned nil with an already-cancelled context")
	}
}

// TestPBPUSH2_SenderSatisfiesTheRelaySeam is the compile-time link between this package
// and the transport it implements. Without it the sender could drift from the interface
// and nothing would notice until assembly.
//
// NOT A RED TEST: it is a compile-time fence and passes the moment both types exist.
func TestPBPUSH2_SenderSatisfiesTheRelaySeam(t *testing.T) {
	var _ relay.PushSink = (*FCM)(nil)
}

// decodeJWTClaims decodes the claim set of an unverified JWT. The assertion is signed
// with a key generated in this test and verified by nobody: this reads what the sender
// ASKED FOR, which is the property under test.
func decodeJWTClaims(t *testing.T, jwt string) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion is not a three-part JWT: %q", jwt)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT claims: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("unmarshal JWT claims: %v", err)
	}
	return claims
}
