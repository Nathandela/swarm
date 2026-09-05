package phonecore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type registrationProofSigner struct {
	delegate InstallationSigner

	mu       sync.Mutex
	calls    [][]byte
	sigs     [][]byte
	failCall int
}

type registrationIDCaptureTransport struct {
	inner http.RoundTripper

	mu    sync.Mutex
	first string
}

func (t *registrationIDCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusCreated ||
		req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/v1/installations") {
		return resp, err
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if readErr != nil {
		return resp, nil
	}
	var decoded struct {
		InstallationID string `json:"installation_id"`
	}
	if json.Unmarshal(body, &decoded) == nil {
		t.mu.Lock()
		if t.first == "" {
			t.first = decoded.InstallationID
		}
		t.mu.Unlock()
	}
	return resp, nil
}

func (t *registrationIDCaptureTransport) firstID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.first
}

func (s *registrationProofSigner) PublicKey() []byte { return s.delegate.PublicKey() }

func (s *registrationProofSigner) Sign(canonical []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, append([]byte(nil), canonical...))
	if s.failCall == len(s.calls) {
		return nil, errors.New("injected registration signing failure")
	}
	sig, err := s.delegate.Sign(canonical)
	if err == nil {
		s.sigs = append(s.sigs, append([]byte(nil), sig...))
	}
	return sig, err
}

