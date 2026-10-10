package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const renewCredentials = `{"claudeAiOauth":{"accessToken":"synthetic-old-access","refreshToken":"synthetic-old-refresh","expiresAt":1700000000000,"scopes":["user:inference","user:profile"],"subscriptionType":"max","rateLimitTier":"synthetic-tier","clientId":"synthetic-client-id"},"mcpOAuth":{"synthetic-server":{"kept":true}}}`

var renewSecrets = []string{"synthetic-old-access", "synthetic-old-refresh", "synthetic-new-access", "synthetic-new-refresh", "synthetic-sibling-access", "synthetic-sibling-refresh"}

type renewFixture struct {
	s        *Store
	c        Candidate
	b        Binding
	rejected UsageCredential
}

func (f renewFixture) credentials() string {
	return filepath.Join(f.c.ProfilePath, ".credentials.json")
}
func (f renewFixture) profileLock() string {
	return filepath.Join(f.c.ProfilePath, ".oauth_refresh.lock")
}
func (f renewFixture) legacyLock() string { return f.c.ProfilePath + ".lock" }

func (f renewFixture) read(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(f.credentials())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f renewFixture) write(t *testing.T, raw string) {
	t.Helper()
	staged := f.credentials() + ".synthetic-stage"
	if err := os.WriteFile(staged, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, f.credentials()); err != nil {
		t.Fatal(err)
	}
}

func (f renewFixture) noLocks(t *testing.T) {
	t.Helper()
	for _, path := range []string{f.profileLock(), f.legacyLock()} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lock directory left behind: %s", filepath.Base(path))
		}
	}
}

func admitRenewClaude(t *testing.T, credentials string) renewFixture {
	t.Helper()
	s, _ := testStore(t)
	c, err := s.CreateCandidate(ProviderClaude, KindNative)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		".credentials.json": credentials,
		".claude.json":      `{"oauthAccount":{"accountUuid":"synthetic-user","organizationUuid":"synthetic-org"}}`,
	} {
		if err := os.WriteFile(filepath.Join(c.ProfilePath, name), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if c, err = s.VerifyCandidate(c); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Snapshot()
	_, a, err := s.Admit(r.Revision, c, "renew")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CurrentBinding(a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := s.ReadUsageCredential(b)
	if err != nil {
		t.Fatal(err)
	}
	return renewFixture{s: s, c: c, b: b, rejected: rejected}
}

func renewedTokens() ClaudeTokens {
	return ClaudeTokens{AccessToken: "synthetic-new-access", RefreshToken: "synthetic-new-refresh", ExpiresIn: 3600, RefreshTokenExpiresIn: 7200, Scopes: []string{"user:inference", "user:sessions"}, AccountUUID: "synthetic-user", OrganizationUUID: "synthetic-org"}
}

func noSecrets(t *testing.T, label, text string) {
	t.Helper()
	for _, secret := range renewSecrets {
		if strings.Contains(text, secret) {
			t.Fatalf("%s exposed token bytes", label)
		}
	}
}

func TestRenewClaudeFreshLockIsBusyWithoutExchange(t *testing.T) {
	for _, lock := range []string{"profile", "legacy"} {
		t.Run(lock, func(t *testing.T) {
			f := admitRenewClaude(t, renewCredentials)
			path := f.profileLock()
			if lock == "legacy" {
				path = f.legacyLock()
			}
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			before := f.read(t)
			called := false
			outcome, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
				called = true
				return renewedTokens(), nil
			})
			if err != nil || outcome != RenewBusy || called {
				t.Fatalf("fresh %s lock: outcome=%v err=%v exchanged=%v", lock, outcome, err, called)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("another holder's fresh lock was removed")
			}
			other := f.legacyLock()
			if lock == "legacy" {
				other = f.profileLock()
			}
			if _, err := os.Lstat(other); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("first lock not released after second lock was busy")
			}
			if !bytes.Equal(before, f.read(t)) {
				t.Fatal("credentials written while busy")
			}
		})
	}
}

