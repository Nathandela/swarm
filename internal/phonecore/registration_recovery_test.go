package phonecore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
)

// registrationRecoveryAttestor deterministically expires the first verdict without
// sleeping. The real gateway still recomputes and verifies each request hash.
type registrationRecoveryAttestor struct {
	mu      sync.Mutex
	issued  int
	accepts int
}

func (a *registrationRecoveryAttestor) attest(hash [32]byte) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.issued++
	return "recovery:" + strconv.Itoa(a.issued) + ":" + base64.RawURLEncoding.EncodeToString(hash[:]), nil
}

func (a *registrationRecoveryAttestor) Verify(_ context.Context, token string) (pushgw.VerdictBinding, error) {
	a.mu.Lock()
	accepts := a.accepts
	a.mu.Unlock()
	parts := strings.Split(token, ":")
	if len(parts) != 3 || parts[0] != "recovery" {
		return pushgw.VerdictBinding{}, errors.New("bad recovery verdict")
	}
	issued, err := strconv.Atoi(parts[1])
	if err != nil || issued != accepts {
		return pushgw.VerdictBinding{}, errors.New("expired recovery verdict")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(raw) != 32 {
		return pushgw.VerdictBinding{}, errors.New("bad recovery verdict hash")
	}
	var binding pushgw.VerdictBinding
	copy(binding.RequestHash[:], raw)
	binding.LicensedBuild = true
	return binding, nil
}

func (a *registrationRecoveryAttestor) allow(issued int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.accepts = issued
}

func (a *registrationRecoveryAttestor) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.issued
}

// registrationRecoveryOutage crosses no server boundary, so its unknown outcome
// proves only a prepared request, never a possible installation commit.
type registrationRecoveryOutage struct {
	mu    sync.Mutex
	calls int
}

func (t *registrationRecoveryOutage) RoundTrip(*http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return nil, errors.New("injected pre-commit outage")
}

func (t *registrationRecoveryOutage) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

type registrationRecoveryPost struct {
	idempotencyKey string
	body           []byte
	installationID string
}

// registrationRecoveryCapture observes only a real gateway's successful commits.
type registrationRecoveryCapture struct {
	inner http.RoundTripper
	mu    sync.Mutex
	posts []registrationRecoveryPost
}

func (t *registrationRecoveryCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/v1/installations") {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	resp, err := t.inner.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusCreated || len(body) == 0 {
		return resp, err
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	var response struct {
		InstallationID string `json:"installation_id"`
	}
	if json.Unmarshal(raw, &response) != nil || response.InstallationID == "" {
		return nil, errors.New("test fixture: successful register omitted installation id")
	}
	t.mu.Lock()
	t.posts = append(t.posts, registrationRecoveryPost{
		idempotencyKey: req.Header.Get("Idempotency-Key"), body: append([]byte(nil), body...), installationID: response.InstallationID,
	})
	t.mu.Unlock()
	return resp, nil
}

func (t *registrationRecoveryCapture) committed() []registrationRecoveryPost {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]registrationRecoveryPost(nil), t.posts...)
}

func recoveryPrepared(t *testing.T, signer InstallationSigner, fcmToken string) preparedRegister {
	t.Helper()
	body, err := json.Marshal(registerBody{
		InstallationPublicKey: base64.RawURLEncoding.EncodeToString(signer.PublicKey()),
		FCMToken:              fcmToken,
		Attestation:           registerAttestation{Kind: "play_integrity", Token: "expired"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return preparedRegister{IdemKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16)), Body: body, FCMToken: fcmToken}
}

func TestRegistrationRecovery_RefreshKeepsPreparedIdentity(t *testing.T) {
	signer := newR3ASigner(t)
	before := recoveryPrepared(t, signer, "fcm-token-recovery")
	client := NewGatewayClient("http://gateway.invalid", signer, func(hash [32]byte) (string, error) {
		return "fresh:" + base64.RawURLEncoding.EncodeToString(hash[:]), nil
	}, &http.Client{Timeout: time.Second})
	after, err := client.refreshPreparedRegister(before)
	if err != nil {
		t.Fatalf("refresh valid pending registration: %v", err)
	}
	var oldBody, freshBody registerBody
	if err := json.Unmarshal(before.Body, &oldBody); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after.Body, &freshBody); err != nil {
		t.Fatal(err)
	}
	if after.IdemKey != before.IdemKey || after.FCMToken != before.FCMToken || oldBody.InstallationPublicKey != freshBody.InstallationPublicKey || oldBody.FCMToken != freshBody.FCMToken || oldBody.Attestation.Kind != freshBody.Attestation.Kind || oldBody.Attestation.Token == freshBody.Attestation.Token {
		t.Fatal("refresh did not preserve the registration identity while replacing its attestation")
	}
}

