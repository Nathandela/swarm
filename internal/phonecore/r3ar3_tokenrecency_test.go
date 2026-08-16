// FAILING-FIRST (TDD RED, GG-5) for Wave R3 round 3, the registration findings. The
// gateway in every test is the REAL internal/pushgw.Server in process, as in rounds 1-2.
//
//   - BLOCKING: rotation ordering installed a STALE FCM token durably. regMu serialises
//     by lock arrival, not token recency, and a token is a snapshot taken before the
//     lock, so a queued caller carrying an older read PUT it over the newer one -- "a
//     phone silently never receives a wake". No phone-side rule can order two opaque
//     token strings, so the seam changes shape: EnsurePushRegistration takes a
//     TokenSource and reads the CURRENT token INSIDE the lock, at act time -- which is
//     exactly what the Android callers have (FirebaseMessaging.getToken answers with the
//     current token, and onNewToken's argument is only ever a hint that it changed). The
//     last-registered token is persisted beside the installation id so staleness is
//     detectable at the seam at all.
//   - MEDIUM: Register's Idempotency-Key lived and died with one call, and the retry
//     loop had no backoff. Three attempts landed in milliseconds on a dead network; if
//     the gateway had processed the POST and only the response was lost, the NEXT call
//     minted a second installation under a fresh key -- the orphaned installation
//     holding a live FCM token for 180 days that PG-REG-2 exists to prevent, and that
//     its 10-minute retention window would have covered had the key (and byte-identical
//     body -- pushgw's cache is keyed on both) been persisted before the POST.
package phonecore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// staticToken is the degenerate TokenSource for callers whose token cannot rotate within
// the test: the shape every pre-round-3 call site takes.
func staticToken(tok string) TokenSource {
	return func() (string, error) { return tok, nil }
}

// r3ar3TokenBox is a rotatable token source: FirebaseMessaging.getToken as the tests can
// steer it.
type r3ar3TokenBox struct {
	mu  sync.Mutex
	tok string
}

func (b *r3ar3TokenBox) set(tok string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tok = tok
}

func (b *r3ar3TokenBox) source() TokenSource {
	return func() (string, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.tok, nil
	}
}

// TestR3AR3_EnsurePushRegistration_AQueuedCallCannotInstallAStaleToken: the round-3
// probe as a permanent assertion. Caller A reads the current token and is held mid-PUT
// while the provider rotates it and caller B queues behind the lock; whatever the lock
// order, every wire write's token is read INSIDE the lock, so the wire can only END on
// the newest token and the durable record follows it.
func TestR3AR3_EnsurePushRegistration_AQueuedCallCannotInstallAStaleToken(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})

	release := make(chan struct{})
	var gate sync.Once
	rt := &r3aRecordingTransport{inner: hs.Client().Transport}
	// Hold caller A's FIRST rotation PUT until the test has rotated the token and queued
	// caller B behind regMu: the round-3 probe's interleaving, made deterministic.
	hold := &r3ar3HoldTransport{inner: rt, hold: func(r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/token") {
			gate.Do(func() { <-release })
		}
	}}
	hc := &http.Client{Transport: hold}

	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	client := NewGatewayClient(hs.URL, newR3ASigner(t), r3aAttestor(t), hc)

	box := &r3ar3TokenBox{tok: "fcm-token-v1"}
	if _, err := core.EnsurePushRegistration(context.Background(), client, box.source()); err != nil {
		t.Fatalf("initial registration: %v", err)
	}

	// Caller A enters with the token still v1 and is held inside its PUT.
	done := make(chan error, 2)
	go func() {
		_, err := core.EnsurePushRegistration(context.Background(), client, box.source())
		done <- err
	}()
	// Give A time to read v1 and reach the held PUT, then rotate and queue B behind it.
	time.Sleep(200 * time.Millisecond)
	box.set("fcm-token-v2")
	go func() {
		_, err := core.EnsurePushRegistration(context.Background(), client, box.source())
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("EnsurePushRegistration: %v", err)
		}
	}

	// The wire must END on the newest token, whatever it carried on the way.
	var tokenPuts []string
	for _, r := range rt.recorded() {
		if r.method == http.MethodPut && strings.HasSuffix(r.path, "/token") {
			tokenPuts = append(tokenPuts, r3ar3FCMToken(t, r.body))
		}
	}
	if len(tokenPuts) == 0 {
		t.Fatal("no token PUT crossed the wire")
	}
	if last := tokenPuts[len(tokenPuts)-1]; last != "fcm-token-v2" {
		t.Errorf("the wire ended on token %q, want fcm-token-v2 (a stale snapshot was "+
			"installed durably: the phone silently never receives another wake)", last)
	}
	if got := core.PushFCMToken(); got != "fcm-token-v2" {
		t.Errorf("durable last-registered token = %q, want fcm-token-v2 (staleness must be "+
			"detectable at the seam)", got)
	}

	// And the gateway agrees: a wake submitted now is forwarded to the newest token.
	alloc, err := client.AllocateAddress(context.Background(), core.PushInstallationID())
	if err != nil {
		t.Fatalf("AllocateAddress: %v", err)
	}
	if status := r3aSubmitWake(t, hs.URL, alloc.Address, alloc.SubmitCapability, 1); status != http.StatusOK {
		t.Fatalf("wake submit: status %d, want 200", status)
	}
	sends := sender.snapshot()
	if n := len(sends); n == 0 || sends[n-1].token != "fcm-token-v2" {
		t.Errorf("the gateway forwarded the wake to %v, want the newest token fcm-token-v2", sends)
	}
}

