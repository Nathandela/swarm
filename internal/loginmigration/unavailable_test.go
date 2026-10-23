package loginmigration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
)

func unavailableFixture(t *testing.T, provider string) (fixture, string) {
	t.Helper()
	f := newFixture(t)
	if err := os.RemoveAll(filepath.Join(f.state, f.id)); err != nil {
		t.Fatal(err)
	}
	var registry oldRegistry
	if err := readJSON(filepath.Join(f.state, "accounts", "registry.json"), &registry); err != nil {
		t.Fatal(err)
	}
	var known string
	var err error
	if provider == "claude" {
		known, err = logins.ClaudeIdentity([]byte(`{"oauthAccount":{"accountUuid":"saved-account","organizationUuid":"saved-org"}}`))
		put(t, filepath.Join(f.profile, ".claude.json"), `{"oauthAccount":{"accountUuid":"foreign-cache","organizationUuid":"other-org"},"userID":"foreign-cache","mcpServers":{"keep":"untouched"}}`)
		put(t, filepath.Join(f.profile, ".credentials.json"), `{"claudeAiOauth":{},"mcpOAuth":{"keep":"untouched"}}`)
	} else {
		known, err = logins.CodexIdentity([]byte(`{"tokens":{"account_id":"saved-account"}}`))
		put(t, filepath.Join(f.profile, "auth.json"), `{"auth_mode":"chatgpt","tokens":{"account_id":"foreign-cache"},"keep":"untouched"}`)
	}
	if err != nil {
		t.Fatal(err)
	}
	account := registry.Accounts[f.account]
	account.Provider = provider
	account.Auth = "invalid"
	generation := account.Generations[account.Current]
	generation.Identity = known
	account.Generations[account.Current] = generation
	registry.Accounts[f.account] = account
	put(t, filepath.Join(f.state, "accounts", "registry.json"), registry)
	return f, known
}

func TestClearedGrantRetainsRecordedIdentityWithoutImportingCachedAuthority(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, state := range []string{"cleared", "missing", "missing-cached-identity", "legacy-valid"} {
			t.Run(provider+"/"+state, func(t *testing.T) {
				f, known := unavailableFixture(t, provider)
				authPath := filepath.Join(f.profile, "auth.json")
				if provider == "claude" {
					authPath = filepath.Join(f.profile, ".credentials.json")
				}
				if state == "missing" {
					if err := os.Remove(authPath); err != nil {
						t.Fatal(err)
					}
				}
				if state == "missing-cached-identity" && provider == "claude" {
					if err := os.Remove(filepath.Join(f.profile, ".claude.json")); err != nil {
						t.Fatal(err)
					}
				}
				if state == "legacy-valid" {
					var registry oldRegistry
					if err := readJSON(filepath.Join(f.state, "accounts", "registry.json"), &registry); err != nil {
						t.Fatal(err)
					}
					account := registry.Accounts[f.account]
					account.Auth = "valid"
					registry.Accounts[f.account] = account
					put(t, filepath.Join(f.state, "accounts", "registry.json"), registry)
				}
				before := map[string]string{}
				for _, name := range []string{"auth.json", ".credentials.json", ".claude.json"} {
					raw, err := os.ReadFile(filepath.Join(f.profile, name))
					if err == nil {
						before[name] = string(raw)
					} else if !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
				}
				plan, _, err := inventory(f.state)
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.Imports) != 1 || !plan.Imports[0].Unavailable || plan.Imports[0].Login.Identity != known || plan.Imports[0].Login.AuthState != logins.AuthNeedsLogin || len(plan.Secrets) != 0 {
					t.Fatal("cleared grant retained cached credential authority")
				}
				if err = Run(f.state); err != nil {
					t.Fatal(err)
				}
				store, err := logins.Open(f.state)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = store.Close() }()
				registry, err := store.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				if len(registry.Logins) != 1 || registry.Logins[0].Identity != known || registry.Logins[0].AuthState != logins.AuthNeedsLogin || registry.Logins[0].Renewable {
					t.Fatal("unavailable identity was changed or made usable")
				}
				for name, want := range before {
					raw, err := os.ReadFile(filepath.Join(f.profile, name))
					if err != nil || string(raw) != want {
						t.Fatalf("unavailable source %s changed", name)
					}
				}
				raw, err := os.ReadFile(filepath.Join(f.state, "logins", "saved", f.account, "credential.json"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), "foreign-cache") || strings.Contains(string(raw), "untouched") {
					t.Fatal("cached authority imported into saved credential")
				}
			})
		}
	}
}