func TestRegistrationRecovery_RefreshRejectsChangedPendingBeforeAttestation(t *testing.T) {
	signer := newR3ASigner(t)
	good := recoveryPrepared(t, signer, "fcm-token-recovery")
	other := newR3ASigner(t)
	badSigner := recoveryPrepared(t, other, good.FCMToken)
	unknown := append([]byte(nil), good.Body...)
	unknown[len(unknown)-1] = '}'
	unknown = append(unknown[:len(unknown)-1], []byte(`,"unexpected":true}`)...)

	for _, tt := range []struct {
		name string
		prep preparedRegister
	}{
		{"malformed JSON", preparedRegister{IdemKey: good.IdemKey, Body: []byte(`{`), FCMToken: good.FCMToken}},
		{"unknown field", preparedRegister{IdemKey: good.IdemKey, Body: unknown, FCMToken: good.FCMToken}},
		{"trailing JSON", preparedRegister{IdemKey: good.IdemKey, Body: append(append([]byte(nil), good.Body...), []byte(` {}`)...), FCMToken: good.FCMToken}},
		{"signer mismatch", badSigner},
		{"FCM mismatch", preparedRegister{IdemKey: good.IdemKey, Body: good.Body, FCMToken: "other-token"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			attestCalls := 0
			client := NewGatewayClient("http://gateway.invalid", signer, func([32]byte) (string, error) {
				attestCalls++
				return "must-not-attest", nil
			}, &http.Client{Timeout: time.Second})
			if _, err := client.refreshPreparedRegister(tt.prep); err == nil {
				t.Fatal("invalid pending registration was refreshed")
			}
			if attestCalls != 0 {
				t.Fatalf("attestation calls=%d, want zero before invalid pending state is refused", attestCalls)
			}
		})
	}
}

func TestRegistrationRecovery_OneFreshAttemptPreservesPendingOnFailure(t *testing.T) {
	for _, tt := range []struct {
		name       string
		failAttest bool
	}{
		{"attestation failure", true},
		{"fresh attestation refused", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			gate := &registrationRecoveryAttestor{} // accepts no old or fresh verdict in this test.
			signer := newR3ASigner(t)
			attestCalls := 0
			attest := func(hash [32]byte) (string, error) {
				attestCalls++
				if tt.failAttest && attestCalls == 2 {
					return "", errors.New("injected fresh attestation failure")
				}
				return "recovery:" + strconv.Itoa(attestCalls) + ":" + base64.RawURLEncoding.EncodeToString(hash[:]), nil
			}
			phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
			outage := &registrationRecoveryOutage{}
			core := phone.resume(t)
			_, err := core.EnsurePushRegistration(ctx,
				NewGatewayClient("http://gateway.invalid", signer, attest, &http.Client{Transport: outage, Timeout: time.Second}),
				staticToken("fcm-token-recovery"))
			if !errors.Is(err, errRegisterOutcomeUnknown) {
				t.Fatalf("seed unknown=%v, want outcome unknown", err)
			}
			core.mu.Lock()
			before := *core.push.data.PendingRegister
			before.Body = append([]byte(nil), before.Body...)
			core.mu.Unlock()

			hs := r3aGateway(t, &r3aSender{}, gate)
			restarted := phone.resume(t)
			_, err = restarted.EnsurePushRegistration(ctx,
				NewGatewayClient(hs.URL, signer, attest, &http.Client{Timeout: time.Second}),
				staticToken("fcm-token-recovery"))
			if !errors.Is(err, errRegisterOutcomeUnknown) {
				t.Fatalf("failed fresh recovery=%v, want outcome unknown", err)
			}
			if attestCalls != 2 {
				t.Fatalf("attestation calls=%d, want initial plus exactly one fresh attempt", attestCalls)
			}
			restarted.mu.Lock()
			after := restarted.push.data.PendingRegister
			restarted.mu.Unlock()
			if after == nil || after.IdemKey != before.IdemKey {
				t.Fatal("failed refresh discarded or replaced the durable registration authority")
			}
			if tt.failAttest && !bytes.Equal(after.Body, before.Body) {
				t.Fatal("attestation failure changed the last replayable pending body")
			}
			if !tt.failAttest && bytes.Equal(after.Body, before.Body) {
				t.Fatal("fresh refusal did not preserve the refreshed attestation body")
			}
			resumed := phone.resume(t)
			resumed.mu.Lock()
			durable := resumed.push.data.PendingRegister
			resumed.mu.Unlock()
			if durable == nil || durable.IdemKey != after.IdemKey || !bytes.Equal(durable.Body, after.Body) {
				t.Fatal("restart lost the last prepared body after refusal")
			}
		})
	}
}

