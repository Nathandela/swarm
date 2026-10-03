package accounts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, stateRoot
}

func nativeCandidate(t *testing.T, s *Store, account string) Candidate {
	t.Helper()
	c, err := s.CreateCandidate(ProviderCodex, KindNative)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": account, "access_token": "synthetic-bearer-" + account, "refresh_token": "synthetic-refresh-" + account}})
	if err := os.WriteFile(filepath.Join(c.ProfilePath, "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = s.VerifyCandidate(c)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDiscardCandidateReconcilesVisibleDeletionAndNeverDeletesAdmittedGeneration(t *testing.T) {
	s, root := testStore(t)
	candidate := nativeCandidate(t, s, "discard-synthetic")
	if err := os.RemoveAll(candidate.ProfilePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "accounts", "vault", candidate.ProfileGeneration), []byte("synthetic-custody"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardCandidate(candidate, true); err != nil {
		t.Fatalf("visible deletion could not reconcile: %v", err)
	}
	if err := s.DiscardCandidate(candidate, true); err != nil {
		t.Fatalf("cleanup is not idempotent: %v", err)
	}
	admitted, _ := admitNative(t, s, "never-discard-admitted")
	if err := s.DiscardCandidate(admitted, true); !errors.Is(err, ErrInUse) {
		t.Fatalf("admitted generation cleanup accepted: %v", err)
	}
	unsafe := nativeCandidate(t, s, "unsafe-discard")
	if err := os.RemoveAll(unsafe.ProfilePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), unsafe.ProfilePath); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardCandidate(unsafe, true); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("unsafe existing generation cleanup accepted: %v", err)
	}
}

func admitNative(t *testing.T, s *Store, account string) (Candidate, Account) {
	t.Helper()
	c := nativeCandidate(t, s, account)
	r, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	_, a, err := s.Admit(r.Revision, c, account)
	if err != nil {
		t.Fatal(err)
	}
	return c, a
}

func TestRegistryCASAcrossHandlesAndSnapshotIsolation(t *testing.T) {
	s, root := testStore(t)
	_, a := admitNative(t, s, "account-a")
	other, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	r, _ := s.Snapshot()
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for _, handle := range []*Store{s, other} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			_, err := store.SetLifecycle(r.Revision, a.ID, LifecyclePaused)
			errorsSeen <- err
		}(handle)
	}
	wg.Wait()
	close(errorsSeen)
	success, conflict := 0, 0
	for err := range errorsSeen {
		if err == nil {
			success++
		} else if errors.Is(err, ErrRevisionConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	r.Accounts[a.ID] = Account{}
	r.Enabled[ProviderCodex] = true
	latest, err := s.Snapshot()
	if err != nil || latest.Accounts[a.ID].ID != a.ID || latest.Enabled[ProviderCodex] {
		t.Fatalf("snapshot aliases persisted state: %v %+v", err, latest)
	}
	if latest.Revision != r.Revision+1 {
		t.Fatal("CAS revision did not advance exactly once")
	}
}

func TestDedupReauthenticationAndFrozenGeneration(t *testing.T) {
	s, _ := testStore(t)
	first, a := admitNative(t, s, "account-a")
	b, err := s.CurrentBinding(a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := nativeCandidate(t, s, "account-a")
	r, _ := s.Snapshot()
	latest, existing, err := s.Admit(r.Revision, duplicate, "duplicate")
	if !errors.Is(err, ErrDuplicate) || existing.ID != a.ID || latest.Revision != r.Revision {
		t.Fatalf("dedup: %v %+v", err, existing)
	}
	if err := s.DiscardCandidate(first, true); !errors.Is(err, ErrInUse) {
		t.Fatalf("admitted profile was discardable: %v", err)
	}
	if err := s.DiscardCandidate(duplicate, false); !errors.Is(err, ErrInUse) {
		t.Fatalf("live candidate was discardable: %v", err)
	}
	r, a, err = s.Reauthenticate(r.Revision, a.ID, duplicate)
	if err != nil || a.CurrentGeneration != 2 {
		t.Fatalf("reauth: %v %+v", err, a)
	}
	oldPath, err := s.ProfilePath(b)
	if err != nil || oldPath != first.ProfilePath {
		t.Fatalf("old frozen binding changed: %v %s", err, oldPath)
	}
	newBinding, _ := s.CurrentBinding(a.ID, 1)
	newPath, _ := s.ProfilePath(newBinding)
	if newPath != duplicate.ProfilePath || newBinding.CredentialGeneration != 2 || b.Identity != newBinding.Identity {
		t.Fatal("reauth moved directory or changed identity")
	}
	wrong := nativeCandidate(t, s, "account-b")
	if _, _, err := s.Reauthenticate(r.Revision, a.ID, wrong); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("wrong account reauth admitted: %v", err)
	}
}

func TestIdentityRefreshAndNoAmbientFallback(t *testing.T) {
	s, root := testStore(t)
	c, a := admitNative(t, s, "account-a")
	b, _ := s.CurrentBinding(a.ID, 7)
	inherited := []string{"HOME=/normal/home", "PATH=/bin", "SSH_AUTH_SOCK=/agent", "OPENAI_API_KEY=ambient-api-key", "ANTHROPIC_AUTH_TOKEN=ambient-token", "CODEX_HOME=/ambient", "CLAUDE_CONFIG_DIR=/ambient"}
	env, err := ResolveBoundEnvironment(root, b, inherited)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "HOME=/normal/home") || !strings.Contains(joined, "SSH_AUTH_SOCK=/agent") || !strings.Contains(joined, "CODEX_HOME="+c.ProfilePath) || strings.Contains(joined, "ambient") || strings.Contains(joined, "synthetic-bearer") {
		t.Fatalf("bad bound environment: %q", joined)
	}
	before, _ := os.ReadFile(filepath.Join(root, "accounts", "registry.json"))
	reader, err := OpenReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if _, err := reader.CreateCandidate(ProviderCodex, KindNative); !errors.Is(err, ErrIneligible) {
		t.Fatal("read-only handle mutated")
	}
	after, _ := os.ReadFile(filepath.Join(root, "accounts", "registry.json"))
	if string(before) != string(after) {
		t.Fatal("shim resolution changed registry")
	}
	refresh := `{"auth_mode":"chatgpt","tokens":{"account_id":"account-a","access_token":"fresh","refresh_token":"fresh-refresh"},"last_refresh":"tomorrow"}`
	if err := os.WriteFile(filepath.Join(c.ProfilePath, "auth.json"), []byte(refresh), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateBinding(b); err != nil {
		t.Fatalf("normal token refresh changed identity: %v", err)
	}
	wrong := strings.Replace(refresh, "account-a", "account-b", 1)
	if err := os.WriteFile(filepath.Join(c.ProfilePath, "auth.json"), []byte(wrong), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBoundEnvironment(root, b, inherited); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("identity mismatch fell back to ambient auth: %v", err)
	}
}

func TestCredentialErasureRetainsHistoryAndGeneration(t *testing.T) {
	s, _ := testStore(t)
	c, a := admitNative(t, s, "account-a")
	if err := os.Mkdir(filepath.Join(c.ProfilePath, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(c.ProfilePath, "sessions", "conversation.jsonl")
	if err := os.WriteFile(history, []byte("saved conversation"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Snapshot()
	proof := ErasureProof{WritersStopped: true, CompleteInventory: true, CredentialFiles: []string{"auth.json"}}
	proof.LiveReferences = 1
	if _, err := s.EraseCredentials(r.Revision, a.ID, 1, proof); !errors.Is(err, ErrInUse) {
		t.Fatalf("erased in-use account: %v", err)
	}
	proof.LiveReferences = 0
	proof.CompleteInventory = false
	if _, err := s.EraseCredentials(r.Revision, a.ID, 1, proof); !errors.Is(err, ErrIneligible) {
		t.Fatalf("unknown inventory claimed erased: %v", err)
	}
	proof.CompleteInventory = true
	r, err := s.EraseCredentials(r.Revision, a.ID, 1, proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.ProfilePath, "auth.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("credential still exists")
	}
	if raw, err := os.ReadFile(history); err != nil || string(raw) != "saved conversation" {
		t.Fatalf("history erased: %v", err)
	}
	if !r.Accounts[a.ID].Generations[1].CredentialErased || r.Accounts[a.ID].Generations[1].ProfileGeneration != c.ProfileGeneration {
		t.Fatal("generation history reference lost")
	}
	if _, err := s.CurrentBinding(a.ID, 1); !errors.Is(err, ErrIneligible) {
		t.Fatal("erased credential still launchable")
	}
}

func TestStrongPersistenceFailureSides(t *testing.T) {
	s, _ := testStore(t)
	r, _ := s.Snapshot()
	s.writeOps.rename = func(string, string) error { return errors.New("injected rename failure") }
	latest, err := s.SetEnabled(r.Revision, ProviderCodex, true)
	if err == nil || latest.Revision != r.Revision || latest.Enabled[ProviderCodex] {
		t.Fatalf("pre-rename failure was visible: %+v %v", latest, err)
	}
	s.writeOps.rename = nil
	s.writeOps.syncDir = func(string) error { return errors.New("injected parent sync failure") }
	latest, err = s.SetEnabled(r.Revision, ProviderCodex, true)
	if !errors.Is(err, ErrDurabilityUncertain) || latest.Revision != r.Revision+1 || !latest.Enabled[ProviderCodex] {
		t.Fatalf("post-rename failed visibility: %+v %v", latest, err)
	}
	if _, err := s.SetEnabled(latest.Revision, ProviderClaude, true); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("uncertain write blindly retried: %v", err)
	}
	s.writeOps = writeOps{}
	reconciled, err := s.Reconcile()
	if err != nil || reconciled.Revision != latest.Revision {
		t.Fatalf("reconcile: %+v %v", reconciled, err)
	}
	if _, err := s.SetEnabled(reconciled.Revision, ProviderClaude, true); err != nil {
		t.Fatal(err)
	}
}

func TestUnsafeNativePathsAndRegistryFIFO(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "public", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := testStore(t)
			c, a := admitNative(t, s, "account-a")
			b, _ := s.CurrentBinding(a.ID, 1)
			path := filepath.Join(c.ProfilePath, "auth.json")
			raw, _ := os.ReadFile(path)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.WriteFile(filepath.Join(c.ProfilePath, "other.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other.json", path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.WriteFile(path, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				other := filepath.Join(c.ProfilePath, "other.json")
				if err := os.WriteFile(other, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(other, path); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.ValidateBinding(b); err == nil {
				t.Fatal("unsafe credential path admitted")
			}
		})
	}
	s, root := testStore(t)
	registry := filepath.Join(root, "accounts", "registry.json")
	if err := os.Remove(registry); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(registry, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("FIFO registry not rejected: %v", err)
	}
}

func TestClaudeCachedIdentityAndUnverifiedTokenCustody(t *testing.T) {
	s, root := testStore(t)
	identity := []byte(`{"oauthAccount":{"accountUuid":"personal-a","organizationUuid":"personal-org","emailAddress":"display@example.test"}}`)
	first, err := ClaudeIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ClaudeIdentity([]byte(strings.Replace(string(identity), "display@example.test", "other@example.test", 1)))
	if err != nil || second != first {
		t.Fatal("mutable display email became identity")
	}
	if _, err := ClaudeIdentity([]byte(`{"oauthAccount":{"emailAddress":"display@example.test"}}`)); err == nil {
		t.Fatal("email-only cached identity accepted")
	}
	const secret = "synthetic-one-year-secret-token"
	c, err := s.ImportClaudeToken(secret)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.Snapshot()
	r, a, err := s.Admit(r.Revision, c, "manual token")
	if err != nil {
		t.Fatal(err)
	}
	if a.Generations[1].Identity != "" || a.Generations[1].Verification != VerificationUnverified {
		t.Fatal("token pretended to authenticate cached identity")
	}
	registry, _ := os.ReadFile(filepath.Join(root, "accounts", "registry.json"))
	marker, _ := os.ReadFile(filepath.Join(c.ProfilePath, candidateFile))
	if strings.Contains(string(registry), secret) || strings.Contains(string(marker), secret) || c.TokenFingerprint == identityDigest(ProviderClaude, secret) {
		t.Fatal("token escaped private vault or used unkeyed fingerprint")
	}
	r, _ = s.SetEnabled(r.Revision, ProviderClaude, true)
	if _, err := Select(r, SelectionRequest{Provider: ProviderClaude, Model: "sonnet", ConfigurationGeneration: 1}, time.Now()); !errors.Is(err, ErrNoCapacity) {
		t.Fatal("unverified token joined automatic pool")
	}
	duplicate, err := s.ImportClaudeToken(secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, existing, err := s.Admit(r.Revision, duplicate, "duplicate token"); !errors.Is(err, ErrDuplicate) || existing.ID != a.ID {
		t.Fatalf("duplicate token not detected: %+v %v", existing, err)
	}
	b, err := s.CurrentBinding(a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	env, err := ResolveBoundEnvironment(root, b, []string{"HOME=/ordinary/home"})
	if err != nil || !strings.Contains(strings.Join(env, "\n"), "CLAUDE_CODE_OAUTH_TOKEN="+secret) {
		t.Fatalf("manual token child custody: %v", err)
	}
}
