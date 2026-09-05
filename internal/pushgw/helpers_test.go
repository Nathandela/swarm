package pushgw_test

// Shared conformance-suite harness for the push gateway (docs/specifications/push-gateway-api.md,
// "the spec"). RED phase (bead agents-tracker-hggx.4.1): package pushgw does not exist yet beyond
// doc.go, so every reference to pushgw.* below is an undefined symbol — that is the frozen
// contract the implementation must supply. See the file header of each sibling *_test.go for the
// spec requirements it drives, and this file's own comments for the design decisions embedded in
// the harness itself (they are as load-bearing as the assertions).

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
	"github.com/Nathandela/swarm/internal/pushreg"
)

// ---------------------------------------------------------------------------------------
// Fake clock. Retention (§8) and auth-expiry (PG-AUTH-3) tests must not sleep for real
// minutes/days, so pushgw.Config.Now is an injected func() time.Time, mirroring the
// manualClock pattern already used in internal/remote/push/fcm_test.go.
// ---------------------------------------------------------------------------------------

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ---------------------------------------------------------------------------------------
// Fake WakeSender. The FCM leg is behind pushgw.WakeSender so the conformance suite never
// dials Google (PB-E2E-5 honesty — see fcmwiring_test.go for the one test that wires the
// REAL internal/remote/push sender, against a loopback fake FCM, never against Google).
// ---------------------------------------------------------------------------------------

type sentWake struct {
	token    string
	envelope []byte
}

type fakeSender struct {
	mu       sync.Mutex
	sent     []sentWake
	behavior func(token string, envelope []byte) error // nil => accept every send
}

func newFakeSender() *fakeSender { return &fakeSender{} }

func (f *fakeSender) Send(_ context.Context, token string, envelope []byte) error {
	f.mu.Lock()
	f.sent = append(f.sent, sentWake{token: token, envelope: append([]byte(nil), envelope...)})
	behavior := f.behavior
	f.mu.Unlock()
	if behavior != nil {
		return behavior(token, envelope)
	}
	return nil
}

func (f *fakeSender) calls() []sentWake {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentWake(nil), f.sent...)
}

func (f *fakeSender) setBehavior(fn func(token string, envelope []byte) error) {
	f.mu.Lock()
	f.behavior = fn
	f.mu.Unlock()
}

var _ pushgw.WakeSender = (*fakeSender)(nil)

// ---------------------------------------------------------------------------------------
// Fake AttestationVerifier (PG-AUTH-11..13). Google's Play Integrity endpoint is never
// reached in this suite; the fake stands in for "the gateway SHALL verify the token with
// Google" and lets each test choose what that verification would have concluded.
// ---------------------------------------------------------------------------------------

type fakeAttestor struct {
	mu sync.Mutex
	fn func(ctx context.Context, verdictToken string) (pushgw.VerdictBinding, error)
}

func newFakeAttestor() *fakeAttestor {
	return &fakeAttestor{fn: func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{}, fmt.Errorf("fakeAttestor: no behavior configured for this test")
	}}
}

func (f *fakeAttestor) Verify(ctx context.Context, verdictToken string) (pushgw.VerdictBinding, error) {
	f.mu.Lock()
	fn := f.fn
	f.mu.Unlock()
	return fn(ctx, verdictToken)
}

func (f *fakeAttestor) setFunc(fn func(ctx context.Context, verdictToken string) (pushgw.VerdictBinding, error)) {
	f.mu.Lock()
	f.fn = fn
	f.mu.Unlock()
}

var _ pushgw.AttestationVerifier = (*fakeAttestor)(nil)

// ---------------------------------------------------------------------------------------
// Server harness.
// ---------------------------------------------------------------------------------------

type harness struct {
	t      *testing.T
	srv    *pushgw.Server
	ts     *httptest.Server
	url    string
	client *http.Client
	clock  *fakeClock
	sender *fakeSender
	attest *fakeAttestor
	dbPath string // legacy storage tests; removed with the bbolt suite
}

