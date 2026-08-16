// FAILING-FIRST (TDD RED, GG-5) for Wave R3's Android/phone slice, part 1 of the scope:
// the INSTALLATION REGISTRATION CLIENT (ADR-015 P5, push-gateway-api.md sections 2 and 3.1-3.3).
//
// WHAT IS UNDER TEST. The phone-side gateway client that does not exist yet:
//
//   - phonecore.NewGatewayClient / (*GatewayClient).Register / RotateToken / AllocateAddress
//     -- the four installation-side operations of push-gateway-api.md, signed per PG-AUTH-1
//     with an injectable InstallationSigner (the production signer is Android Keystore's
//     P-256 key, PG-AUTH-2; these tests sign with an in-process ecdsa key that honours the
//     same wire contract: IEEE P1363 64-byte r||s, low s).
//   - (*Core).EnsurePushRegistration -- the durable orchestration the Kotlin token entry
//     points call: register when this installation has never registered, rotate (PUT, the
//     PG-AUTH-5 refresh) when it has, fall back to a fresh registration when the gateway no
//     longer knows the installation (180-day expiry, or a wiped gateway).
//
// THE GATEWAY IN EVERY TEST IS THE REAL ONE. internal/pushgw's own Server -- the machine
// half this repository already shipped and reviewed -- is spun in process behind httptest,
// with only its two declared seams faked (WakeSender, AttestationVerifier). Keys and
// signatures are therefore verified by the gateway's OWN verifyInstallationSignature, and
// the client's RFC 8785-shaped attestation requestHash is verified by the gateway's OWN
// recomputation (register.go requestHash), not by a mock of the contract.
//
// NOTHING HERE TOUCHES FCM, GOOGLE, OR A HANDSET. PB-E2E-5 and R3's physical exit are not
// claimed by any test in this file.
package phonecore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// ---------------------------------------------------------------------------------------
// Test fixtures: the real gateway behind httptest, a Keystore-shaped signer, and the two
// halves of a fake Play Integrity that share only the requestHash convention.
// ---------------------------------------------------------------------------------------

// r3aSender is the WakeSender seam: it records every (token, envelope) pair the gateway
// forwards, so a test can prove which FCM token a wake was routed to after a rotation.
type r3aSender struct {
	mu    sync.Mutex
	sends []r3aSend
}

type r3aSend struct {
	token    string
	envelope []byte
}

func (s *r3aSender) Send(_ context.Context, fcmToken string, envelope []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, r3aSend{token: fcmToken, envelope: append([]byte(nil), envelope...)})
	return nil
}

func (s *r3aSender) snapshot() []r3aSend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]r3aSend(nil), s.sends...)
}

// r3aAttestVerifier is the GATEWAY half of the fake Play Integrity pair. The token format
// ("attest:" + base64url(requestHash)) is private to this test file; what makes the pair a
// contract test is that the gateway RECOMPUTES the hash from the received body and refuses
// on mismatch, so the client's canonicalization is checked against the server's.
type r3aAttestVerifier struct {
	licensed bool
}

func (v *r3aAttestVerifier) Verify(_ context.Context, verdictToken string) (pushgw.VerdictBinding, error) {
	raw, ok := strings.CutPrefix(verdictToken, "attest:")
	if !ok {
		return pushgw.VerdictBinding{}, errors.New("r3a: unexpected verdict token shape")
	}
	sum, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(sum) != 32 {
		return pushgw.VerdictBinding{}, errors.New("r3a: unparseable verdict token")
	}
	var b pushgw.VerdictBinding
	copy(b.RequestHash[:], sum)
	b.LicensedBuild = v.licensed
	return b, nil
}

// r3aAttestor is the PHONE half: the AttestFunc handed to the client. It requires the
// client to have computed a 32-byte requestHash (PG-AUTH-11) and embeds it in the token
// the gateway-side verifier decodes.
func r3aAttestor(t *testing.T) AttestFunc {
	t.Helper()
	return func(requestHash [32]byte) (string, error) {
		return "attest:" + base64.RawURLEncoding.EncodeToString(requestHash[:]), nil
	}
}

