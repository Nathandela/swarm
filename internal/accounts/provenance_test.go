package accounts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexImportedAccountCacheCannotAuthenticateOpaqueCredentials(t *testing.T) {
	s, root := testStore(t)
	for _, id := range []string{"cached-account-a", "cached-account-b"} {
		raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": id, "access_token": "same-synthetic-access", "refresh_token": "same-synthetic-refresh"}})
		path := filepath.Join(root, id+".json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := s.ImportCodex(path)
		if err != nil || c.Source != SourceCachedImport || c.Verification != VerificationUnverified {
			t.Fatalf("Codex cached import established authority: %+v %v", c, err)
		}
		c.Source, c.Verification = SourceNativeLogin, VerificationCodex
		verified, err := s.VerifyCandidate(c)
		if err != nil || verified.Source != SourceCachedImport || verified.Verification != VerificationUnverified {
			t.Fatalf("re-verification promoted Codex cache: %+v %v", verified, err)
		}
		r, _ := s.Snapshot()
		if _, _, err := s.Admit(r.Revision, c, "import"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("cached Codex account was admitted: %v", err)
		}
	}
}

func TestNewGenerationAuthFailureDoesNotInvalidateHealthyRetainedBinding(t *testing.T) {
	s, _ := testStore(t)
	_, a := admitNative(t, s, "retained-account")
	old, _ := s.CurrentBinding(a.ID, 1)
	newCandidate := nativeCandidate(t, s, "retained-account")
	r, _ := s.Snapshot()
	r, _, err := s.Reauthenticate(r.Revision, a.ID, newCandidate)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := s.CurrentBinding(a.ID, 1)
	if _, err := s.SetAuth(r.Revision, a.ID, AuthNeedsLogin); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateBinding(current); !errors.Is(err, ErrIneligible) {
		t.Fatalf("unhealthy current generation accepted: %v", err)
	}
	if _, err := s.ResolveEnvironment(old, []string{"HOME=/normal/home"}); err != nil {
		t.Fatalf("healthy frozen older generation rejected: %v", err)
	}
}

func TestClaudeImportedCacheCannotAuthenticateOpaqueCredentials(t *testing.T) {
	s, root := testStore(t)
	credentials := []byte(`{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","scopes":["user:inference"],"subscriptionType":"pro"}}`)
	credentialPath := filepath.Join(root, "import-credentials.json")
	if err := os.WriteFile(credentialPath, credentials, 0o600); err != nil {
		t.Fatal(err)
	}
	var imported []Candidate
	for _, id := range []string{"historical-cache-a", "historical-cache-b"} {
		identity, _ := json.Marshal(map[string]any{"oauthAccount": map[string]string{"accountUuid": id, "organizationUuid": "synthetic-org"}})
		identityPath := filepath.Join(root, id+".json")
		if err := os.WriteFile(identityPath, identity, 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := s.ImportClaude(credentialPath, identityPath)
		if err != nil || c.Source != SourceCachedImport || c.Verification != VerificationUnverified {
			t.Fatalf("import unexpectedly established authority: %+v %v", c, err)
		}
		imported = append(imported, c)
	}
	if imported[0].Identity == imported[1].Identity {
		t.Fatal("fixture does not contain distinct stale cached identities")
	}
	// Reopening and caller edits cannot replace the durable import provenance.
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	for _, c := range imported {
		c.Source = SourceNativeLogin
		c.Verification = VerificationClaude
		verified, err := reopened.VerifyCandidate(c)
		if err != nil || verified.Source != SourceCachedImport || verified.Verification != VerificationUnverified {
			t.Fatalf("re-verification promoted independent cache: %+v %v", verified, err)
		}
		r, _ := reopened.Snapshot()
		if _, _, err := reopened.Admit(r.Revision, c, "import"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("cached import was admitted: %v", err)
		}
	}
	// A fresh isolated native-login profile remains eligible. Reauthentication
	// cannot use the imported cache even when its UUID matches that account.
	fresh, err := reopened.CreateCandidate(ProviderClaude, KindNative)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := os.ReadFile(filepath.Join(imported[0].ProfilePath, ".claude.json"))
	for name, raw := range map[string][]byte{".credentials.json": credentials, ".claude.json": identity} {
		if err := os.WriteFile(filepath.Join(fresh.ProfilePath, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err = reopened.VerifyCandidate(fresh)
	if err != nil || fresh.Verification != VerificationClaude {
		t.Fatalf("fresh login was rejected: %v", err)
	}
	r, _ := reopened.Snapshot()
	r, account, err := reopened.Admit(r.Revision, fresh, "native")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.Reauthenticate(r.Revision, account.ID, imported[0]); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("reauthentication promoted imported cache: %v", err)
	}
}