func TestRenewClaudeStaleLockTakenOverAndReleased(t *testing.T) {
	for _, lock := range []string{"profile", "legacy", "both"} {
		t.Run(lock, func(t *testing.T) {
			f := admitRenewClaude(t, renewCredentials)
			var paths []string
			switch lock {
			case "profile":
				paths = []string{f.profileLock()}
			case "legacy":
				paths = []string{f.legacyLock()}
			default:
				paths = []string{f.profileLock(), f.legacyLock()}
			}
			old := time.Now().Add(-2 * time.Minute)
			for _, path := range paths {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}
			heldDuringExchange := false
			outcome, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
				_, first := os.Stat(f.profileLock())
				_, second := os.Stat(f.legacyLock())
				heldDuringExchange = first == nil && second == nil
				return renewedTokens(), nil
			})
			if err != nil || outcome != RenewRefreshed {
				t.Fatalf("stale %s lock not taken over: outcome=%v err=%v", lock, outcome, err)
			}
			if !heldDuringExchange {
				t.Fatal("both locks were not held during the exchange")
			}
			f.noLocks(t)
		})
	}
}

func TestRenewClaudeAdoptsChangedAccessTokenWithoutExchange(t *testing.T) {
	f := admitRenewClaude(t, renewCredentials)
	sibling := strings.Replace(renewCredentials, "synthetic-old-access", "synthetic-sibling-access", 1)
	f.write(t, sibling)
	called := false
	outcome, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
		called = true
		return renewedTokens(), nil
	})
	if err != nil || outcome != RenewAdopted || called {
		t.Fatalf("changed access token not adopted: outcome=%v err=%v exchanged=%v", outcome, err, called)
	}
	if string(f.read(t)) != sibling {
		t.Fatal("adopted credentials were rewritten")
	}
	f.noLocks(t)
}

func TestRenewClaudeRefreshedWritePreservesKeysAndRoundTrips(t *testing.T) {
	f := admitRenewClaude(t, renewCredentials)
	var gotRT, gotClient string
	var gotScopes []string
	start := time.Now()
	outcome, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(_ context.Context, rt, client string, scopes []string) (ClaudeTokens, error) {
		gotRT, gotClient, gotScopes = rt, client, scopes
		return renewedTokens(), nil
	})
	end := time.Now()
	if err != nil || outcome != RenewRefreshed {
		t.Fatalf("renewal failed: outcome=%v err=%v", outcome, err)
	}
	if gotRT != "synthetic-old-refresh" || gotClient != "synthetic-client-id" || strings.Join(gotScopes, " ") != "user:inference user:profile" {
		t.Fatal("exchange did not receive the stored refresh token, client id and scopes")
	}
	info, err := os.Lstat(f.credentials())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode is not 0600: %v", info.Mode().Perm())
	}
	raw := f.read(t)
	if validateClaudeCredentials(raw) != nil {
		t.Fatal("written credentials fail validation")
	}
	if _, err := readPrivate(f.s.root, filepath.Join("profiles", f.c.ProfileGeneration, ".credentials.json"), maxCredentialBytes); err != nil {
		t.Fatal("written credentials are not private")
	}
	var file struct {
		OAuth struct {
			AccessToken           string   `json:"accessToken"`
			RefreshToken          string   `json:"refreshToken"`
			ExpiresAt             int64    `json:"expiresAt"`
			RefreshTokenExpiresAt int64    `json:"refreshTokenExpiresAt"`
			Scopes                []string `json:"scopes"`
			SubscriptionType      string   `json:"subscriptionType"`
			RateLimitTier         string   `json:"rateLimitTier"`
			ClientID              string   `json:"clientId"`
		} `json:"claudeAiOauth"`
		MCP map[string]map[string]bool `json:"mcpOAuth"`
	}
	if json.Unmarshal(raw, &file) != nil {
		t.Fatal("written credentials are not JSON")
	}
	o := file.OAuth
	if o.AccessToken != "synthetic-new-access" || o.RefreshToken != "synthetic-new-refresh" {
		t.Fatal("new tokens not written")
	}
	if o.ExpiresAt < start.Add(time.Hour).UnixMilli() || o.ExpiresAt > end.Add(time.Hour).UnixMilli() {
		t.Fatalf("expiresAt is not now+expires_in in ms: %d", o.ExpiresAt)
	}
	if o.RefreshTokenExpiresAt < start.Add(2*time.Hour).UnixMilli() || o.RefreshTokenExpiresAt > end.Add(2*time.Hour).UnixMilli() {
		t.Fatalf("refreshTokenExpiresAt is not now+refresh_token_expires_in in ms: %d", o.RefreshTokenExpiresAt)
	}
	if strings.Join(o.Scopes, " ") != "user:inference user:sessions" {
		t.Fatal("returned scopes not written")
	}
	if o.SubscriptionType != "max" || o.RateLimitTier != "synthetic-tier" || o.ClientID != "synthetic-client-id" || !file.MCP["synthetic-server"]["kept"] {
		t.Fatal("unrelated credential keys not preserved")
	}
	next, err := f.s.ReadUsageCredential(f.b)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil)
	if err := next.Authorize(req); err != nil || req.Header.Get("Authorization") != "Bearer synthetic-new-access" {
		t.Fatal("renewed credential does not round trip through ReadUsageCredential")
	}
	f.noLocks(t)
}