// r3aGateway spins one REAL pushgw.Server over a fresh bbolt file and serves it on a
// loopback httptest server. The caller owns nothing; cleanup is registered here.
func r3aGateway(t *testing.T, sender pushgw.WakeSender, attest pushgw.AttestationVerifier) *httptest.Server {
	t.Helper()
	srv, err := pushgw.NewServer(pushgw.Config{
		DBPath: filepath.Join(t.TempDir(), "pushgw.db"),
		Sender: sender,
		Attest: attest,
	})
	if err != nil {
		t.Fatalf("pushgw.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	return hs
}

// r3aSigner implements the InstallationSigner contract with an in-process P-256 key: the
// SEC1 uncompressed public point, and IEEE P1363 64-byte r||s signatures with s normalized
// low, over SHA-256 of the canonical string -- exactly what PG-AUTH-2 requires of the
// production Keystore signer.
type r3aSigner struct {
	key *ecdsa.PrivateKey
}

func newR3ASigner(t *testing.T) *r3aSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	return &r3aSigner{key: key}
}

func (s *r3aSigner) PublicKey() []byte {
	// crypto/ecdh's Bytes() is the SEC1 uncompressed point -- byte-identical to the
	// deprecated elliptic.Marshal encoding (that function's own deprecation note says
	// so), so the wire contract this fixture pins (PG-AUTH-2 / spec 3.1's 65-byte
	// pattern) is unchanged. GREEN swapped the call only to keep staticcheck green.
	pub, err := s.key.PublicKey.ECDH()
	if err != nil {
		panic(err) // unreachable: the key is minted on P-256 by newR3ASigner
	}
	return pub.Bytes()
}

func (s *r3aSigner) Sign(canonical []byte) ([]byte, error) {
	digest := sha256.Sum256(canonical)
	r, sInt, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return nil, err
	}
	n := s.key.Curve.Params().N
	half := new(big.Int).Rsh(n, 1)
	if sInt.Cmp(half) > 0 {
		sInt = new(big.Int).Sub(n, sInt)
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	sInt.FillBytes(out[32:])
	return out, nil
}

// r3aRecordingTransport wraps a transport and records every request that crosses it, with
// an optional per-request veto that simulates a lost response AFTER the server processed
// the request.
type r3aRecordingTransport struct {
	inner http.RoundTripper

	mu       sync.Mutex
	requests []r3aWireRequest
	swallow  func(seq int, r *http.Request) bool
}

type r3aWireRequest struct {
	method         string
	path           string
	idempotencyKey string
	body           []byte
}

func (rt *r3aRecordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil && r.GetBody != nil {
		b, err := r.GetBody()
		if err == nil {
			buf := make([]byte, 0, 4096)
			tmp := make([]byte, 4096)
			for {
				n, rerr := b.Read(tmp)
				buf = append(buf, tmp[:n]...)
				if rerr != nil {
					break
				}
			}
			body = buf
		}
	}
	rt.mu.Lock()
	seq := len(rt.requests)
	rt.requests = append(rt.requests, r3aWireRequest{
		method:         r.Method,
		path:           r.URL.Path,
		idempotencyKey: r.Header.Get("Idempotency-Key"),
		body:           body,
	})
	swallow := rt.swallow
	rt.mu.Unlock()

	resp, err := rt.inner.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if swallow != nil && swallow(seq, r) {
		resp.Body.Close()
		return nil, errors.New("r3a: response lost on the way back (injected)")
	}
	return resp, nil
}

func (rt *r3aRecordingTransport) recorded() []r3aWireRequest {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]r3aWireRequest(nil), rt.requests...)
}