func TestUnavailableFallbackRefusesUsableForeignOrMalformedState(t *testing.T) {
	for _, kind := range []string{"claude-usable", "codex-usable", "codex-access-without-refresh", "codex-api-key", "malformed-auth", "null-auth", "wrong-oauth-type", "wrong-access-type", "wrong-refresh-type", "malformed-identity", "wrong-account-type", "wrong-account-id-type", "duplicate-identity-key", "unknown-recorded-id", "malformed-recorded-id", "duplicate-recorded-id"} {
		t.Run(kind, func(t *testing.T) {
			provider := "claude"
			if strings.HasPrefix(kind, "codex-") {
				provider = "codex"
			}
			f, _ := unavailableFixture(t, provider)
			switch kind {
			case "claude-usable":
				put(t, filepath.Join(f.profile, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"synthetic-access"}}`)
			case "codex-usable":
				put(t, filepath.Join(f.profile, "auth.json"), `{"tokens":{"account_id":"foreign-cache","access_token":"synthetic-access","refresh_token":"synthetic-refresh"}}`)
			case "codex-access-without-refresh":
				put(t, filepath.Join(f.profile, "auth.json"), `{"tokens":{"account_id":"foreign-cache","access_token":"synthetic-access"}}`)
			case "codex-api-key":
				put(t, filepath.Join(f.profile, "auth.json"), `{"OPENAI_API_KEY":"synthetic-api-key"}`)
			case "malformed-auth":
				put(t, filepath.Join(f.profile, ".credentials.json"), `{"claudeAiOauth":`)
			case "null-auth":
				put(t, filepath.Join(f.profile, ".credentials.json"), `null`)
			case "wrong-oauth-type":
				put(t, filepath.Join(f.profile, ".credentials.json"), `{"claudeAiOauth":[]}`)
			case "wrong-access-type":
				put(t, filepath.Join(f.profile, ".credentials.json"), `{"claudeAiOauth":{"accessToken":123}}`)
			case "wrong-refresh-type":
				put(t, filepath.Join(f.profile, ".credentials.json"), `{"claudeAiOauth":{"refreshToken":[]}}`)
			case "malformed-identity":
				put(t, filepath.Join(f.profile, ".claude.json"), `{"oauthAccount":`)
			case "wrong-account-type":
				put(t, filepath.Join(f.profile, ".claude.json"), `{"oauthAccount":"foreign-cache"}`)
			case "wrong-account-id-type":
				put(t, filepath.Join(f.profile, ".claude.json"), `{"oauthAccount":{"accountUuid":123}}`)
			case "duplicate-identity-key":
				put(t, filepath.Join(f.profile, ".claude.json"), `{"oauthAccount":{},"oauthAccount":{}}`)
			default:
				var registry oldRegistry
				if err := readJSON(filepath.Join(f.state, "accounts", "registry.json"), &registry); err != nil {
					t.Fatal(err)
				}
				account := registry.Accounts[f.account]
				generation := account.Generations[account.Current]
				switch kind {
				case "unknown-recorded-id":
					generation.Identity = ""
				case "malformed-recorded-id":
					generation.Identity = "unknown"
				}
				account.Generations[account.Current] = generation
				registry.Accounts[f.account] = account
				if kind == "duplicate-recorded-id" {
					duplicate := account
					duplicate.ID = strings.Repeat("d", 32)
					registry.Accounts[duplicate.ID] = duplicate
				}
				put(t, filepath.Join(f.state, "accounts", "registry.json"), registry)
			}
			if err := Preflight(f.state); err == nil {
				t.Fatal("foreign or malformed credential state accepted")
			}
			if _, err := os.Stat(filepath.Join(f.state, "logins")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refusal mutated login destination")
			}
		})
	}
}
