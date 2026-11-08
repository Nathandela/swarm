package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func bridgeFixture(t *testing.T, handler http.HandlerFunc) *ManagerBridgeClient {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "mb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "b.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		_ = l.Close()
		t.Fatal(err)
	}
	s := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close(); <-done })
	if err := writeVoiceFile(dir, "auth", []byte(strings.Repeat("a", 64))); err != nil {
		t.Fatal(err)
	}
	c, err := NewManagerBridgeClient(socket, filepath.Join(dir, "auth"), "conversation", "session")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestManagerBridgeAuthenticatedExactEnvelope(t *testing.T) {
	c := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manager/call" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			t.Error("invalid authenticated route")
		}
		var req ManagerBridgeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Version != 1 || req.ConversationID != "conversation" || req.SessionID != "session" || req.Call.Name != "publish" || req.Call.Meta["callId"] != "native-call" {
			t.Error("lost manager binding")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"state":"published"}}`)
	})
	raw, err := c.Call(context.Background(), ManagerToolCall{Name: "publish", Arguments: json.RawMessage(`{"text":"hello"}`), Meta: map[string]any{"callId": "native-call"}})
	if err != nil || string(raw) != `{"state":"published"}` {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestManagerBridgeRejectsUntrustedResponsesWithoutDisclosure(t *testing.T) {
	for name, body := range map[string]string{
		"unknown_error": `{"error":"echoed-secret"}`,
		"missing":       `{}`,
		"unknown_field": `{"data":{},"extra":"echoed-secret"}`,
		"bad_unicode":   `{"data":{"text":"\ud800"}}`,
		"oversized":     strings.Repeat("x", MaxControlFrameBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			c := bridgeFixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
			_, err := c.Call(context.Background(), ManagerToolCall{Name: "swarm_list", Arguments: json.RawMessage(`{}`)})
			if err == nil {
				t.Fatal("accepted invalid response")
			}
			if strings.Contains(err.Error(), "echoed-secret") {
				t.Fatal("response was disclosed")
			}
		})
	}
}

func TestManagerBridgeDoesNotRetryLostReply(t *testing.T) {
	var calls atomic.Int64
	c := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	_, err := c.Call(context.Background(), ManagerToolCall{Name: "swarm_create", Arguments: json.RawMessage(`{}`)})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("lost response calls=%d err=%v", calls.Load(), err)
	}
}

func TestManagerBridgeBoundsCanonicalResult(t *testing.T) {
	body := `{"data":{"text":"` + strings.Repeat("<", MaxUtteranceBytes-32) + `"}}`
	c := bridgeFixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
	if raw, err := c.Call(context.Background(), ManagerToolCall{Name: "swarm_read", Arguments: json.RawMessage(`{}`)}); !errors.Is(err, errManagerPayloadTooLarge) || len(raw) != 0 {
		t.Fatalf("oversized canonical result was not refused: bytes=%d err=%v", len(raw), err)
	}
}

func TestManagerBridgeCancellationAndRedirect(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		c := bridgeFixture(t, func(_ http.ResponseWriter, r *http.Request) {
			close(started)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		})
		defer close(release)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := c.Call(ctx, ManagerToolCall{Name: "swarm_list", Arguments: json.RawMessage(`{}`)})
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("request not started")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancel stalled")
		}
	})
	t.Run("redirect", func(t *testing.T) {
		var calls atomic.Int64
		c := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Location", "http://elsewhere/credential")
			w.WriteHeader(http.StatusTemporaryRedirect)
		})
		_, err := c.Call(context.Background(), ManagerToolCall{Name: "swarm_list", Arguments: json.RawMessage(`{}`)})
		if err == nil || calls.Load() != 1 {
			t.Fatal(fmt.Sprintf("redirect followed, calls %d", calls.Load()), err)
		}
	})
}