// r3aSubmitWake acts as the MACHINE (swarm-remote): it seals a real WakeV1 with the
// machine-side producer and submits it to the real gateway under the submit capability,
// returning the HTTP status. This is the cross-side contract: the wake the phone's
// allocation authorizes is the wake the machine's own sealer produces.
func r3aSubmitWake(t *testing.T, gwURL string, addr PushAddress, capability string, seq uint64) int {
	t.Helper()
	env, err := remotegw.SealWakeV1(r3aFixedWakeKey(), remotegw.PushAddress(addr), seq, time.Now())
	if err != nil {
		t.Fatalf("remotegw.SealWakeV1: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, gwURL+"/v1/wakes", strings.NewReader(string(env)))
	if err != nil {
		t.Fatalf("building the wake request: %v", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Swarm-Capability "+capability)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("submitting the wake: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// r3aFixedWakeKey is a deterministic wake key for tests whose subject is ROUTING (which
// token, which address), not wake authentication; the gateway never opens the envelope
// (PG-SUB-5), so the key's value is irrelevant to it.
func r3aFixedWakeKey() (k crypto.WakeKey) {
	for i := range k {
		k[i] = 0x42
	}
	return k
}

// ---------------------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------------------

// TestR3A_Register_MintsAnInstallationAgainstTheRealGateway: the client's Register must
// produce a request the real gateway admits end to end -- P-256 key marshalling, the
// closed field set, and PG-AUTH-11's requestHash carve-out (the body with
// attestation.token replaced by the empty string, canonicalized) all checked by the
// gateway's own handleRegister, which recomputes the hash from the received bytes.
func TestR3A_Register_MintsAnInstallationAgainstTheRealGateway(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})

	client := NewGatewayClient(hs.URL, newR3ASigner(t), r3aAttestor(t), hs.Client())
	reg, err := client.Register(context.Background(), "fcm-token-alpha")
	if err != nil {
		t.Fatalf("Register against the real gateway: %v", err)
	}
	if reg.InstallationID == "" {
		t.Fatal("Register returned an empty installation id")
	}
	if len(reg.InstallationID) != 22 {
		t.Errorf("installation id %q is not the 22-character base64url of 16 opaque bytes", reg.InstallationID)
	}
	if !reg.RefreshBefore.After(time.Now()) {
		t.Errorf("RefreshBefore %v is not in the future; the 180-day floor was not parsed", reg.RefreshBefore)
	}
}

// TestR3A_Register_RetriesWithTheSameIdempotencyKey: PG-REG-2's client half. A response
// lost on a flaky handset network must be retried with the SAME Idempotency-Key and the
// byte-identical body -- anything else mints a second durable installation whose token
// lives until the 180-day expiry.
func TestR3A_Register_RetriesWithTheSameIdempotencyKey(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})

	rt := &r3aRecordingTransport{inner: hs.Client().Transport}
	rt.swallow = func(seq int, r *http.Request) bool {
		// Swallow exactly the first register response, AFTER the gateway handled it.
		return seq == 0 && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/installations")
	}
	hc := &http.Client{Transport: rt}

	client := NewGatewayClient(hs.URL, newR3ASigner(t), r3aAttestor(t), hc)
	reg, err := client.Register(context.Background(), "fcm-token-alpha")
	if err != nil {
		t.Fatalf("Register with one lost response: %v", err)
	}
	if reg.InstallationID == "" {
		t.Fatal("Register returned an empty installation id after the retry")
	}

	posts := 0
	var keys []string
	var bodies [][]byte
	for _, r := range rt.recorded() {
		if r.method == http.MethodPost && strings.HasSuffix(r.path, "/v1/installations") {
			posts++
			keys = append(keys, r.idempotencyKey)
			bodies = append(bodies, r.body)
		}
	}
	if posts != 2 {
		t.Fatalf("expected exactly 2 register attempts on the wire (original + retry), saw %d", posts)
	}
	if keys[0] == "" || keys[0] != keys[1] {
		t.Errorf("the retry did not reuse the Idempotency-Key: %q then %q", keys[0], keys[1])
	}
	if string(bodies[0]) != string(bodies[1]) {
		t.Error("the retry was not byte-identical to the original registration body")
	}
}

// TestR3A_Register_ARefusedAttestationEnrollsNothing: PG-AUTH-13. A device the verifier
// does not recognise as the licensed build is refused registration; the client surfaces
// the typed refusal and MUST NOT have persisted any installation identity for the app to
// act on -- the honest state is "foreground updates only", not a half-enrolled identity.
func TestR3A_Register_ARefusedAttestationEnrollsNothing(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: false})

	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)

	client := NewGatewayClient(hs.URL, newR3ASigner(t), r3aAttestor(t), hs.Client())
	_, err := core.EnsurePushRegistration(context.Background(), client, staticToken("fcm-token-alpha"))
	if !errors.Is(err, ErrAttestationRefused) {
		t.Fatalf("EnsurePushRegistration under a refused attestation: got %v, want ErrAttestationRefused", err)
	}

	// Nothing durable: a process restart must come back unregistered.
	restarted := phone.resume(t)
	if got := restarted.PushInstallationID(); got != "" {
		t.Errorf("a refused attestation still persisted installation id %q", got)
	}
}