// TestR3AR3_Register_AnUnknownOutcomeIsReplayedByTheNextCall: PG-REG-2 ACROSS calls. A
// first call whose every attempt loses the response returns with no identity -- but the
// Idempotency-Key and the byte-identical body are durable, so the NEXT call (a fresh
// process, hours later, inside the retention window) replays them and adopts the
// installation the gateway already minted, instead of minting an orphan under a fresh
// key.
func TestR3AR3_Register_AnUnknownOutcomeIsReplayedByTheNextCall(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})

	rt := &r3aRecordingTransport{inner: hs.Client().Transport}
	rt.swallow = func(seq int, r *http.Request) bool {
		// Every response of the FIRST CALL's register attempts is lost AFTER the gateway
		// handled it: the flaky-handset worst case.
		return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/installations") && seq < 3
	}
	hc := &http.Client{Transport: rt}

	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	client := NewGatewayClient(hs.URL, newR3ASigner(t), r3aAttestor(t), hc)

	if _, err := core.EnsurePushRegistration(context.Background(), client, staticToken("fcm-token-alpha")); err == nil {
		t.Fatal("a registration whose every response was lost reported success")
	}
	if got := core.PushInstallationID(); got != "" {
		t.Fatalf("an unresolved registration persisted installation id %q", got)
	}

	// Process death, then the next call on a healed network.
	restarted := phone.resume(t)
	reg, err := restarted.EnsurePushRegistration(context.Background(), client, staticToken("fcm-token-alpha"))
	if err != nil {
		t.Fatalf("the next call after an unknown outcome: %v", err)
	}
	if reg.InstallationID == "" || reg.InstallationID != restarted.PushInstallationID() {
		t.Fatalf("no durable identity after the replay: returned %q, durable %q",
			reg.InstallationID, restarted.PushInstallationID())
	}

	// THE property: every register POST that crossed the wire -- the lost-response
	// attempts and the next call's replay -- carried ONE Idempotency-Key and ONE byte
	// sequence, so pushgw's (key, body) idempotency cache maps them all onto the SAME
	// minted installation and no orphan holds a live token for 180 days.
	var keys, bodies []string
	for _, r := range rt.recorded() {
		if r.method == http.MethodPost && strings.HasSuffix(r.path, "/v1/installations") {
			keys = append(keys, r.idempotencyKey)
			bodies = append(bodies, string(r.body))
		}
	}
	if len(keys) < 2 {
		t.Fatalf("expected the lost attempts plus the replay on the wire, saw %d POSTs", len(keys))
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] != keys[0] {
			t.Errorf("POST %d used Idempotency-Key %q, want %q (a fresh key per call is a "+
				"second durable installation whenever the first POST was processed)", i, keys[i], keys[0])
		}
		if bodies[i] != bodies[0] {
			t.Errorf("POST %d body differs from the first: pushgw's idempotency cache is keyed "+
				"on (key, body), so a changed body is a cache MISS and a second installation", i)
		}
	}
}

// TestR3AR3_Register_RetriesBackOffOnADeadNetwork: the within-call retry loop must not
// land three attempts in milliseconds -- a dead radio needs time, and a hammering client
// exhausts its bounded attempts before the network can come back. The floor asserted is
// deliberately below the configured backoff sum so a loaded CI machine cannot flake it.
func TestR3AR3_Register_RetriesBackOffOnADeadNetwork(t *testing.T) {
	rt := &r3aRecordingTransport{inner: failingTransport{}}
	hc := &http.Client{Transport: rt}
	client := NewGatewayClient("http://gateway.invalid", newR3ASigner(t), r3aAttestor(t), hc)

	start := time.Now()
	_, err := client.Register(context.Background(), "fcm-token-alpha")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Register succeeded against a dead transport")
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("three attempts completed in %v: the retry loop has no backoff, so a dead "+
			"network consumes every bounded attempt in milliseconds", elapsed)
	}

	// And the backoff honours cancellation: a caller that gives up is not held hostage.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start = time.Now()
	if _, err := client.Register(ctx, "fcm-token-alpha"); err == nil {
		t.Fatal("Register succeeded against a dead transport")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("a cancelled Register still waited %v; the backoff must select on ctx.Done", waited)
	}
}

// failingTransport refuses every request without touching a network.
type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("r3ar3: dead network")
}

// r3ar3HoldTransport blocks matching requests until the test releases them, WITHOUT
// touching the round-1 recording transport's shape (that file's line numbers are
// evidence).
type r3ar3HoldTransport struct {
	inner http.RoundTripper
	hold  func(*http.Request)
}

func (h *r3ar3HoldTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	h.hold(r)
	return h.inner.RoundTrip(r)
}

// r3ar3FCMToken reads the fcm_token field out of one recorded wire body.
func r3ar3FCMToken(t *testing.T, body []byte) string {
	t.Helper()
	var b struct {
		FCMToken string `json:"fcm_token"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("recorded body is not JSON: %v", err)
	}
	return b.FCMToken
}