func TestRenewClaudeKeepsOldRefreshTokenAndScopesWhenOmitted(t *testing.T) {
	credentials := strings.Replace(renewCredentials, `,"clientId":"synthetic-client-id"`, "", 1)
	f := admitRenewClaude(t, credentials)
	var gotClient string
	outcome, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(_ context.Context, _, client string, _ []string) (ClaudeTokens, error) {
		gotClient = client
		return ClaudeTokens{AccessToken: "synthetic-new-access", ExpiresIn: 3600}, nil
	})
	if err != nil || outcome != RenewRefreshed {
		t.Fatalf("renewal failed: outcome=%v err=%v", outcome, err)
	}
	if gotClient != "9d1c250a-e61b-44d9-88ed-5944d1962f5e" {
		t.Fatal("default client id not used when none is stored")
	}
	var file struct {
		OAuth map[string]any `json:"claudeAiOauth"`
	}
	if json.Unmarshal(f.read(t), &file) != nil {
		t.Fatal("written credentials are not JSON")
	}
	scopes, _ := json.Marshal(file.OAuth["scopes"])
	if file.OAuth["accessToken"] != "synthetic-new-access" || file.OAuth["refreshToken"] != "synthetic-old-refresh" || string(scopes) != `["user:inference","user:profile"]` {
		t.Fatal("omitted refresh token or scopes were not kept")
	}
	if _, ok := file.OAuth["refreshTokenExpiresAt"]; ok {
		t.Fatal("refreshTokenExpiresAt written without a response value")
	}
	if _, ok := file.OAuth["clientId"]; ok {
		t.Fatal("default client id persisted")
	}
}

func TestRenewClaudeSiblingRotationDuringExchangeIsAdopted(t *testing.T) {
	f := admitRenewClaude(t, renewCredentials)
	sibling := strings.NewReplacer("synthetic-old-access", "synthetic-sibling-access", "synthetic-old-refresh", "synthetic-sibling-refresh").Replace(renewCredentials)
	outcome, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
		f.write(t, sibling)
		return renewedTokens(), nil
	})
	if err != nil || outcome != RenewAdopted {
		t.Fatalf("CAS did not refuse a sibling rotation: outcome=%v err=%v", outcome, err)
	}
	if string(f.read(t)) != sibling {
		t.Fatal("sibling credentials were overwritten")
	}
	f.noLocks(t)
}