// ---------------------------------------------------------------------------------------
// Rotation
// ---------------------------------------------------------------------------------------

// TestR3A_TokenRotation_RotatesWithoutMintingASecondInstallation: scope item 1's core
// property (ADR-015 P5, PG-ROT-1). A provider token rotation is ONE authenticated PUT --
// never a second registration -- and it touches no allocation: the address allocated
// before the rotation still routes, and the wake the machine submits after it is
// forwarded to the NEW token.
func TestR3A_TokenRotation_RotatesWithoutMintingASecondInstallation(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})

	rt := &r3aRecordingTransport{inner: hs.Client().Transport}
	hc := &http.Client{Transport: rt}

	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)

	client := NewGatewayClient(hs.URL, newR3ASigner(t), r3aAttestor(t), hc)

	// Register with token A and allocate one per-machine address.
	reg, err := core.EnsurePushRegistration(context.Background(), client, staticToken("fcm-token-alpha"))
	if err != nil {
		t.Fatalf("initial EnsurePushRegistration: %v", err)
	}
	alloc, err := client.AllocateAddress(context.Background(), reg.InstallationID)
	if err != nil {
		t.Fatalf("AllocateAddress: %v", err)
	}
	if alloc.SubmitCapability == "" || alloc.MachineRevokeCapability == "" {
		t.Fatal("allocation returned an empty capability")
	}
	if alloc.SubmitCapability == alloc.MachineRevokeCapability {
		t.Fatal("submit and machine-revoke capabilities are not distinct (PG-AUTH-9)")
	}

	// FCM rotates the token while the app is backgrounded; onNewToken hands it here.
	if _, err := core.EnsurePushRegistration(context.Background(), client, staticToken("fcm-token-beta")); err != nil {
		t.Fatalf("EnsurePushRegistration after rotation: %v", err)
	}

	// The wire shape: exactly ONE register, and at least one authenticated PUT of the
	// token; rotation must never re-register.
	posts, puts := 0, 0
	for _, r := range rt.recorded() {
		if r.method == http.MethodPost && strings.HasSuffix(r.path, "/v1/installations") {
			posts++
		}
		if r.method == http.MethodPut && strings.HasSuffix(r.path, "/token") {
			puts++
		}
	}
	if posts != 1 {
		t.Errorf("token rotation minted a new installation: %d register calls on the wire", posts)
	}
	if puts == 0 {
		t.Error("no PUT .../token crossed the wire; the rotation never reached the gateway")
	}

	// PG-ROT-1, observed end to end: the machine's wake for the PRE-rotation allocation
	// is accepted and forwarded to the POST-rotation token.
	status := r3aSubmitWake(t, hs.URL, alloc.Address, alloc.SubmitCapability, 1)
	if status != http.StatusOK {
		t.Fatalf("wake submit after rotation: status %d, want 200 (the allocation must survive rotation)", status)
	}
	sends := sender.snapshot()
	if len(sends) != 1 {
		t.Fatalf("gateway forwarded %d wakes, want 1", len(sends))
	}
	if sends[0].token != "fcm-token-beta" {
		t.Errorf("the wake was forwarded to token %q, want the rotated fcm-token-beta", sends[0].token)
	}
	if len(sends[0].envelope) != 74 {
		t.Errorf("forwarded envelope is %d bytes, want the 74-byte WakeV1 (PG-WAKE-2)", len(sends[0].envelope))
	}
}

