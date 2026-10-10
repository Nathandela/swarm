package accountusage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
)

// tlsExchangeClient returns a hardened client (no proxy, no redirects) whose every dial reaches
// the TLS test server, so the pinned production endpoint is exercised without the network.
func tlsExchangeClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	transport := server.Client().Transport.(*http.Transport).Clone()
	addr := server.Listener.Addr().String()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	transport.TLSClientConfig.ServerName = "example.com"
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestClaudeExchangePostsPinnedRefreshRequestAndParsesTokens(t *testing.T) {
	for _, clientID := range []string{"9d1c250a-e61b-44d9-88ed-5944d1962f5e", "synthetic-stored-client"} {
		t.Run(clientID, func(t *testing.T) {
			calls := 0
			client := tlsExchangeClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				raw, _ := io.ReadAll(r.Body)
				var body map[string]any
				if r.Method != http.MethodPost || r.Host != "platform.claude.com" || r.URL.Path != "/v1/oauth/token" || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" || json.Unmarshal(raw, &body) != nil {
					t.Error("refresh request escaped the pinned native contract")
				}
				want := map[string]any{"grant_type": "refresh_token", "refresh_token": "synthetic-refresh", "client_id": clientID, "scope": "user:inference user:profile"}
				if !reflect.DeepEqual(body, want) {
					t.Errorf("refresh body fields differ from the native protocol: keys=%d", len(body))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"access_token":"synthetic-renewed","refresh_token":"synthetic-next-refresh","expires_in":28800,"refresh_token_expires_in":2592000,"scope":"user:inference user:profile","account":{"uuid":"synthetic-id","email_address":"synthetic@example.com"},"organization":{"uuid":"synthetic-org"},"token_type":"Bearer"}`)
			})
			tokens, err := exchangeClaude(client)(context.Background(), "synthetic-refresh", clientID, []string{"user:inference", "user:profile"})
			if err != nil || calls != 1 {
				t.Fatalf("exchange failed: calls=%d", calls)
			}
			want := accounts.ClaudeTokens{AccessToken: "synthetic-renewed", RefreshToken: "synthetic-next-refresh", ExpiresIn: 28800, RefreshTokenExpiresIn: 2592000, Scopes: []string{"user:inference", "user:profile"}, AccountUUID: "synthetic-id", OrganizationUUID: "synthetic-org"}
			if !reflect.DeepEqual(tokens, want) {
				t.Fatal("refresh response was not mapped to ClaudeTokens")
			}
		})
	}
}

func TestClaudeExchangeKeepsOptionalFieldsAbsent(t *testing.T) {
	client := tlsExchangeClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"synthetic-renewed","expires_in":3600}`)
	})
	tokens, err := exchangeClaude(client)(context.Background(), "synthetic-refresh", "synthetic-client", []string{"user:inference"})
	if err != nil {
		t.Fatal("minimal refresh response rejected")
	}
	if !reflect.DeepEqual(tokens, accounts.ClaudeTokens{AccessToken: "synthetic-renewed", ExpiresIn: 3600}) {
		t.Fatal("absent optional fields were invented")
	}
}

func TestClaudeExchangeMapsInvalidGrantToRefreshRejected(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		client := tlsExchangeClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"synthetic-secret-description"}`)
		})
		_, err := exchangeClaude(client)(context.Background(), "synthetic-refresh", "synthetic-client", nil)
		if !errors.Is(err, accounts.ErrRefreshRejected) || strings.Contains(err.Error(), "synthetic") {
			t.Fatalf("invalid_grant at %d was not a safe refresh rejection", status)
		}
	}
}

func TestClaudeExchangeFailuresAreClassifiedAndSafe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header string
		body   string
		class  string
		retry  time.Duration
	}{
		{"rate-limited", http.StatusTooManyRequests, "120", `{"error":"rate_limited"}`, "rate-limited", 2 * time.Minute},
		{"server-error", http.StatusInternalServerError, "", "synthetic-secret-body", "unavailable", 0},
		{"other-oauth-error", http.StatusBadRequest, "", `{"error":"invalid_request"}`, "unavailable", 0},
		{"unauthorized-not-grant", http.StatusUnauthorized, "", `{"error":"invalid_client"}`, "unavailable", 0},
		{"duplicate-key", http.StatusOK, "", `{"access_token":"synthetic-a","access_token":"synthetic-b","expires_in":3600}`, "malformed", 0},
		{"not-json", http.StatusOK, "", "synthetic-secret-body", "malformed", 0},
		{"trailing-json", http.StatusOK, "", `{"access_token":"synthetic-a","expires_in":3600} {}`, "malformed", 0},
		{"wrong-type", http.StatusOK, "", `{"access_token":"synthetic-a","expires_in":"3600"}`, "malformed", 0},
		{"oversized", http.StatusOK, "", strings.Repeat(" ", maxBody+1), "malformed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := tlsExchangeClient(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := exchangeClaude(client)(context.Background(), "synthetic-refresh", "synthetic-client", nil)
			var failure *Error
			if !errors.As(err, &failure) || failure.Class != tc.class || failure.RetryAfter != tc.retry || errors.Is(err, accounts.ErrRefreshRejected) || strings.Contains(err.Error(), "synthetic") {
				t.Fatalf("refresh failure %s misclassified or unsafe", tc.name)
			}
		})
	}
}

func TestClaudeExchangeDoesNotFollowRedirects(t *testing.T) {
	redirected := 0
	client := tlsExchangeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/oauth/token" {
			redirected++
			_, _ = io.WriteString(w, `{"access_token":"synthetic-renewed","expires_in":3600}`)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	_, err := exchangeClaude(client)(context.Background(), "synthetic-refresh", "synthetic-client", nil)
	var failure *Error
	if redirected != 0 || !errors.As(err, &failure) || failure.Class != "unavailable" {
		t.Fatalf("refresh followed a redirect: hits=%d", redirected)
	}
}

func TestClaudeExchangeTransportAndCancellationFailures(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic-private-token-in-error")
	})}
	_, err := exchangeClaude(client)(context.Background(), "synthetic-refresh", "synthetic-client", nil)
	var failure *Error
	if !errors.As(err, &failure) || failure.Class != "network" || strings.Contains(err.Error(), "synthetic") {
		t.Fatal("raw transport error escaped the refresh")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = exchangeClaude(client)(ctx, "synthetic-refresh", "synthetic-client", nil)
	if err == nil || errors.Is(err, accounts.ErrRefreshRejected) {
		t.Fatal("cancelled refresh succeeded or claimed rejection")
	}
}