func TestRegistrationRecovery_RefreshPersistFailureNeverSendsUncommittedBody(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	signer := newR3ASigner(t)
	attestCalls := 0
	attest := func(hash [32]byte) (string, error) {
		attestCalls++
		return "fresh-" + strconv.Itoa(attestCalls) + ":" + base64.RawURLEncoding.EncodeToString(hash[:]), nil
	}
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	if _, err := core.EnsurePushRegistration(ctx,
		NewGatewayClient("http://gateway.invalid", signer, attest, &http.Client{Transport: &registrationRecoveryOutage{}, Timeout: time.Second}),
		staticToken("fcm-token-recovery")); !errors.Is(err, errRegisterOutcomeUnknown) {
		t.Fatalf("seed unknown=%v, want outcome unknown", err)
	}
	core.mu.Lock()
	before := *core.push.data.PendingRegister
	before.Body = append([]byte(nil), before.Body...)
	path := core.push.path
	core.mu.Unlock()
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	core.push.path = filepath.Join(blocked, "push-state.sealed")
	t.Cleanup(func() { core.push.path = path })

	refusal := &registrationStaticResponseTransport{status: http.StatusForbidden, body: `{"code":"attestation_invalid","retryable":false}`}
	for attempt := 0; attempt < 2; attempt++ {
		_, err := core.EnsurePushRegistration(ctx,
			NewGatewayClient("http://gateway.invalid", signer, attest, &http.Client{Transport: refusal, Timeout: time.Second}),
			staticToken("fcm-token-recovery"))
		if !errors.Is(err, errRegisterOutcomeUnknown) {
			t.Fatalf("attempt %d=%v, want outcome unknown", attempt+1, err)
		}
	}
	requests := refusal.recorded()
	if len(requests) != 2 || !bytes.Equal(requests[0].body, before.Body) || !bytes.Equal(requests[1].body, before.Body) {
		t.Fatalf("uncommitted refreshed body reached the gateway: %+v", requests)
	}
	if attestCalls != 3 {
		t.Fatalf("attestation calls=%d, want initial plus one refresh per Ensure", attestCalls)
	}
	core.mu.Lock()
	after := core.push.data.PendingRegister
	core.mu.Unlock()
	if after == nil || after.IdemKey != before.IdemKey || !bytes.Equal(after.Body, before.Body) {
		t.Fatal("failed refresh changed in-memory pending registration")
	}
	resumed := phone.resume(t)
	resumed.mu.Lock()
	durable := resumed.push.data.PendingRegister
	resumed.mu.Unlock()
	if durable == nil || durable.IdemKey != before.IdemKey || !bytes.Equal(durable.Body, before.Body) {
		t.Fatal("failed refresh changed the durable pending registration")
	}
}