func TestRenewClaudeRejectedRefreshToken(t *testing.T) {
	for _, rotated := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged-is-dead", true: "rotated-is-adopted"}[rotated], func(t *testing.T) {
			f := admitRenewClaude(t, renewCredentials)
			before := f.read(t)
			sibling := strings.NewReplacer("synthetic-old-access", "synthetic-sibling-access", "synthetic-old-refresh", "synthetic-sibling-refresh").Replace(renewCredentials)
			outcome, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
				if rotated {
					f.write(t, sibling)
				}
				return ClaudeTokens{}, fmt.Errorf("synthetic invalid_grant: %w", ErrRefreshRejected)
			})
			want, wantBytes := RenewDead, before
			if rotated {
				want, wantBytes = RenewAdopted, []byte(sibling)
			}
			if err != nil || outcome != want {
				t.Fatalf("rejected refresh: outcome=%v err=%v", outcome, err)
			}
			if !bytes.Equal(wantBytes, f.read(t)) {
				t.Fatal("credentials modified after a rejected refresh")
			}
			f.noLocks(t)
		})
	}
}

func TestRenewClaudeExchangeFailuresWriteNothing(t *testing.T) {
	cases := map[string]ClaudeTokens{
		"empty-access":     {RefreshToken: "synthetic-new-refresh", ExpiresIn: 3600},
		"zero-expiry":      {AccessToken: "synthetic-new-access", ExpiresIn: 0},
		"negative-expiry":  {AccessToken: "synthetic-new-access", ExpiresIn: -1},
		"over-thirty-days": {AccessToken: "synthetic-new-access", ExpiresIn: 30*24*3600 + 1},
		"network":          {},
	}
	for name, tokens := range cases {
		t.Run(name, func(t *testing.T) {
			f := admitRenewClaude(t, renewCredentials)
			before := f.read(t)
			network := errors.New("synthetic network failure")
			outcome, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
				if name == "network" {
					return ClaudeTokens{}, network
				}
				return tokens, nil
			})
			if err == nil || outcome == RenewRefreshed {
				t.Fatalf("invalid exchange accepted: outcome=%v", outcome)
			}
			if name == "network" && !errors.Is(err, network) {
				t.Fatal("exchange error not returned")
			}
			noSecrets(t, "error", fmt.Sprintf("%v %#v", err, err))
			if !bytes.Equal(before, f.read(t)) {
				t.Fatal("credentials written after a failed exchange")
			}
			f.noLocks(t)
		})
	}
}

func TestRenewClaudeIdentityMismatchWritesNothing(t *testing.T) {
	for name, tokens := range map[string]ClaudeTokens{
		"account":      {AccessToken: "synthetic-new-access", RefreshToken: "synthetic-new-refresh", ExpiresIn: 3600, AccountUUID: "synthetic-other-user", OrganizationUUID: "synthetic-org"},
		"organization": {AccessToken: "synthetic-new-access", RefreshToken: "synthetic-new-refresh", ExpiresIn: 3600, AccountUUID: "synthetic-user", OrganizationUUID: "synthetic-other-org"},
	} {
		t.Run(name, func(t *testing.T) {
			f := admitRenewClaude(t, renewCredentials)
			before := f.read(t)
			_, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
				return tokens, nil
			})
			if !errors.Is(err, ErrIdentityChanged) {
				t.Fatalf("identity mismatch accepted: %v", err)
			}
			if !bytes.Equal(before, f.read(t)) {
				t.Fatal("credentials written for another identity")
			}
			f.noLocks(t)
		})
	}
	t.Run("partial-identity-is-not-checked", func(t *testing.T) {
		f := admitRenewClaude(t, renewCredentials)
		outcome, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
			return ClaudeTokens{AccessToken: "synthetic-new-access", ExpiresIn: 3600, AccountUUID: "synthetic-other-user"}, nil
		})
		if err != nil || outcome != RenewRefreshed {
			t.Fatalf("partial response identity refused: outcome=%v err=%v", outcome, err)
		}
	})
}

