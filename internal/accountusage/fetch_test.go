package accountusage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func usageStore(t *testing.T, provider string) (*accounts.Store, accounts.Binding, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := accounts.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	candidate, err := store.CreateCandidate(provider, accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"auth.json": `{"auth_mode":"chatgpt","tokens":{"account_id":"synthetic-id","access_token":"synthetic-access","refresh_token":"synthetic-refresh"}}`}
	if provider == accounts.ProviderClaude {
		files = map[string]string{
			".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","subscriptionType":"pro","scopes":["user:inference"]}}`,
			".claude.json":      `{"oauthAccount":{"accountUuid":"synthetic-id","organizationUuid":"synthetic-org"}}`,
		}
	}
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(candidate.ProfilePath, name), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err = store.VerifyCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Snapshot()
	_, account, err := store.Admit(snapshot.Revision, candidate, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.CurrentBinding(account.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return store, binding, candidate.ProfilePath
}

func fakeClient(status int, body string, inspect func(*http.Request)) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if inspect != nil {
			inspect(request)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func findScope(t *testing.T, result Result, scope string) accounts.ScopeObservation {
	t.Helper()
	for _, observation := range result.Observations {
		if observation.Scope == scope {
			return observation
		}
	}
	t.Fatalf("missing usage scope %s", scope)
	return accounts.ScopeObservation{}
}

func TestCodexHTTPUsageNormalizesPinnedWireWithoutModelRequests(t *testing.T) {
	store, binding, profile := usageStore(t, accounts.ProviderCodex)
	before, _ := os.ReadFile(filepath.Join(profile, "auth.json"))
	payload := `{"account_id":"synthetic-id","rate_limit":{"allowed":true,"primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_at":1900000000},"secondary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_at":null}},"additional_rate_limits":[{"metered_feature":"codex_other","limit_name":"Other model","normal_model_slug":"gpt-other","rate_limit":{"allowed":false,"primary_window":{"used_percent":89,"reset_at":1900001000}}}],"opaque_future":{"safe":true}}`
	client := fakeClient(http.StatusOK, payload, func(request *http.Request) {
		if request.Method != http.MethodGet || request.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || request.Body != nil || request.Header.Get("Authorization") != "Bearer synthetic-access" || request.Header.Get("ChatGPT-Account-Id") != "synthetic-id" || request.Header.Get("User-Agent") != "codex-cli" || request.Header.Get("x-openai-codex-luna-reserve") != "" {
			t.Fatal("request escaped pinned passive usage contract")
		}
	})
	result, err := fetch(context.Background(), store, binding, client)
	if err != nil {
		t.Fatal(err)
	}
	primary := findScope(t, result, "bucket:codex:primary")
	secondary := findScope(t, result, "bucket:codex:secondary")
	additional := findScope(t, result, "bucket:codex_other:primary")
	if *primary.UsedPercent != 12 || !primary.ResetPresent || primary.ResetAt.Unix() != 1900000000 || result.WindowSeconds[primary.Scope] != 18000 || result.Labels[additional.Scope] != "Other model primary window" {
		t.Fatal("native window metadata was lost")
	}
	if *secondary.UsedPercent != 100 || !secondary.ResetPresent || secondary.ResetAt != nil || secondary.Authority != accounts.AuthorityAllowed || additional.Model != "gpt-other" || additional.Authority != accounts.AuthorityDenied {
		t.Fatal("percentage inferred permission or model-scoped evidence was lost")
	}
	after, _ := os.ReadFile(filepath.Join(profile, "auth.json"))
	if string(before) != string(after) {
		t.Fatal("usage fetch rewrote native credentials")
	}
}

func TestClaudeHTTPUsageRemainsTelemetryAndPreservesSparseValues(t *testing.T) {
	store, binding, _ := usageStore(t, accounts.ProviderClaude)
	payload := `{"five_hour":{"utilization":46.6,"resets_at":"2030-02-03T04:05:06.123456+02:00","limit_dollars":null},"seven_day":{"utilization":100,"resets_at":null},"seven_day_opus":{"resets_at":null},"seven_day_sonnet":null,"extra_usage":{"is_enabled":true,"spend_limit_reached":false,"utilization":100},"unknown_future":true}`
	result, err := fetch(context.Background(), store, binding, fakeClient(http.StatusOK, payload, func(request *http.Request) {
		if request.URL.String() != "https://api.anthropic.com/api/oauth/usage" || request.Header.Get("anthropic-beta") != "oauth-2025-04-20" || request.Header.Get("ChatGPT-Account-Id") != "" {
			t.Fatal("Claude usage contract changed")
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	five := findScope(t, result, "bucket:claude:five_hour")
	seven := findScope(t, result, "bucket:claude:seven_day")
	opus := findScope(t, result, "bucket:claude:seven_day_opus")
	if *five.UsedPercent != 47 || five.ResetAt.Format(time.RFC3339Nano) != "2030-02-03T02:05:06.123456Z" || *seven.UsedPercent != 100 || !seven.ResetPresent || seven.ResetAt != nil || opus.UsedPercent != nil || opus.Model == "" {
		t.Fatal("Claude sparse usage or reset distinction was lost")
	}
	for _, scope := range result.Observations {
		if scope.Authority != accounts.AuthorityUnknown {
			t.Fatal("Claude usage became model permission")
		}
	}
	result, err = fetch(context.Background(), store, binding, fakeClient(http.StatusOK, `{"five_hour":{"utilization":0}}`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if observation := findScope(t, result, "bucket:claude:five_hour"); *observation.UsedPercent != 0 || observation.ResetPresent {
		t.Fatal("absent reset overwrote prior reset")
	}
}

func TestUsageMalformedOrAmbiguousResponsesFailClosed(t *testing.T) {
	for provider, payloads := range map[string][]string{
		accounts.ProviderCodex: {
			`{"account_id":"other-account","rate_limit":{"allowed":true}}`,
			`{"rate_limit":{"allowed":"true"}}`,
			`{"rate_limit":{"primary_window":{"used_percent":101}}}`,
			`{"rate_limit":{"primary_window":{"used_percent":1.5}}}`,
			`{"rate_limit":{"primary_window":{"used_percent":1,"used_percent":2}}}`,
			`{"rate_limit":{"primary_window":{"used_percent":1,"reset_at":"invalid"}}}`,
			`{"rate_limit":{"allowed":true},"additional_rate_limits":[{"metered_feature":"codex","rate_limit":{}}]}`,
		},
		accounts.ProviderClaude: {
			`{"five_hour":{"utilization":-1}}`,
			`{"five_hour":{"utilization":101}}`,
			`{"five_hour":{"utilization":"46"}}`,
			`{"five_hour":{"utilization":42,"resets_at":"invalid"}}`,
			`{"five_hour":{"utilization":42},"unknown":{"duplicate":1,"duplicate":2}}`,
			`{"five_hour":null,"seven_day":null}`,
		},
	} {
		t.Run(provider, func(t *testing.T) {
			store, binding, _ := usageStore(t, provider)
			for _, payload := range append(payloads, `{}`, `null`, `[]`, `{} {}`, strings.Repeat(" ", maxBody+1)) {
				_, err := fetch(context.Background(), store, binding, fakeClient(http.StatusOK, payload, nil))
				var failure *Error
				if !errors.As(err, &failure) || failure.Class != "malformed" {
					t.Fatalf("ambiguous response accepted: %s", payload[:min(len(payload), 80)])
				}
			}
		})
	}
}

func TestUsageFailuresNeverExposeBodiesTokensOrRawTransportErrors(t *testing.T) {
	store, binding, _ := usageStore(t, accounts.ProviderCodex)
	for status, class := range map[int]string{401: "auth-required", 403: "auth-required", 429: "rate-limited", 500: "unavailable", 302: "unavailable"} {
		client := fakeClient(status, "synthetic-provider-secret-body", nil)
		client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"120"}, "Location": []string{"https://example.com/secret"}}, Body: io.NopCloser(strings.NewReader("synthetic-provider-secret-body")), Request: request}, nil
		})
		_, err := fetch(context.Background(), store, binding, client)
		var failure *Error
		if !errors.As(err, &failure) || failure.Class != class || failure.RetryAfter != 2*time.Minute || strings.Contains(err.Error(), "synthetic") || len(err.Error()) > 128 {
			t.Fatalf("unsafe HTTP failure for status %d", status)
		}
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic-private-token-in-error")
	})}
	_, err := fetch(context.Background(), store, binding, client)
	var failure *Error
	if !errors.As(err, &failure) || failure.Class != "network" || strings.Contains(err.Error(), "synthetic") {
		t.Fatal("raw transport error escaped")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = fetch(ctx, store, binding, client)
	if !errors.As(err, &failure) || failure.Class != "cancelled" {
		t.Fatal("cancelled usage fetch proceeded")
	}
	now := time.Now().UTC().Truncate(time.Second)
	if retryAfter("9999999999999", now) != 24*time.Hour || retryAfter(now.Add(3*time.Minute).Format(http.TimeFormat), now) != 3*time.Minute || retryAfter("-1", now) != 0 {
		t.Fatal("retry delay was not safely bounded")
	}
}

func TestUsageRetriesOnlyChangedNativeSnapshotOnce(t *testing.T) {
	for _, mode := range []string{"changed", "unchanged", "identity-changed", "retiring", "twice"} {
		t.Run(mode, func(t *testing.T) {
			store, binding, profile := usageStore(t, accounts.ProviderCodex)
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				status, body := http.StatusUnauthorized, "synthetic-error"
				if calls == 1 && mode != "unchanged" {
					identity := "synthetic-id"
					if mode == "identity-changed" {
						identity = "different-id"
					}
					raw := `{"auth_mode":"chatgpt","tokens":{"account_id":"` + identity + `","access_token":"synthetic-next","refresh_token":"synthetic-refresh"}}`
					staged := filepath.Join(profile, "synthetic.tmp")
					if err := os.WriteFile(staged, []byte(raw), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(staged, filepath.Join(profile, "auth.json")); err != nil {
						t.Fatal(err)
					}
					if mode == "retiring" {
						snapshot, _ := store.Snapshot()
						if _, err := store.SetLifecycle(snapshot.Revision, binding.AccountID, accounts.LifecycleRetiring); err != nil {
							t.Fatal(err)
						}
					}
				}
				if calls == 2 {
					if request.Header.Get("Authorization") != "Bearer synthetic-next" {
						t.Fatal("retry did not use changed managed bearer")
					}
					if mode == "changed" {
						status, body = http.StatusOK, `{"rate_limit":{"primary_window":{"used_percent":42}}}`
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}, nil
			})}
			_, err := fetch(context.Background(), store, binding, client)
			if mode == "changed" {
				if err != nil || calls != 2 {
					t.Fatalf("native bearer race did not recover: calls=%d error=%v", calls, err)
				}
			} else {
				wantCalls := 1
				if mode == "twice" {
					wantCalls = 2
				}
				if calls != wantCalls || err == nil {
					t.Fatalf("unbounded/unsafe native bearer retry: calls=%d error=%v", calls, err)
				}
			}
		})
	}
}