func TestRegistrationRecovery_IdentityPersistFailureRestoresAllMemory(t *testing.T) {
	signer := newR3ASigner(t)
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	before := recoveryPrepared(t, signer, "fcm-token-recovery")
	if err := core.storePendingRegister(before); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := core.push.path
	core.push.path = filepath.Join(blocked, "push-state.sealed")
	if err := core.persistPushIdentity("unexpected-installation", before.FCMToken); err == nil {
		t.Fatal("persisted identity through injected pre-commit failure")
	}
	core.push.path = path
	core.mu.Lock()
	id, token, pending := core.push.data.InstallationID, core.push.data.LastFCMToken, core.push.data.PendingRegister
	core.mu.Unlock()
	if id != "" || token != "" || pending == nil || pending.IdemKey != before.IdemKey || !bytes.Equal(pending.Body, before.Body) {
		t.Fatal("uncommitted identity write changed live push state")
	}
	resumed := phone.resume(t)
	resumed.mu.Lock()
	id, token, pending = resumed.push.data.InstallationID, resumed.push.data.LastFCMToken, resumed.push.data.PendingRegister
	resumed.mu.Unlock()
	if id != "" || token != "" || pending == nil || pending.IdemKey != before.IdemKey || !bytes.Equal(pending.Body, before.Body) {
		t.Fatal("uncommitted identity write changed durable push state")
	}
}

func TestRegistrationRecovery_ResolvedPendingRotatesCurrentToken(t *testing.T) {
	hs := r3aGateway(t, &r3aSender{}, &r3aAttestVerifier{licensed: true})
	signer := newR3ASigner(t)
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	lost := &r3aRecordingTransport{inner: hs.Client().Transport, swallow: func(_ int, r *http.Request) bool {
		return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/installations")
	}}
	core := phone.resume(t)
	if _, err := core.EnsurePushRegistration(context.Background(),
		NewGatewayClient(hs.URL, signer, r3aAttestor(t), &http.Client{Transport: lost, Timeout: time.Second}),
		staticToken("fcm-token-recovery")); !errors.Is(err, errRegisterOutcomeUnknown) {
		t.Fatalf("seed unknown=%v, want outcome unknown", err)
	}

	replay := &r3aRecordingTransport{inner: hs.Client().Transport}
	if _, err := phone.resume(t).EnsurePushRegistration(context.Background(),
		NewGatewayClient(hs.URL, signer, r3aAttestor(t), &http.Client{Transport: replay, Timeout: time.Second}),
		staticToken("fcm-token-recovery")); err != nil {
		t.Fatalf("resolve pending registration: %v", err)
	}
	rotates := 0
	for _, request := range replay.recorded() {
		if request.method == http.MethodPut && strings.HasPrefix(request.path, "/v1/installations/") {
			rotates++
		}
	}
	if rotates != 1 {
		t.Fatalf("same-token pending recovery rotated %d times, want one", rotates)
	}
}

func TestRegistrationRecovery_RotateFailureKeepsResolvedIdentity(t *testing.T) {
	hs := r3aGateway(t, &r3aSender{}, &r3aAttestVerifier{licensed: true})
	signer := newR3ASigner(t)
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	lost := &r3aRecordingTransport{inner: hs.Client().Transport, swallow: func(_ int, r *http.Request) bool {
		return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/installations")
	}}
	if _, err := phone.resume(t).EnsurePushRegistration(context.Background(),
		NewGatewayClient(hs.URL, signer, r3aAttestor(t), &http.Client{Transport: lost, Timeout: time.Second}),
		staticToken("fcm-token-recovery")); !errors.Is(err, errRegisterOutcomeUnknown) {
		t.Fatalf("seed unknown=%v, want outcome unknown", err)
	}
	droppedRotate := &r3aRecordingTransport{inner: hs.Client().Transport, swallow: func(_ int, r *http.Request) bool {
		return r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/installations/")
	}}
	core := phone.resume(t)
	if _, err := core.EnsurePushRegistration(context.Background(),
		NewGatewayClient(hs.URL, signer, r3aAttestor(t), &http.Client{Transport: droppedRotate, Timeout: time.Second}),
		staticToken("fcm-token-recovery")); err == nil {
		t.Fatal("lost rotate response reported recovered registration ready")
	}
	core.mu.Lock()
	id, pending := core.push.data.InstallationID, core.push.data.PendingRegister
	core.mu.Unlock()
	if id == "" || pending != nil {
		t.Fatal("lost rotate response discarded the already durable recovered identity")
	}
	retry := &r3aRecordingTransport{inner: hs.Client().Transport}
	if _, err := phone.resume(t).EnsurePushRegistration(context.Background(),
		NewGatewayClient(hs.URL, signer, r3aAttestor(t), &http.Client{Transport: retry, Timeout: time.Second}),
		staticToken("fcm-token-recovery")); err != nil {
		t.Fatalf("restart after lost rotate: %v", err)
	}
	posts, rotates := 0, 0
	for _, request := range retry.recorded() {
		if request.method == http.MethodPost && strings.HasSuffix(request.path, "/v1/installations") {
			posts++
		}
		if request.method == http.MethodPut && strings.HasPrefix(request.path, "/v1/installations/") {
			rotates++
		}
	}
	if posts != 0 || rotates != 1 {
		t.Fatalf("restart requests POST=%d PUT=%d, want no registration and one token rotation", posts, rotates)
	}
}