// TestR3A_TokenRotation_ReRegistersWhenTheGatewayForgotTheInstallation: the fallback half
// of "token rotation triggers re-registration". An installation the gateway no longer
// holds (180-day expiry, or a rebuilt gateway) answers the rotation PUT with 401; the
// client must fall back to a FRESH registration and persist the new identity, because the
// alternative is a phone that retries an unauthorized PUT forever and never receives
// another wake.
func TestR3A_TokenRotation_ReRegistersWhenTheGatewayForgotTheInstallation(t *testing.T) {
	sender := &r3aSender{}

	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	signer := newR3ASigner(t)

	// First life: register against gateway A.
	hsA := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})
	clientA := NewGatewayClient(hsA.URL, signer, r3aAttestor(t), hsA.Client())
	regA, err := core.EnsurePushRegistration(context.Background(), clientA, staticToken("fcm-token-alpha"))
	if err != nil {
		t.Fatalf("EnsurePushRegistration against gateway A: %v", err)
	}

	// Second life: the gateway's store no longer knows the installation. Same durable
	// phone state, fresh gateway B.
	hsB := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})
	rt := &r3aRecordingTransport{inner: hsB.Client().Transport}
	clientB := NewGatewayClient(hsB.URL, signer, r3aAttestor(t), &http.Client{Transport: rt})

	restarted := phone.resume(t)
	regB, err := restarted.EnsurePushRegistration(context.Background(), clientB, staticToken("fcm-token-beta"))
	if err != nil {
		t.Fatalf("EnsurePushRegistration against a gateway that forgot the installation: %v", err)
	}
	if regB.InstallationID == "" || regB.InstallationID == regA.InstallationID {
		t.Errorf("fallback registration did not mint a fresh installation: old %q new %q",
			regA.InstallationID, regB.InstallationID)
	}

	// The wire order tells the story: the client tried the rotation first (PUT), was
	// refused, and only then registered.
	recorded := rt.recorded()
	sawPut, sawPost := false, false
	for _, r := range recorded {
		if r.method == http.MethodPut && strings.HasSuffix(r.path, "/token") {
			if sawPost {
				t.Error("the client registered BEFORE attempting the rotation PUT; rotation must be tried first")
			}
			sawPut = true
		}
		if r.method == http.MethodPost && strings.HasSuffix(r.path, "/v1/installations") {
			sawPost = true
		}
	}
	if !sawPut || !sawPost {
		t.Errorf("expected a refused PUT then a fresh POST on the wire; saw put=%v post=%v", sawPut, sawPost)
	}

	// The fresh identity is durable.
	again := phone.resume(t)
	if got := again.PushInstallationID(); got != regB.InstallationID {
		t.Errorf("re-registration was not persisted: durable id %q, want %q", got, regB.InstallationID)
	}
}

// r3aPhone is a minimal provisioned phone for this file: a state directory and the two
// tier sealers, resumed exactly as an Android process start does.
type r3aPhone struct {
	dir     string
	wake    *s14aSealer
	content *s14aSealer
}

func (p *r3aPhone) resume(t *testing.T) *Core {
	t.Helper()
	core, err := Resume(Config{
		Dir: p.dir, Machine: "machine-endpoint-r3a",
		WakeSealer: p.wake, ContentSealer: p.content,
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	return core
}