func (s *registrationProofSigner) snapshot() (calls, sigs [][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, call := range s.calls {
		calls = append(calls, append([]byte(nil), call...))
	}
	for _, sig := range s.sigs {
		sigs = append(sigs, append([]byte(nil), sig...))
	}
	return calls, sigs
}

func registrationProofMessageForTest(idem string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte("swarm-pg-register-v1|" + idem + "|" + base64.RawURLEncoding.EncodeToString(sum[:]))
}

func TestRegisterProof_BindsEveryPOSTToThePersistedBodyAndIdempotencyKey(t *testing.T) {
	hs := r3aGateway(t, &r3aSender{}, &r3aAttestVerifier{licensed: true})
	rt := &r3aRecordingTransport{inner: hs.Client().Transport}
	rt.swallow = func(seq int, r *http.Request) bool {
		return seq == 0 && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/installations")
	}
	signer := &registrationProofSigner{delegate: newR3ASigner(t)}
	attestCalls := 0
	client := NewGatewayClient(hs.URL, signer, func(hash [32]byte) (string, error) {
		attestCalls++
		return "attest:" + base64.RawURLEncoding.EncodeToString(hash[:]), nil
	}, &http.Client{Transport: rt})

	if _, err := client.Register(context.Background(), "fcm-token-proof"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var posts []r3aWireRequest
	for _, req := range rt.recorded() {
		if req.method == http.MethodPost && strings.HasSuffix(req.path, "/v1/installations") {
			posts = append(posts, req)
		}
	}
	if len(posts) != 2 {
		t.Fatalf("registration POSTs = %d, want original plus lost-response replay", len(posts))
	}
	calls, sigs := signer.snapshot()
	if len(calls) != len(posts) || len(sigs) != len(posts) {
		t.Fatalf("sign calls/signatures/POSTs = %d/%d/%d, want one fresh proof per POST", len(calls), len(sigs), len(posts))
	}
	for i, post := range posts {
		wantMessage := registrationProofMessageForTest(post.idempotencyKey, post.body)
		if !bytes.Equal(calls[i], wantMessage) {
			t.Errorf("sign call %d = %q, want proof bound to exact final body and Idempotency-Key %q", i, calls[i], wantMessage)
		}
		wantHeader := "p256-sha256 " + base64.RawURLEncoding.EncodeToString(sigs[i])
		if post.registrationProof != wantHeader {
			t.Errorf("POST %d proof = %q, want %q", i, post.registrationProof, wantHeader)
		}
	}
	if attestCalls != 1 {
		t.Errorf("attestation calls = %d, want one for the persisted body", attestCalls)
	}
}

func TestRegisterProof_SigningFailureSendsNoUnsignedRequest(t *testing.T) {
	rt := &r3aRecordingTransport{inner: failingTransport{}}
	signer := &registrationProofSigner{delegate: newR3ASigner(t), failCall: 1}
	attestCalls := 0
	client := NewGatewayClient("http://gateway.invalid", signer, func(hash [32]byte) (string, error) {
		attestCalls++
		return "attest:" + base64.RawURLEncoding.EncodeToString(hash[:]), nil
	}, &http.Client{Transport: rt})

	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	if _, err := core.EnsurePushRegistration(context.Background(), client, staticToken("fcm-token-proof")); err == nil {
		t.Fatal("EnsurePushRegistration succeeded when the installation signer failed")
	}
	if got := len(rt.recorded()); got != 0 {
		t.Fatalf("HTTP requests = %d, want zero (there is no unsigned registration fallback)", got)
	}
	if attestCalls != 1 {
		t.Errorf("attestation calls = %d, want one prepared body", attestCalls)
	}
	restarted := phone.resume(t)
	restarted.mu.Lock()
	pending := restarted.push.data.PendingRegister
	restarted.mu.Unlock()
	if pending != nil {
		t.Fatal("first-attempt signing failure left a durable pending registration even though no POST was sent")
	}
}

func TestRegisterProof_SigningFailureAfterLostResponsePreservesExactDurableReplay(t *testing.T) {
	hs := r3aGateway(t, &r3aSender{}, &r3aAttestVerifier{licensed: true})
	capture := &registrationIDCaptureTransport{inner: hs.Client().Transport}
	rt := &r3aRecordingTransport{inner: capture}
	rt.swallow = func(seq int, r *http.Request) bool {
		return seq == 0 && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/installations")
	}
	baseSigner := newR3ASigner(t)
	firstSigner := &registrationProofSigner{delegate: baseSigner, failCall: 2}
	attestCalls := 0
	attest := func(hash [32]byte) (string, error) {
		attestCalls++
		return "attest:" + base64.RawURLEncoding.EncodeToString(hash[:]), nil
	}
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	client := NewGatewayClient(hs.URL, firstSigner, attest, &http.Client{Transport: rt})

	_, err := core.EnsurePushRegistration(context.Background(), client, staticToken("fcm-token-proof"))
	if !errors.Is(err, errRegisterOutcomeUnknown) {
		t.Fatalf("lost response followed by signing failure = %v, want outcome unknown", err)
	}
	mintedID := capture.firstID()
	if mintedID == "" {
		t.Fatal("first POST did not mint an installation before its response was lost")
	}
	core.mu.Lock()
	if core.push.data.PendingRegister == nil {
		core.mu.Unlock()
		t.Fatal("lost response followed by signing failure discarded the prepared registration")
	}
	pending := *core.push.data.PendingRegister
	pending.Body = append([]byte(nil), pending.Body...)
	core.mu.Unlock()

	// A restarted process must treat a signer failure as still outcome-unknown because
	// the durable pair exists only after an earlier POST may already have committed.
	restarted := phone.resume(t)
	failingSigner := &registrationProofSigner{delegate: baseSigner, failCall: 1}
	before := len(rt.recorded())
	_, err = restarted.EnsurePushRegistration(context.Background(),
		NewGatewayClient(hs.URL, failingSigner, attest, &http.Client{Transport: rt}),
		staticToken("fcm-token-proof"))
	if !errors.Is(err, errRegisterOutcomeUnknown) {
		t.Fatalf("restart signing failure = %v, want prior outcome to remain unknown", err)
	}
	if got := len(rt.recorded()); got != before {
		t.Fatalf("restart signing failure sent %d HTTP requests, want zero", got-before)
	}
	restarted.mu.Lock()
	afterFailure := restarted.push.data.PendingRegister
	if afterFailure == nil || afterFailure.IdemKey != pending.IdemKey || !bytes.Equal(afterFailure.Body, pending.Body) {
		t.Error("restart signing failure discarded or changed the outcome-unknown prepared registration")
	}
	restarted.mu.Unlock()

	// Once signing recovers, the exact durable pair is replayed without another
	// attestation and resolves the installation already minted before the lost response.
	healed := phone.resume(t)
	rt.swallow = nil
	reg, err := healed.EnsurePushRegistration(context.Background(),
		NewGatewayClient(hs.URL, &registrationProofSigner{delegate: baseSigner}, attest, &http.Client{Transport: rt}),
		staticToken("fcm-token-proof"))
	if err != nil {
		t.Fatalf("healed exact replay: %v", err)
	}
	if reg.InstallationID != mintedID || reg.InstallationID != healed.PushInstallationID() {
		t.Fatalf("healed replay installation = %q, first minted %q, durable %q",
			reg.InstallationID, mintedID, healed.PushInstallationID())
	}
	requests := rt.recorded()
	last := requests[len(requests)-1]
	if last.idempotencyKey != pending.IdemKey || !bytes.Equal(last.body, pending.Body) {
		t.Error("healed replay changed the durable Idempotency-Key or exact final body")
	}
	if attestCalls != 1 {
		t.Errorf("attestation calls = %d, want one across lost response, restart failure, and exact replay", attestCalls)
	}
}