// TestRegistrationRecovery_ExpiredPreCommitUnknownReattestsSameIntent is the liveness
// half of PG-REG-2: an outage before any server commit leaves an honest unknown, but its
// short-lived Play verdict cannot become a permanent foreground-only wedge. Recovery keeps
// the idempotency key and installation authority, refreshes only the attestation-bearing body,
// and mints at most one installation through the real gateway.
func TestRegistrationRecovery_ExpiredPreCommitUnknownReattestsSameIntent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	attestor := &registrationRecoveryAttestor{}
	hs := r3aGateway(t, &r3aSender{}, attestor)
	signer := newR3ASigner(t)
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	outage := &registrationRecoveryOutage{}

	_, err := core.EnsurePushRegistration(ctx,
		NewGatewayClient(hs.URL, signer, attestor.attest, &http.Client{Transport: outage, Timeout: 2 * time.Second}),
		staticToken("fcm-token-recovery"))
	if !errors.Is(err, errRegisterOutcomeUnknown) {
		t.Fatalf("pre-commit outage = %v, want outcome unknown", err)
	}
	if outage.count() == 0 {
		t.Fatal("test did not attempt registration before the simulated outage")
	}
	core.mu.Lock()
	if core.push.data.PendingRegister == nil {
		core.mu.Unlock()
		t.Fatal("pre-commit outcome unknown lost its durable registration")
	}
	pending := *core.push.data.PendingRegister
	pending.Body = append([]byte(nil), pending.Body...)
	core.mu.Unlock()
	if pending.IdemKey == "" {
		t.Fatal("outcome-unknown registration lost its durable idempotency key")
	}
	var original registerBody
	if err := json.Unmarshal(pending.Body, &original); err != nil {
		t.Fatalf("decode pending registration: %v", err)
	}

	// Logical time advances past the old verdict. Only a fresh attestation is accepted.
	attestor.allow(2)
	capture := &registrationRecoveryCapture{inner: hs.Client().Transport}
	restarted := phone.resume(t)
	reg, err := restarted.EnsurePushRegistration(ctx,
		NewGatewayClient(hs.URL, signer, attestor.attest, &http.Client{Transport: capture, Timeout: 2 * time.Second}),
		staticToken("fcm-token-recovery"))
	if err != nil {
		t.Fatalf("expired pre-commit recovery: %v", err)
	}
	committed := capture.committed()
	if len(committed) != 1 {
		t.Fatalf("successful installations = %d, want exactly one", len(committed))
	}
	if reg.InstallationID != committed[0].installationID || restarted.PushInstallationID() != committed[0].installationID {
		t.Fatalf("recovered installation=%q durable=%q, committed=%q", reg.InstallationID, restarted.PushInstallationID(), committed[0].installationID)
	}
	if committed[0].idempotencyKey != pending.IdemKey {
		t.Fatalf("recovery idempotency key=%q, want pending %q", committed[0].idempotencyKey, pending.IdemKey)
	}
	var refreshed registerBody
	if err := json.Unmarshal(committed[0].body, &refreshed); err != nil {
		t.Fatalf("decode recovered registration: %v", err)
	}
	if refreshed.InstallationPublicKey != original.InstallationPublicKey || refreshed.FCMToken != original.FCMToken || refreshed.Attestation.Kind != original.Attestation.Kind {
		t.Fatal("recovery changed the installation authority or logical registration intent")
	}
	if refreshed.Attestation.Token == original.Attestation.Token || bytes.Equal(committed[0].body, pending.Body) {
		t.Fatal("recovery replayed the expired attestation instead of refreshing its proof")
	}
	if got := attestor.calls(); got != 2 {
		t.Fatalf("attestation calls=%d, want original plus one fresh proof", got)
	}
}
