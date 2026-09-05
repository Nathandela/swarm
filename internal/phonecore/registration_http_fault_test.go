package phonecore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// The gateway commits normally, then the HTTP peer sends fewer bytes than its
// Content-Length. This exercises a real net/http response-body failure, not a
// RoundTripper returning a fabricated error or response.
func TestRegisterHTTPFault_TruncatedCommittedResponseReplaysAfterRestart(t *testing.T) {
	gateway := r3aGateway(t, &r3aSender{}, &r3aAttestVerifier{licensed: true})
	var damageNext atomic.Bool
	damageNext.Store(true)
	minted := make(chan string, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded := httptest.NewRecorder()
		gateway.Config.Handler.ServeHTTP(recorded, r)
		body := recorded.Body.Bytes()
		for key, values := range recorded.Header() {
			w.Header()[key] = append([]string(nil), values...)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if recorded.Code == http.StatusCreated && damageNext.Swap(false) {
			var result struct {
				ID string `json:"installation_id"`
			}
			if err := json.Unmarshal(body, &result); err != nil || result.ID == "" {
				t.Errorf("real gateway did not return a valid committed registration: %v", err)
			}
			minted <- result.ID
			body = body[:len(body)/2]
		}
		w.WriteHeader(recorded.Code)
		_, _ = w.Write(body)
	}))
	t.Cleanup(peer.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	signer := newR3ASigner(t)
	attestCalls := 0
	attest := func(hash [32]byte) (string, error) {
		attestCalls++
		return r3aAttestor(t)(hash)
	}
	rt := &r3aRecordingTransport{inner: peer.Client().Transport}
	hc := &http.Client{Transport: rt, Timeout: 2 * time.Second}
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	_, err := core.EnsurePushRegistration(ctx, NewGatewayClient(peer.URL, signer, attest, hc), staticToken("fcm-http-fault"))
	if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, errRegisterOutcomeUnknown) {
		t.Fatalf("short HTTP body error = %v, want unexpected EOF and unknown outcome", err)
	}
	var originalID string
	select {
	case originalID = <-minted:
	default:
		t.Fatal("no committed gateway response was truncated")
	}
	if originalID == "" {
		t.Fatal("gateway committed no installation before truncation")
	}

	restarted := phone.resume(t)
	reg, err := restarted.EnsurePushRegistration(ctx, NewGatewayClient(peer.URL, signer, attest, hc), staticToken("fcm-http-fault"))
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if reg.InstallationID != originalID || restarted.PushInstallationID() != originalID {
		t.Fatal("restart did not recover the original committed installation")
	}
	requests := rt.recorded()
	if len(requests) != 2 || requests[0].method != http.MethodPost || requests[1].method != http.MethodPost {
		t.Fatalf("HTTP requests = %d, want two registration POSTs", len(requests))
	}
	if requests[0].idempotencyKey != requests[1].idempotencyKey || !bytes.Equal(requests[0].body, requests[1].body) {
		t.Fatal("restart changed the prepared body or idempotency key")
	}
	if attestCalls != 1 {
		t.Fatalf("attestation calls = %d, want one across the real HTTP fault and restart", attestCalls)
	}
}