// defaultTestQuotas sets every bucket generously high so quota tests are the only ones
// that need to narrow it. See quotas_test.go for the deliberately tight variants.
func defaultTestQuotas() pushgw.QuotaConfig {
	return pushgw.QuotaConfig{
		WakesPerAddress:            pushgw.RateLimit{Max: 1000, Window: time.Minute},
		WakesPerSourceIP:           pushgw.RateLimit{Max: 1000, Window: time.Minute},
		RegistrationsPerSourceIP:   pushgw.RateLimit{Max: 1000, Window: time.Minute},
		RegistrationsGlobal:        pushgw.RateLimit{Max: 1000, Window: time.Minute},
		AllocationsPerSourceIP:     pushgw.RateLimit{Max: 1000, Window: time.Minute},
		AllocationsGlobal:          pushgw.RateLimit{Max: 1000, Window: time.Minute},
		AllocationsPerInstallation: 20,
	}
}

// newHarness builds a fresh gateway over a temp-file bbolt database (per PG-RET-10's
// closed field set — one file, per the task's storage decision) fronted by httptest, so
// every test in this suite drives the REAL handler over REAL HTTP, never a mocked router.
func newHarness(t *testing.T, mutate func(*pushgw.Config)) *harness {
	t.Helper()
	clk := newFakeClock()
	snd := newFakeSender()
	att := newFakeAttestor()
	cfg := pushgw.Config{
		Repository:            pushgw.NewMemoryRepository(),
		TokenKeys:             map[string][]byte{"test-v1": bytes.Repeat([]byte{1}, 32)},
		ActiveTokenKeyVersion: "test-v1",
		RegistrationDigestKey: bytes.Repeat([]byte{2}, 32),
		RegistrationAdmission: func(string) bool { return true },
		Sender:                snd,
		Attest:                att,
		Now:                   clk.Now,
		Quotas:                defaultTestQuotas(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := pushgw.NewFirestoreServer(cfg)
	if err != nil {
		t.Fatalf("pushgw.NewServer: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = srv.Close() })
	return &harness{t: t, srv: srv, ts: ts, url: ts.URL, client: ts.Client(), clock: clk, sender: snd, attest: att}
}

// ---------------------------------------------------------------------------------------
// HTTP plumbing.
// ---------------------------------------------------------------------------------------

func (h *harness) do(method, path string, body []byte, headers map[string]string) *http.Response {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, h.url+path, rdr)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	h.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (h *harness) doJSON(method, path string, body []byte, extra map[string]string) *http.Response {
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range extra {
		headers[k] = v
	}
	return h.do(method, path, body, headers)
}

// errorBody mirrors the wire shape of components.schemas.Error (spec §3.6). It is a
// LOCAL test type deliberately: the JSON wire contract is what §4 makes normative, not any
// particular Go struct name pushgw chooses to produce it from.
type errorBody struct {
	Code              string  `json:"code"`
	Message           string  `json:"message"`
	Retryable         bool    `json:"retryable"`
	RetryAfterSeconds *int    `json:"retry_after_seconds"`
	ServerTime        *string `json:"server_time"`
}

func decodeError(t *testing.T, resp *http.Response) errorBody {
	t.Helper()
	var e errorBody
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read error body: %v", err)
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("decode error body %q: %v", raw, err)
	}
	return e
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode body %q: %v", raw, err)
	}
}

// requireStatus fails the test with the response body attached, so a wrong status is
// legible without a second run under -v.
func requireStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d; body=%s", resp.StatusCode, want, raw)
	}
}

// ---------------------------------------------------------------------------------------
// Installation-key signing (PG-AUTH-1, PG-AUTH-2).
// ---------------------------------------------------------------------------------------

// genInstallationKey mints an ECDSA P-256 key and its SEC1 uncompressed-point encoding
// (0x04||X||Y, 65 bytes, base64url unpadded => 87 chars — PG-AUTH-2, §3.1's pattern).
func genInstallationKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	ecdhPub, err := priv.PublicKey.ECDH()
	if err != nil {
		t.Fatalf("PublicKey.ECDH: %v", err)
	}
	return priv, base64RawURL(ecdhPub.Bytes())
}

type signParams struct {
	method string
	path   string
	body   []byte
	nonce  string // "" => fresh 16 CSPRNG bytes
	expiry int64  // 0 => now + 30s
}

