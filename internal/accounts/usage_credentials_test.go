package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageCredentialExactSnapshotAndEndpointCustody(t *testing.T) {
	s, _ := testStore(t)
	candidate, account := admitNative(t, s, "usage-synthetic")
	binding, _ := s.CurrentBinding(account.ID, 1)
	credential, err := s.ReadUsageCredential(binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if output := fmt.Sprintf(format, credential); strings.Contains(output, "synthetic-bearer") || strings.Contains(output, "usage-synthetic") {
			t.Fatal("credential formatting exposed private fields")
		}
	}
	serialized, err := json.Marshal(map[string]any{"credential": credential})
	if err != nil || string(serialized) != `{"credential":{}}` {
		t.Fatal("credential is serializable")
	}
	for _, url := range []string{"https://example.com/backend-api/wham/usage", "http://chatgpt.com/backend-api/wham/usage", "https://chatgpt.com/backend-api/wham/usage?override=yes", "https://chatgpt.com:443/backend-api/wham/usage", "https://user@chatgpt.com/backend-api/wham/usage", "https://chatgpt.com/backend-api/wham/usage#fragment", "https://chatgpt.com/backend-api/models"} {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if credential.Authorize(req) == nil || req.Header.Get("Authorization") != "" {
			t.Fatalf("unsafe credential destination accepted: %s", url)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", nil)
	if err := credential.Authorize(req); err != nil || req.Header.Get("Authorization") != "Bearer synthetic-bearer-usage-synthetic" || !credential.MatchesAccountID("usage-synthetic") {
		t.Fatal("exact managed snapshot was not authorized")
	}
	// A native atomic refresh leaves the admitted immutable identity intact.
	raw := []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"usage-synthetic","access_token":"synthetic-next","refresh_token":"synthetic-refresh"}}`)
	staged := filepath.Join(candidate.ProfilePath, "refresh.tmp")
	if err := os.WriteFile(staged, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, filepath.Join(candidate.ProfilePath, "auth.json")); err != nil {
		t.Fatal(err)
	}
	next, err := s.ReadUsageCredential(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Authorize(req); err != nil || req.Header.Get("Authorization") != "Bearer synthetic-next" {
		t.Fatal("current native refresh was not read")
	}
	// Old copied credential remains its exact snapshot; no read or refresh occurs.
	if err := credential.Authorize(req); err != nil || req.Header.Get("Authorization") != "Bearer synthetic-bearer-usage-synthetic" {
		t.Fatal("credential snapshot changed after native refresh")
	}
}

func TestUsageCredentialGenerationLifecycleAndUnsafeFiles(t *testing.T) {
	for _, mode := range []string{"paused", "retiring", "old-generation", "identity", "symlink", "hardlink", "public", "duplicate", "header"} {
		t.Run(mode, func(t *testing.T) {
			s, root := testStore(t)
			candidate, account := admitNative(t, s, "usage-lifecycle")
			binding, _ := s.CurrentBinding(account.ID, 1)
			revision, _ := s.Snapshot()
			path := filepath.Join(candidate.ProfilePath, "auth.json")
			switch mode {
			case "paused", "retiring":
				if _, err := s.SetLifecycle(revision.Revision, account.ID, mode); err != nil {
					t.Fatal(err)
				}
			case "old-generation":
				if _, _, err := s.Reauthenticate(revision.Revision, account.ID, nativeCandidate(t, s, "usage-lifecycle")); err != nil {
					t.Fatal(err)
				}
			case "identity", "duplicate", "header":
				identity, token := "usage-lifecycle", "synthetic-access"
				if mode == "identity" {
					identity = "different-account"
				}
				if mode == "header" {
					token = "synthetic\r\ninjection"
				}
				raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": identity, "access_token": token, "refresh_token": "synthetic-refresh"}})
				if mode == "duplicate" {
					raw = []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"usage-lifecycle","access_token":"first","access_token":"second","refresh_token":"synthetic"}}`)
				}
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(root, "alias")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".original", path); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.ReadUsageCredential(binding)
			if mode == "paused" {
				if err != nil {
					t.Fatalf("paused read-only account rejected: %v", err)
				}
			} else if err == nil {
				t.Fatalf("unsafe usage snapshot accepted: %s", mode)
			}
		})
	}
}

func TestClaudeUsageCredentialUsesAdmittedNativeProfile(t *testing.T) {
	s, _ := testStore(t)
	candidate, err := s.CreateCandidate(ProviderClaude, KindNative)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-claude-access","refreshToken":"synthetic-refresh","scopes":["user:inference"],"subscriptionType":"pro"}}`,
		".claude.json":      `{"oauthAccount":{"accountUuid":"synthetic-user","organizationUuid":"synthetic-org"}}`,
	} {
		if err := os.WriteFile(filepath.Join(candidate.ProfilePath, name), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err = s.VerifyCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	revision, _ := s.Snapshot()
	_, account, err := s.Admit(revision.Revision, candidate, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := s.CurrentBinding(account.ID, 1)
	credential, err := s.ReadUsageCredential(binding)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil)
	if err := credential.Authorize(req); err != nil || req.Header.Get("Authorization") != "Bearer synthetic-claude-access" || req.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
		t.Fatal("Claude exact native credential missing")
	}
	if err := os.WriteFile(filepath.Join(candidate.ProfilePath, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"changed-user","organizationUuid":"synthetic-org"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadUsageCredential(binding); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("changed Claude cached identity accepted: %v", err)
	}
}
