package phonecore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type registrationResponseMutationTransport struct {
	inner  http.RoundTripper
	mutate func(*http.Response, []byte, string)

	mu       sync.Mutex
	mintedID string
}

func (t *registrationResponseMutationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		return resp, err
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	var body struct {
		InstallationID string `json:"installation_id"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.InstallationID == "" {
		return nil, errors.New("test fixture: real gateway returned no installation id")
	}
	t.mu.Lock()
	t.mintedID = body.InstallationID
	t.mu.Unlock()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	t.mutate(resp, raw, body.InstallationID)
	return resp, nil
}

func (t *registrationResponseMutationTransport) minted() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.mintedID
}

type registrationReadErrorBody struct {
	done bool
}

func (b *registrationReadErrorBody) Read(p []byte) (int, error) {
	if b.done {
		return 0, errors.New("injected response truncation")
	}
	b.done = true
	return copy(p, `{"installation_id":`), nil
}

func (*registrationReadErrorBody) Close() error { return nil }

type registrationStaticResponseTransport struct {
	status int
	body   string

	mu       sync.Mutex
	requests []r3aWireRequest
}

func (t *registrationStaticResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.requests = append(t.requests, r3aWireRequest{
		method:            req.Method,
		path:              req.URL.Path,
		idempotencyKey:    req.Header.Get("Idempotency-Key"),
		registrationProof: req.Header.Get("Swarm-Registration-Proof"),
		body:              body,
	})
	t.mu.Unlock()
	return &http.Response{
		StatusCode: t.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Request:    req,
	}, nil
}

func (t *registrationStaticResponseTransport) recorded() []r3aWireRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]r3aWireRequest(nil), t.requests...)
}

func TestRegisterOutcome_CommittedUncertainResponsePreservesExactReplay(t *testing.T) {
	tests := []struct {
		name, wantErr string
		mutate        func(*http.Response, []byte, string)
	}{
		{
			name: "malformed JSON",
			mutate: func(resp *http.Response, _ []byte, _ string) {
				resp.Body = io.NopCloser(strings.NewReader("{"))
			},
		},
		{
			name: "truncated body read",
			mutate: func(resp *http.Response, _ []byte, _ string) {
				resp.Body = &registrationReadErrorBody{}
			},
		},
		{
			name: "empty installation id",
			mutate: func(resp *http.Response, _ []byte, _ string) {
				resp.Body = io.NopCloser(strings.NewReader(`{"installation_id":"","refresh_before":"2030-01-01T00:00:00Z"}`))
			},
		},
		{
			name: "missing refresh before",
			mutate: func(resp *http.Response, _ []byte, id string) {
				resp.Body = io.NopCloser(strings.NewReader(`{"installation_id":"` + id + `"}`))
			},
		},
		{
			name: "invalid refresh before",
			mutate: func(resp *http.Response, _ []byte, id string) {
				resp.Body = io.NopCloser(strings.NewReader(`{"installation_id":"` + id + `","refresh_before":"not-a-time"}`))
			},
		},
		{
			name: "noncanonical installation id",
			mutate: func(resp *http.Response, _ []byte, id string) {
				const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
				changed := []byte(id)
				last := strings.IndexByte(alphabet, changed[len(changed)-1])
				changed[len(changed)-1] = alphabet[last+1]
				resp.Body = io.NopCloser(strings.NewReader(`{"installation_id":"` + string(changed) + `","refresh_before":"2030-01-01T00:00:00Z"}`))
			},
		},
		{
			name: "unknown field",
			mutate: func(resp *http.Response, _ []byte, id string) {
				resp.Body = io.NopCloser(strings.NewReader(`{"installation_id":"` + id + `","refresh_before":"2030-01-01T00:00:00Z","extra":true}`))
			},
		},
		{
			name: "duplicate installation id",
			mutate: func(resp *http.Response, _ []byte, id string) {
				resp.Body = io.NopCloser(strings.NewReader(`{"installation_id":"` + id + `","installation_id":"` + id + `","refresh_before":"2030-01-01T00:00:00Z"}`))
			},
		},
		{
			name: "wrong case field",
			mutate: func(resp *http.Response, _ []byte, id string) {
				resp.Body = io.NopCloser(strings.NewReader(`{"Installation_ID":"` + id + `","refresh_before":"2030-01-01T00:00:00Z"}`))
			},
		},
		{
			name: "wrong content type",
			mutate: func(resp *http.Response, _ []byte, _ string) {
				resp.Header.Set("Content-Type", "text/plain")
			},
		},
		{
			name:    "oversized body",
			wantErr: "exceeds size limit",
			mutate: func(resp *http.Response, _ []byte, _ string) {
				resp.Body = io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), 64*1024+1)))
			},
		},
		{
			name: "internal after commit",
			mutate: func(resp *http.Response, _ []byte, _ string) {
				resp.StatusCode = http.StatusInternalServerError
				resp.Body = io.NopCloser(strings.NewReader(`{"code":"internal","retryable":true}`))
			},
		},
		{
			name: "unavailable after commit",
			mutate: func(resp *http.Response, _ []byte, _ string) {
				resp.StatusCode = http.StatusServiceUnavailable
				resp.Body = io.NopCloser(strings.NewReader(`{"code":"service_unavailable","retryable":true}`))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hs := r3aGateway(t, &r3aSender{}, &r3aAttestVerifier{licensed: true})
			transport := &registrationResponseMutationTransport{inner: hs.Client().Transport, mutate: tt.mutate}
			signer := newR3ASigner(t)
			attestCalls := 0
			attest := func(hash [32]byte) (string, error) {
				attestCalls++
				return "attest:" + base64.RawURLEncoding.EncodeToString(hash[:]), nil
			}
			phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
			core := phone.resume(t)
			_, err := core.EnsurePushRegistration(context.Background(),
				NewGatewayClient(hs.URL, signer, attest, &http.Client{Transport: transport}),
				staticToken("fcm-token-outcome"))
			if err == nil {
				t.Fatal("uncertain response was accepted")
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("response error=%q, want %q", err, tt.wantErr)
			}
			mintedID := transport.minted()
			if mintedID == "" {
				t.Fatal("real gateway did not commit before response mutation")
			}
			core.mu.Lock()
			pending := core.push.data.PendingRegister
			if pending != nil {
				pending = &pendingRegisterRec{IdemKey: pending.IdemKey, Body: append([]byte(nil), pending.Body...), FCMToken: pending.FCMToken}
			}
			core.mu.Unlock()
			if pending == nil {
				t.Fatal("uncertain response discarded a registration the server already committed")
			}

			replayTransport := &r3aRecordingTransport{inner: hs.Client().Transport}
			restarted := phone.resume(t)
			reg, err := restarted.EnsurePushRegistration(context.Background(),
				NewGatewayClient(hs.URL, signer, attest, &http.Client{Transport: replayTransport}),
				staticToken("fcm-token-outcome"))
			if err != nil {
				t.Fatalf("exact replay: %v", err)
			}
			if reg.InstallationID != mintedID || restarted.PushInstallationID() != mintedID {
				t.Fatalf("replay ID=%q durable=%q, want originally minted %q", reg.InstallationID, restarted.PushInstallationID(), mintedID)
			}
			requests := replayTransport.recorded()
			if len(requests) != 1 || requests[0].idempotencyKey != pending.IdemKey || !bytes.Equal(requests[0].body, pending.Body) {
				t.Fatalf("replay changed or duplicated pending request: %+v", requests)
			}
			if attestCalls != 1 {
				t.Fatalf("attestation calls=%d, want one across commit and exact replay", attestCalls)
			}
		})
	}
}

func TestRegisterOutcome_PriorUnknownSurvivesEveryLaterNonSuccess(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "internal", status: http.StatusInternalServerError, body: `{"code":"internal","retryable":true}`},
		{name: "in progress", status: http.StatusServiceUnavailable, body: `{"code":"service_unavailable","retryable":true}`},
		{name: "later attestation refusal", status: http.StatusForbidden, body: `{"code":"attestation_invalid","retryable":false}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hs := r3aGateway(t, &r3aSender{}, &r3aAttestVerifier{licensed: true})
			capture := &registrationIDCaptureTransport{inner: hs.Client().Transport}
			lost := &r3aRecordingTransport{inner: capture, swallow: func(_ int, r *http.Request) bool {
				return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/installations")
			}}
			signer := newR3ASigner(t)
			attestCalls := 0
			attest := func(hash [32]byte) (string, error) {
				attestCalls++
				return "attest:" + base64.RawURLEncoding.EncodeToString(hash[:]), nil
			}
			phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
			core := phone.resume(t)
			_, err := core.EnsurePushRegistration(context.Background(),
				NewGatewayClient(hs.URL, signer, attest, &http.Client{Transport: lost}),
				staticToken("fcm-token-outcome"))
			if !errors.Is(err, errRegisterOutcomeUnknown) {
				t.Fatalf("seed lost response=%v, want outcome unknown", err)
			}
			core.mu.Lock()
			pending := *core.push.data.PendingRegister
			pending.Body = append([]byte(nil), pending.Body...)
			core.mu.Unlock()

			refusal := &registrationStaticResponseTransport{status: tt.status, body: tt.body}
			restarted := phone.resume(t)
			_, err = restarted.EnsurePushRegistration(context.Background(),
				NewGatewayClient("http://gateway.invalid", signer, attest, &http.Client{Transport: refusal}),
				staticToken("fcm-token-outcome"))
			if !errors.Is(err, errRegisterOutcomeUnknown) {
				t.Fatalf("later response=%v, want earlier outcome to remain unknown", err)
			}
			requests := refusal.recorded()
			if len(requests) != 1 || requests[0].idempotencyKey != pending.IdemKey || !bytes.Equal(requests[0].body, pending.Body) {
				t.Fatalf("later response minted or changed registration: %+v", requests)
			}
			restarted.mu.Lock()
			after := restarted.push.data.PendingRegister
			if after == nil || after.IdemKey != pending.IdemKey || !bytes.Equal(after.Body, pending.Body) {
				t.Error("later response discarded or changed prior outcome-unknown registration")
			}
			restarted.mu.Unlock()
			if attestCalls != 1 {
				t.Fatalf("attestation calls=%d, want no fresh body after later response", attestCalls)
			}
		})
	}
}

func TestRegisterOutcome_FirstDefinitiveRefusalStillClearsPreparedPair(t *testing.T) {
	transport := &registrationStaticResponseTransport{
		status: http.StatusForbidden,
		body:   `{"code":"attestation_invalid","retryable":false}`,
	}
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	_, err := core.EnsurePushRegistration(context.Background(),
		NewGatewayClient("http://gateway.invalid", newR3ASigner(t), r3aAttestor(t), &http.Client{Transport: transport}),
		staticToken("fcm-token-definitive"))
	if !errors.Is(err, ErrAttestationRefused) {
		t.Fatalf("first definitive refusal=%v, want ErrAttestationRefused", err)
	}
	restarted := phone.resume(t)
	restarted.mu.Lock()
	pending := restarted.push.data.PendingRegister
	restarted.mu.Unlock()
	if pending != nil {
		t.Fatal("first definitive refusal retained a never-committed registration")
	}
}
