package accountusage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
)

const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
const claudeTokenURL = "https://platform.claude.com/v1/oauth/token"

type renewScript struct {
	usage, token int
	tokenBodies  []map[string]any
	bearers      []string
}

// renewClient answers usage GETs with usage(n) and token POSTs with token(n), both 1-based.
func renewClient(t *testing.T, script *renewScript, usage func(n int) (int, string), token func(n int) (int, string)) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var status int
		var body string
		switch request.URL.String() {
		case claudeUsageURL:
			script.usage++
			script.bearers = append(script.bearers, strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
			status, body = usage(script.usage)
		case claudeTokenURL:
			script.token++
			raw, _ := io.ReadAll(request.Body)
			var fields map[string]any
			if request.Method != http.MethodPost || json.Unmarshal(raw, &fields) != nil {
				t.Error("token request escaped the native contract")
			}
			script.tokenBodies = append(script.tokenBodies, fields)
			if token == nil {
				t.Error("unexpected token refresh")
				return nil, errors.New("unexpected")
			}
			status, body = token(script.token)
		default:
			t.Error("request to an unpinned destination")
			return nil, errors.New("unexpected")
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func readClaudeOAuth(t *testing.T, profile string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(profile, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file["claudeAiOauth"]
}

func writeClaudeCredentials(t *testing.T, profile, raw string) {
	t.Helper()
	staged := filepath.Join(profile, "synthetic.tmp")
	if err := os.WriteFile(staged, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, filepath.Join(profile, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
}

const usageOK = `{"five_hour":{"utilization":42}}`
const renewedTokens = `{"access_token":"synthetic-renewed","refresh_token":"synthetic-next-refresh","expires_in":28800}`

func TestClaudeUsage401RenewsOnceAndRetriesWithNewBearer(t *testing.T) {
	store, binding, profile := usageStore(t, accounts.ProviderClaude)
	script := &renewScript{}
	client := renewClient(t, script, func(n int) (int, string) {
		if n == 1 {
			return http.StatusUnauthorized, "synthetic-error"
		}
		return http.StatusOK, usageOK
	}, func(int) (int, string) { return http.StatusOK, renewedTokens })
	result, err := fetch(context.Background(), store, binding, client)
	if err != nil || script.usage != 2 || script.token != 1 {
		t.Fatalf("renewal did not recover usage: usage=%d token=%d error=%v", script.usage, script.token, err)
	}
	if script.bearers[0] != "synthetic-access" || script.bearers[1] != "synthetic-renewed" {
		t.Fatal("retry did not use the renewed bearer")
	}
	if *findScope(t, result, "bucket:claude:five_hour").UsedPercent != 42 {
		t.Fatal("retried usage was not parsed")
	}
	body := script.tokenBodies[0]
	if body["grant_type"] != "refresh_token" || body["refresh_token"] != "synthetic-refresh" || body["client_id"] != "9d1c250a-e61b-44d9-88ed-5944d1962f5e" || body["scope"] != "user:inference" || len(body) != 4 {
		t.Fatal("renewal did not post the default-client native refresh body")
	}
	oauth := readClaudeOAuth(t, profile)
	if oauth["accessToken"] != "synthetic-renewed" || oauth["refreshToken"] != "synthetic-next-refresh" || oauth["subscriptionType"] != "pro" {
		t.Fatal("renewed tokens were not saved with other keys preserved")
	}
}

func TestClaudeRenewalPostsStoredClientID(t *testing.T) {
	store, binding, profile := usageStore(t, accounts.ProviderClaude)
	writeClaudeCredentials(t, profile, `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","subscriptionType":"pro","scopes":["user:inference","user:profile"],"clientId":"synthetic-stored-client"}}`)
	script := &renewScript{}
	client := renewClient(t, script, func(n int) (int, string) {
		if n == 1 {
			return http.StatusUnauthorized, ""
		}
		return http.StatusOK, usageOK
	}, func(int) (int, string) { return http.StatusOK, renewedTokens })
	if _, err := fetch(context.Background(), store, binding, client); err != nil || script.token != 1 {
		t.Fatalf("renewal with stored client failed: token=%d error=%v", script.token, err)
	}
	if body := script.tokenBodies[0]; body["client_id"] != "synthetic-stored-client" || body["scope"] != "user:inference user:profile" {
		t.Fatal("renewal ignored the stored client id or scopes")
	}
}

func TestClaudeRenewalAdoptedBySiblingRetriesWithSiblingBearer(t *testing.T) {
	store, binding, profile := usageStore(t, accounts.ProviderClaude)
	script := &renewScript{}
	client := renewClient(t, script, func(n int) (int, string) {
		if n == 1 {
			return http.StatusUnauthorized, ""
		}
		return http.StatusOK, usageOK
	}, func(int) (int, string) {
		// A sibling CLI renewed while our refresh was in flight, consuming the old RT.
		writeClaudeCredentials(t, profile, `{"claudeAiOauth":{"accessToken":"synthetic-sibling","refreshToken":"synthetic-sibling-refresh","subscriptionType":"pro","scopes":["user:inference"]}}`)
		return http.StatusBadRequest, `{"error":"invalid_grant"}`
	})
	_, err := fetch(context.Background(), store, binding, client)
	if err != nil || script.usage != 2 || script.token != 1 || script.bearers[1] != "synthetic-sibling" {
		t.Fatalf("adopted renewal did not retry with sibling bearer: usage=%d token=%d error=%v", script.usage, script.token, err)
	}
}

func TestClaudeRenewalDeadRefreshTokenIsLoginExpired(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		store, binding, profile := usageStore(t, accounts.ProviderClaude)
		before, _ := os.ReadFile(filepath.Join(profile, ".credentials.json"))
		script := &renewScript{}
		client := renewClient(t, script, func(int) (int, string) { return http.StatusUnauthorized, "" }, func(int) (int, string) {
			return status, `{"error":"invalid_grant","error_description":"synthetic-secret"}`
		})
		_, err := fetch(context.Background(), store, binding, client)
		var failure *Error
		if !errors.As(err, &failure) || failure.Class != "login-expired" || err.Error() != "Sign-in expired for this account; sign in again." || script.usage != 1 || script.token != 1 {
			t.Fatalf("dead refresh token at %d was not login-expired: usage=%d token=%d", status, script.usage, script.token)
		}
		after, _ := os.ReadFile(filepath.Join(profile, ".credentials.json"))
		if string(before) != string(after) {
			t.Fatal("dead refresh rewrote native credentials")
		}
	}
}

func TestClaudeRenewalBusyLockIsUnavailable(t *testing.T) {
	store, binding, profile := usageStore(t, accounts.ProviderClaude)
	if err := os.Mkdir(filepath.Join(profile, ".oauth_refresh.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	script := &renewScript{}
	client := renewClient(t, script, func(int) (int, string) { return http.StatusUnauthorized, "" }, nil)
	_, err := fetch(context.Background(), store, binding, client)
	var failure *Error
	if !errors.As(err, &failure) || failure.Class != "unavailable" || script.usage != 1 || script.token != 0 {
		t.Fatalf("busy native lock was not unavailable: usage=%d token=%d error=%v", script.usage, script.token, err)
	}
}

func TestClaudeRenewalHappensAtMostOncePerFetch(t *testing.T) {
	store, binding, _ := usageStore(t, accounts.ProviderClaude)
	script := &renewScript{}
	client := renewClient(t, script, func(int) (int, string) { return http.StatusUnauthorized, "" }, func(int) (int, string) {
		return http.StatusOK, renewedTokens
	})
	_, err := fetch(context.Background(), store, binding, client)
	var failure *Error
	if !errors.As(err, &failure) || failure.Class != "auth-required" || script.usage != 2 || script.token != 1 {
		t.Fatalf("renewal loop was not bounded: usage=%d token=%d error=%v", script.usage, script.token, err)
	}
}

func TestClaudeRenewalSkippedWhenNativeSnapshotAlreadyChanged(t *testing.T) {
	store, binding, profile := usageStore(t, accounts.ProviderClaude)
	script := &renewScript{}
	client := renewClient(t, script, func(n int) (int, string) {
		if n == 1 {
			writeClaudeCredentials(t, profile, `{"claudeAiOauth":{"accessToken":"synthetic-next","refreshToken":"synthetic-refresh","subscriptionType":"pro","scopes":["user:inference"]}}`)
			return http.StatusUnauthorized, ""
		}
		return http.StatusOK, usageOK
	}, nil)
	if _, err := fetch(context.Background(), store, binding, client); err != nil || script.usage != 2 || script.token != 0 || script.bearers[1] != "synthetic-next" {
		t.Fatalf("changed snapshot retry regressed: usage=%d token=%d error=%v", script.usage, script.token, err)
	}
}

func TestCodexUsage401NeverRenews(t *testing.T) {
	store, binding, _ := usageStore(t, accounts.ProviderCodex)
	usage, other := 0, 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == "https://chatgpt.com/backend-api/wham/usage" {
			usage++
		} else {
			other++
		}
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})}
	_, err := fetch(context.Background(), store, binding, client)
	var failure *Error
	if !errors.As(err, &failure) || failure.Class != "auth-required" || usage != 1 || other != 0 {
		t.Fatalf("codex 401 path changed: usage=%d other=%d", usage, other)
	}
}

func TestClaudeRenewalTokenEndpointFailureKeepsItsClass(t *testing.T) {
	store, binding, _ := usageStore(t, accounts.ProviderClaude)
	script := &renewScript{}
	client := renewClient(t, script, func(int) (int, string) { return http.StatusUnauthorized, "" }, func(int) (int, string) {
		return http.StatusTooManyRequests, ""
	})
	_, err := fetch(context.Background(), store, binding, client)
	var failure *Error
	if !errors.As(err, &failure) || failure.Class != "rate-limited" || script.usage != 1 || script.token != 1 {
		t.Fatalf("token endpoint 429 was reported as %v: usage=%d token=%d", err, script.usage, script.token)
	}
}