// sign builds the three PG-AUTH-1 headers over the canonical string
// swarm-pg-v1|METHOD|path|body_sha256|nonce|expiry, and returns the nonce/expiry used so
// a replay test can resend the identical pair on purpose.
func sign(t *testing.T, priv *ecdsa.PrivateKey, now time.Time, p signParams) (headers map[string]string, nonce string, expiry int64) {
	t.Helper()
	nonce = p.nonce
	if nonce == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			t.Fatalf("rand: %v", err)
		}
		nonce = base64RawURL(b)
	}
	expiry = p.expiry
	if expiry == 0 {
		expiry = now.Add(30 * time.Second).Unix()
	}
	bodyHash := sha256.Sum256(p.body)
	canonical := strings.Join([]string{
		"swarm-pg-v1",
		strings.ToUpper(p.method),
		p.path,
		base64RawURL(bodyHash[:]),
		nonce,
		strconv.FormatInt(expiry, 10),
	}, "|")
	sig := signP256(t, priv, []byte(canonical))
	return map[string]string{
		"Swarm-Nonce":     nonce,
		"Swarm-Expiry":    strconv.FormatInt(expiry, 10),
		"Swarm-Signature": "p256-sha256 " + base64RawURL(sig),
	}, nonce, expiry
}

func registrationHeaders(t *testing.T, priv *ecdsa.PrivateKey, idem string, body []byte) map[string]string {
	t.Helper()
	sig := signP256(t, priv, pushreg.RegistrationProofMessage(idem, body))
	return map[string]string{
		"Idempotency-Key":          idem,
		"Swarm-Registration-Proof": "p256-sha256 " + base64RawURL(sig),
	}
}

func signP256(t *testing.T, priv *ecdsa.PrivateKey, message []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(message)
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatalf("ecdsa.Sign: %v", err)
	}
	// IEEE P1363 fixed-width 64-byte r||s with s normalized low (PG-AUTH-2).
	n := priv.Curve.Params().N
	half := new(big.Int).Rsh(n, 1)
	if s.Cmp(half) > 0 {
		s = new(big.Int).Sub(n, s)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return sig
}

func base64RawURL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// fixedIdemKey pads/truncates tag to exactly 22 chars so it matches §3.6's
// Idempotency-Key pattern (^[A-Za-z0-9_-]{22}$) without hand-counting a literal.
func fixedIdemKey(tag string) string {
	return (tag + strings.Repeat("0", 22))[:22]
}

// ---------------------------------------------------------------------------------------
// Attestation / JCS request hash (PG-AUTH-11).
// ---------------------------------------------------------------------------------------

// jcsRequestHash reproduces PG-AUTH-11's formula: SHA-256 of the RFC 8785 (JCS)
// canonicalization of the registration body with attestation.token replaced by "".
//
// This is a BEST-EFFORT canonicalizer scoped to this suite's flat, ASCII-keyed fixture
// shapes, not a general RFC 8785 implementation: it relies on encoding/json's default
// object-key ordering (bytewise ASCII, which agrees with JCS's UTF-16-code-unit order for
// the ASCII field names §3.1 declares) and disables HTML-escaping so no fixture value is
// silently rewritten. The production verifier (PG-AUTH-11) MUST implement true RFC 8785;
// this helper exists only so the test and that future implementation can be pinned to the
// same expected digest for a given body.
func jcsRequestHash(t *testing.T, body []byte) [32]byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal registration body: %v", err)
	}
	if att, ok := m["attestation"].(map[string]any); ok {
		att["token"] = ""
		m["attestation"] = att
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		t.Fatalf("canonicalize registration body: %v", err)
	}
	return sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
}

// ---------------------------------------------------------------------------------------
// WakeV1 fixture builder (spec §5.1). The gateway holds no wake key and never attempts
// the AEAD (PG-SUB-1, PG-WAKE-3 checks shape/type before any AEAD is touched), so a
// fixture only needs a correctly shaped header — the trailing nonce||tag 40 bytes can be
// arbitrary and the gateway's forwarding behavior must be identical either way. A test
// that specifically needs a cryptographically real seal (fcmwiring_test.go's byte-
// identical-to-FCM check) still doesn't need a valid TAG, only valid SHAPE, because
// nothing on the gateway side ever verifies it.
// ---------------------------------------------------------------------------------------

func decodeAddr(t *testing.T, pushAddress string) [16]byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(pushAddress)
	if err != nil {
		t.Fatalf("decode push_address %q: %v", pushAddress, err)
	}
	if len(raw) != 16 {
		t.Fatalf("push_address decodes to %d bytes, want 16", len(raw))
	}
	var out [16]byte
	copy(out[:], raw)
	return out
}