func TestRenewClaudeCancelledDuringExchangeWritesNothing(t *testing.T) {
	f := admitRenewClaude(t, renewCredentials)
	before := f.read(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome, err := f.s.RenewClaudeCredential(ctx, f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
		cancel()
		return renewedTokens(), nil
	})
	if !errors.Is(err, context.Canceled) || outcome == RenewRefreshed {
		t.Fatalf("cancelled renewal: outcome=%v err=%v", outcome, err)
	}
	if !bytes.Equal(before, f.read(t)) {
		t.Fatal("credentials written after cancellation")
	}
	f.noLocks(t)
}

func TestRenewClaudeDoesNotHoldStoreMutexAcrossExchange(t *testing.T) {
	f := admitRenewClaude(t, renewCredentials)
	outcome, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
		done := make(chan struct{})
		go func() {
			_, _ = f.s.Snapshot()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("store mutex held across the network exchange")
		}
		return renewedTokens(), nil
	})
	if err != nil || outcome != RenewRefreshed {
		t.Fatalf("renewal failed: outcome=%v err=%v", outcome, err)
	}
}

func TestRenewClaudeIneligibleBindingNeverExchanges(t *testing.T) {
	for _, mode := range []string{"retiring", "codex"} {
		t.Run(mode, func(t *testing.T) {
			f := admitRenewClaude(t, renewCredentials)
			b := f.b
			rejected := f.rejected
			if mode == "retiring" {
				r, _ := f.s.Snapshot()
				if _, err := f.s.SetLifecycle(r.Revision, b.AccountID, LifecycleRetiring); err != nil {
					t.Fatal(err)
				}
			} else {
				_, a := admitNative(t, f.s, "renew-codex")
				var err error
				if b, err = f.s.CurrentBinding(a.ID, 1); err != nil {
					t.Fatal(err)
				}
				if rejected, err = f.s.ReadUsageCredential(b); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			outcome, err := f.s.RenewClaudeCredential(context.Background(), b, rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
				called = true
				return renewedTokens(), nil
			})
			if err == nil || called || outcome == RenewRefreshed {
				t.Fatalf("ineligible %s binding renewed: outcome=%v err=%v exchanged=%v", mode, outcome, err, called)
			}
		})
	}
}

func TestClaudeTokensFormattingIsRedacted(t *testing.T) {
	tokens := renewedTokens()
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		noSecrets(t, "ClaudeTokens "+format, fmt.Sprintf(format, tokens))
		noSecrets(t, "*ClaudeTokens "+format, fmt.Sprintf(format, &tokens))
	}
	noSecrets(t, "ErrRefreshRejected", fmt.Sprintf("%v %#v", ErrRefreshRejected, ErrRefreshRejected))
}

func TestRenewClaudeReadOnlyStoreNeverExchanges(t *testing.T) {
	f := admitRenewClaude(t, renewCredentials)
	reader, err := OpenReadOnly(filepath.Dir(f.s.path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	before := f.read(t)
	_, err = reader.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
		t.Fatal("read-only store exchanged a refresh token")
		return ClaudeTokens{}, nil
	})
	if !errors.Is(err, ErrIneligible) || !bytes.Equal(before, f.read(t)) {
		t.Fatalf("read-only store renewed: %v", err)
	}
	f.noLocks(t)
}

// A token the usage reader would refuse must never be written: it would brick the profile.
func TestRenewClaudeRejectsHeaderUnsafeTokens(t *testing.T) {
	for name, mutate := range map[string]func(*ClaudeTokens){
		"access":  func(c *ClaudeTokens) { c.AccessToken = "synthetic new-access" },
		"refresh": func(c *ClaudeTokens) { c.RefreshToken = "synthetic-new-refresh\n" },
	} {
		t.Run(name, func(t *testing.T) {
			f := admitRenewClaude(t, renewCredentials)
			before := f.read(t)
			_, err := f.s.RenewClaudeCredential(context.Background(), f.b, f.rejected, func(context.Context, string, string, []string) (ClaudeTokens, error) {
				tokens := renewedTokens()
				mutate(&tokens)
				return tokens, nil
			})
			if err == nil || !bytes.Equal(before, f.read(t)) {
				t.Fatalf("header-unsafe token written: %v", err)
			}
			f.noLocks(t)
		})
	}
}
