package accounts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func retiringNative(t *testing.T, provider string) (*Store, Candidate, Account, Binding, Registry) {
	t.Helper()
	s, _ := testStore(t)
	var c Candidate
	var a Account
	if provider == ProviderCodex {
		c, a = admitNative(t, s, "retirement-synthetic")
	} else {
		var err error
		c, err = s.CreateCandidate(ProviderClaude, KindNative)
		if err != nil {
			t.Fatal(err)
		}
		for name, raw := range map[string]string{
			".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","scopes":["user:inference"],"subscriptionType":"pro"}}`,
			".claude.json":      `{"oauthAccount":{"accountUuid":"synthetic-account","organizationUuid":"synthetic-org"},"primaryApiKey":"synthetic-config-secret","customApiKeyResponses":{"approved":["synthetic-key-suffix"],"rejected":[]},"projects":{"/synthetic":{"hasTrustDialogAccepted":true,"allowedTools":["Read"]}},"numStartups":7,"lastTotalInputTokens":99,"lastModelUsage":{"synthetic-model":{"inputTokens":7,"cacheReadInputTokens":2}}}`,
		} {
			if err := os.WriteFile(filepath.Join(c.ProfilePath, name), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		c, err = s.VerifyCandidate(c)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		_, a, err = s.Admit(r.Revision, c, "retire")
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.CurrentBinding(a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.SetLifecycle(r.Revision, a.ID, LifecycleRetiring)
	if err != nil {
		t.Fatal(err)
	}
	return s, c, a, b, r
}

func TestCredentialErasureFenceOrdersSecretResolution(t *testing.T) {
	for _, resolveFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "resolve-before-fence", false: "fence-before-resolve"}[resolveFirst], func(t *testing.T) {
			s, _, a, b, r := retiringNative(t, ProviderCodex)
			other, err := OpenReadOnly(filepath.Dir(s.path))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = other.Close() }()
			ready, proceed := make(chan struct{}), make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			var resolveErr error
			go func() {
				defer wg.Done()
				if !resolveFirst {
					<-proceed
				}
				_, resolveErr = other.ResolveEnvironment(b, nil)
				close(ready)
			}()
			if resolveFirst {
				<-ready
			}
			r, err = s.BeginCredentialErasure(r.Revision, a.ID, 1, "0.160.0")
			if err != nil {
				t.Fatal(err)
			}
			close(proceed)
			wg.Wait()
			if resolveFirst && resolveErr != nil {
				t.Fatal(resolveErr)
			}
			if !resolveFirst && !errors.Is(resolveErr, ErrIneligible) {
				t.Fatalf("post-fence credentials resolved: %v", resolveErr)
			}
			if _, err := other.ResolveEnvironment(b, nil); !errors.Is(err, ErrIneligible) {
				t.Fatal("embargo not visible to existing read handle")
			}
			if _, err := other.HistoryProfilePath(b); err != nil {
				t.Fatal(err)
			}
			if !r.Accounts[a.ID].Generations[1].CredentialErasing {
				t.Fatal("missing durable fence")
			}
		})
	}
}

func TestClaudeRetirementErasesStagingAndCopiesPreservesTrust(t *testing.T) {
	s, c, a, b, r := retiringNative(t, ProviderClaude)
	for _, dir := range []string{"backups", "projects"} {
		if err := os.Mkdir(filepath.Join(c.ProfilePath, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(c.ProfilePath, "backups"), 0o775); err != nil {
		t.Fatal(err)
	}
	remove := []string{".credentials.json.tmp.abcdef12", ".claude.json.tmp.87654321", ".claude.json.tmp.12345.012345abcdef", "backups/.claude.json.backup.1791058214651", "backups/.claude.json.corrupted.1791058214652", ".claude.json.corrupted.1791058214653"}
	for _, name := range remove {
		if err := os.WriteFile(filepath.Join(c.ProfilePath, name), []byte("synthetic secret or malformed backup"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	history := filepath.Join(c.ProfilePath, "projects", "retained.jsonl")
	if err := os.WriteFile(history, []byte("synthetic retained conversation"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := s.BeginCredentialErasure(r.Revision, a.ID, 1, "2.1.288")
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range append(remove, ".credentials.json") {
		if _, err := os.Lstat(filepath.Join(c.ProfilePath, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cache remains: %s", name)
		}
	}
	raw, err := os.ReadFile(filepath.Join(c.ProfilePath, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if json.Unmarshal(raw, &config) != nil {
		t.Fatal("invalid preserved config")
	}
	if _, ok := config["customApiKeyResponses"]; ok {
		t.Fatal("credential suffix approvals remain")
	}
	if _, ok := config["primaryApiKey"]; ok {
		t.Fatal("config credential remains")
	}
	if config["numStartups"] != float64(7) || config["projects"] == nil || config["oauthAccount"] == nil {
		t.Fatal("trust or identity metadata lost")
	}
	if raw, err := os.ReadFile(history); err != nil || string(raw) != "synthetic retained conversation" {
		t.Fatal("history lost")
	}
	if _, err := s.HistoryProfilePath(b); err != nil || !r.Accounts[a.ID].Generations[1].CredentialErased {
		t.Fatal("erased history no longer resolvable")
	}
}

func TestNativeErasureUnknownInventoryRetainsEveryCredential(t *testing.T) {
	cases := []struct{ provider, name, raw string }{
		{ProviderCodex, ".credentials.json", `{"synthetic-mcp":"secret"}`},
		{ProviderCodex, "config.toml", `cli_auth_credentials_store = "keyring"`},
		{ProviderCodex, "config.toml", "[mcp_servers.synthetic]\ncommand = \"synthetic\""},
		{ProviderClaude, ".credentials.json.tmp.nothex00", "synthetic-staging"},
		{ProviderClaude, ".claude.json", `{"oauthAccount":{},"primaryApiKey":"synthetic","futureSecretStore":"unknown"}`},
		{ProviderClaude, ".claude.json", `{"primaryApiKey":"synthetic","projects":{},"projects":{"changed":true}}`},
		{ProviderClaude, "backups/unknown-cache", "synthetic"},
	}
	for _, test := range cases {
		t.Run(test.provider+"/"+test.name+"/"+test.raw, func(t *testing.T) {
			s, c, a, _, r := retiringNative(t, test.provider)
			if filepath.Dir(test.name) != "." {
				if err := os.Mkdir(filepath.Join(c.ProfilePath, filepath.Dir(test.name)), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(c.ProfilePath, test.name), []byte(test.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			version, credential := "0.160.0", "auth.json"
			if test.provider == ProviderClaude {
				version, credential = "2.1.288", ".credentials.json"
			}
			r, err := s.BeginCredentialErasure(r.Revision, a.ID, 1, version)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true}); err == nil {
				t.Fatal("unknown inventory erased")
			}
			if _, err := os.Stat(filepath.Join(c.ProfilePath, credential)); err != nil {
				t.Fatal("deleted before complete inventory validation")
			}
		})
	}
}

func TestCredentialErasureDurabilityFailureKeepsEmbargoAndRetries(t *testing.T) {
	s, c, a, b, r := retiringNative(t, ProviderClaude)
	r, err := s.BeginCredentialErasure(r.Revision, a.ID, 1, "2.1.288")
	if err != nil {
		t.Fatal(err)
	}
	s.writeOps.syncDir = func(string) error { return errors.New("synthetic sync failure") }
	if _, err = s.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true}); err == nil {
		t.Fatal("failed config sync acknowledged")
	}
	if _, err = s.ResolveEnvironment(b, nil); !errors.Is(err, ErrIneligible) {
		t.Fatal("uncertain sanitation reopened writers")
	}
	if _, err = os.Stat(filepath.Join(c.ProfilePath, ".credentials.json")); err != nil {
		t.Fatal("deleted credential before config durability")
	}
	s.writeOps = writeOps{}
	r, err = s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true})
	if err != nil || !r.Accounts[a.ID].Generations[1].CredentialErased {
		t.Fatalf("safe retry failed: %v", err)
	}
}

func TestCredentialErasureRejectsUnconfirmedFenceBeforeAnyDeletion(t *testing.T) {
	s, c, a, b, r := retiringNative(t, ProviderCodex)
	s.writeOps.syncDir = func(string) error { return errors.New("synthetic fence parent sync failure") }
	r, err := s.BeginCredentialErasure(r.Revision, a.ID, 1, "0.160.0")
	if !errors.Is(err, ErrDurabilityUncertain) || !r.Accounts[a.ID].Generations[1].CredentialErasing {
		t.Fatal("fixture did not reach visible uncertain fence")
	}
	if _, err := s.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true}); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("unknown fence durability authorized deletion: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.ProfilePath, "auth.json")); err != nil {
		t.Fatal("deleted before fence was durable")
	}
	if _, err := s.ResolveEnvironment(b, nil); !errors.Is(err, ErrIneligible) {
		t.Fatal("uncertain fence allowed credential resolution")
	}
	s.writeOps = writeOps{}
	r, err = s.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialErasureReopenedHandleConfirmsFenceBeforeDeletion(t *testing.T) {
	s, c, a, _, r := retiringNative(t, ProviderClaude)
	r, err := s.BeginCredentialErasure(r.Revision, a.ID, 1, "2.1.288")
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.writeOps.syncDir = func(string) error { return errors.New("synthetic persistent confirmation failure") }
	for i := 0; i < 2; i++ {
		if _, err := other.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true}); !errors.Is(err, ErrDurabilityUncertain) {
			t.Fatalf("reopened handle inferred durable embargo: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(c.ProfilePath, ".claude.json"))
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if json.Unmarshal(raw, &config) != nil || config["primaryApiKey"] == nil {
			t.Fatal("sanitized before fence confirmation")
		}
		if _, err := os.Stat(filepath.Join(c.ProfilePath, ".credentials.json")); err != nil {
			t.Fatal("deleted before fence confirmation")
		}
	}
	other.writeOps = writeOps{}
	if _, err := other.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeErasureRemovesEmptyLeftoverRefreshLock(t *testing.T) {
	s, c, a, _, r := retiringNative(t, ProviderClaude)
	// Native proper-lockfile creates the directory with the default 0777&umask mode.
	lock := filepath.Join(c.ProfilePath, ".oauth_refresh.lock")
	if err := os.Mkdir(lock, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := s.BeginCredentialErasure(r.Revision, a.ID, 1, "2.1.288")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true}); err != nil {
		t.Fatalf("leftover refresh lock refused erasure: %v", err)
	}
	for _, name := range []string{".oauth_refresh.lock", ".credentials.json"} {
		if _, err := os.Lstat(filepath.Join(c.ProfilePath, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("not removed: %s", name)
		}
	}
}

func TestClaudeErasureRefusesNonEmptyRefreshLock(t *testing.T) {
	s, c, a, _, r := retiringNative(t, ProviderClaude)
	lock := filepath.Join(c.ProfilePath, ".oauth_refresh.lock")
	if err := os.Mkdir(lock, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lock, "unknown"), []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := s.BeginCredentialErasure(r.Revision, a.ID, 1, "2.1.288")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true}); err == nil {
		t.Fatal("non-empty refresh lock erased")
	}
	if _, err := os.Stat(filepath.Join(c.ProfilePath, ".credentials.json")); err != nil {
		t.Fatal("deleted before complete inventory validation")
	}
}

func TestClaudeErasureRemovesEmptyLegacySiblingLock(t *testing.T) {
	s, c, a, _, r := retiringNative(t, ProviderClaude)
	legacy := c.ProfilePath + ".lock"
	if err := os.Mkdir(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := s.BeginCredentialErasure(r.Revision, a.ID, 1, "2.1.288")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy sibling refresh lock outlived erasure")
	}
}