// buildWakeV1 constructs a spec-shaped 74-byte envelope: version 0x01, type 0x03,
// push_address, wake_seq (uint64 BE), issued_at (int64 BE ms), then 24+16 arbitrary bytes
// standing in for the nonce and AEAD tag (see the file-level comment above).
func buildWakeV1(t *testing.T, addr [16]byte, seq uint64, issuedAt time.Time) [74]byte {
	t.Helper()
	var b [74]byte
	b[0] = 0x01
	b[1] = 0x03
	copy(b[2:18], addr[:])
	binary.BigEndian.PutUint64(b[18:26], seq)
	binary.BigEndian.PutUint64(b[26:34], uint64(issuedAt.UnixMilli()))
	if _, err := rand.Read(b[34:74]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------------------
// Registration / allocation flow helpers shared by every operation-specific test file.
// ---------------------------------------------------------------------------------------

type registered struct {
	installationID string
	refreshBefore  string
	priv           *ecdsa.PrivateKey
	pubKey         string
	fcmToken       string
}

// registerInstallation drives POST /v1/installations end to end, including wiring the
// fake attestor to the request's own JCS hash so the happy path is genuinely exercised
// rather than short-circuited.
func registerInstallation(t *testing.T, h *harness, fcmToken string) registered {
	t.Helper()
	priv, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               fcmToken,
		"attestation": map[string]any{
			"kind":  "play_integrity",
			"token": "verdict-token-" + pub[:8],
		},
	})
	hash := jcsRequestHash(t, body)
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{RequestHash: hash, LicensedBuild: true}, nil
	})
	idem := make([]byte, 16)
	_, _ = rand.Read(idem)
	idemKey := base64RawURL(idem)
	resp := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idemKey, body))
	requireStatus(t, resp, http.StatusCreated)
	var out struct {
		InstallationID string `json:"installation_id"`
		RefreshBefore  string `json:"refresh_before"`
	}
	decodeJSON(t, resp, &out)
	if out.InstallationID == "" {
		t.Fatalf("register: empty installation_id")
	}
	return registered{installationID: out.InstallationID, refreshBefore: out.RefreshBefore, priv: priv, pubKey: pub, fcmToken: fcmToken}
}

type allocated struct {
	pushAddress             string
	submitCapability        string
	machineRevokeCapability string
	unboundExpiresAt        string
}

// allocateAddress drives POST /v1/installations/{id}/addresses with a fresh nonce/expiry
// signed by the installation's own key.
func allocateAddress(t *testing.T, h *harness, r registered) allocated {
	t.Helper()
	path := "/v1/installations/" + r.installationID + "/addresses"
	body := []byte("{}")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "POST", path: path, body: body})
	resp := h.doJSON("POST", path, body, headers)
	requireStatus(t, resp, http.StatusCreated)
	var out allocated
	var raw struct {
		PushAddress             string `json:"push_address"`
		SubmitCapability        string `json:"submit_capability"`
		MachineRevokeCapability string `json:"machine_revoke_capability"`
		UnboundExpiresAt        string `json:"unbound_expires_at"`
	}
	decodeJSON(t, resp, &raw)
	out.pushAddress, out.submitCapability, out.machineRevokeCapability, out.unboundExpiresAt =
		raw.PushAddress, raw.SubmitCapability, raw.MachineRevokeCapability, raw.UnboundExpiresAt
	if out.pushAddress == "" || out.submitCapability == "" || out.machineRevokeCapability == "" {
		t.Fatalf("allocate: incomplete triple: %+v", out)
	}
	if out.submitCapability == out.machineRevokeCapability {
		t.Fatalf("allocate: submit and machine-revoke capabilities must be distinct (PG-AUTH-9)")
	}
	return out
}

// submitTestWake posts a freshly built WakeV1 envelope under the given submit capability
// and returns the raw response for the caller to inspect.
func submitTestWake(h *harness, submitCapability string, envelope [74]byte) *http.Response {
	headers := map[string]string{
		"Content-Type":  "application/octet-stream",
		"Authorization": "Swarm-Capability " + submitCapability,
	}
	return h.do("POST", "/v1/wakes", envelope[:], headers)
}

// bindAddress lands the mandatory first test wake PG-ALLOC-2 requires before an
// allocation counts as bound. Every helper that needs a BOUND (not merely allocated)
// address should call this rather than reaching into wake_test.go's assertions.
func bindAddress(t *testing.T, h *harness, a allocated) {
	t.Helper()
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, resp, http.StatusOK)
}
