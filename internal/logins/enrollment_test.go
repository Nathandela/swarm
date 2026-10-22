package logins

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrollmentJobReplayNeverOverwritesNewerNativeGrant(t *testing.T) {
	s := testStore(t)
	job := strings.Repeat("a", 32)
	r := snapshot(t, s)
	r, l, e := s.Import(r.Revision, Login{ID: job, Provider: ProviderCodex, Label: "A", LastLoginJobID: job}, codexCredential("a", "initial"))
	if e != nil {
		t.Fatal(e)
	}
	r, l, e = s.Relogin(r.Revision, l.ID, codexCredential("a", "latest"))
	if e != nil {
		t.Fatal(e)
	}
	if l.LastLoginJobID != job {
		t.Fatal("ordinary reauth dropped consumed job marker")
	}
	// The accepted job may have no surviving source grant after terminal cleanup.
	if _, _, e = s.Import(0, Login{ID: job, Provider: ProviderCodex, Label: "A", LastLoginJobID: job}, Credential{}); e != nil {
		t.Fatalf("accepted import job replay failed: %v", e)
	}
	saved, e := s.readSaved(l)
	if e != nil || !strings.Contains(string(saved.Auth), "latest") {
		t.Fatalf("old enrollment replayed grant: %s %v", saved.Auth, e)
	}
	job2 := strings.Repeat("b", 32)
	r = snapshot(t, s)
	_, l, e = s.ReloginJob(r.Revision, l.ID, codexCredential("a", "job-two"), job2)
	if e != nil {
		t.Fatal(e)
	}
	activate(t, s, l)
	if _, e = durableReplace(s.root, "provider/codex/native/auth.json", codexCredential("a", "native-refreshed").Auth, writeOps{}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.ReloginJob(0, l.ID, Credential{}, job2); e != nil {
		t.Fatalf("accepted reauth job replay failed: %v", e)
	}
	auth, e := s.nativeCredential(ProviderCodex, l.ID)
	if e != nil || !strings.Contains(string(auth.Auth), "native-refreshed") {
		t.Fatal("old job overwrote native active refresh")
	}
}

func TestActiveLoginJobReplayIsIdempotentBeforeAndAfterCommit(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "old"))
	activate(t, s, a)
	job := strings.Repeat("d", 32)
	r := snapshot(t, s)
	op, e := s.BeginReloginJob(r.Revision, a.ID, codexCredential("a", "fresh"), "sign in", nil, job)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.BeginReloginJob(0, a.ID, Credential{}, "same job", nil, job)
	if e != nil || again.ID != op.ID {
		t.Fatalf("pending job restaged: %+v %v", again, e)
	}
	op.Phase = "stopping"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopped"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	if e = s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = durableReplace(s.root, "provider/codex/native/auth.json", codexCredential("a", "newer-native").Auth, writeOps{}); e != nil {
		t.Fatal(e)
	}
	if done, e := s.BeginReloginJob(0, a.ID, Credential{}, "ack completed", nil, job); e != nil || done.ID != "" {
		t.Fatalf("completed job created replacement operation: %+v %v", done, e)
	}
	auth, e := s.nativeCredential(ProviderCodex, a.ID)
	if e != nil || !strings.Contains(string(auth.Auth), "newer-native") {
		t.Fatal("completed active job replayed old token")
	}
}

func TestParkedLoginJobMarkerSurvivesCanonicalWriteCrash(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderClaude, "A", claudeCredential("a", "old"))
	job := strings.Repeat("e", 32)
	r := snapshot(t, s)
	s.writeOps.rename = func(old, new string) error {
		if new == s.savedPath(a) {
			return errors.New("crash after canonical Keychain write")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e := s.ReloginJob(r.Revision, a.ID, claudeCredential("a", "fresh"), job); e == nil {
		t.Fatal("injected crash not reached")
	}
	s.writeOps = writeOps{}
	if e := s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	r = snapshot(t, s)
	if r.Logins[0].LastLoginJobID != job {
		t.Fatal("reconciled grant has no consumed job marker")
	}
	if e := s.writeClaudeAuth(a.ID, claudeCredential("a", "newer").Auth); e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.ReloginJob(0, a.ID, Credential{}, job); e != nil {
		t.Fatal(e)
	}
	got, e := s.claudeAuth(a.ID)
	if e != nil || !strings.Contains(string(got), "newer") {
		t.Fatal("replayed parked job overwrote native refresh")
	}
}

func TestEnrollmentCleanupPreservesNonAuthFieldsAndIsIdempotent(t *testing.T) {
	for _, provider := range []string{ProviderClaude, ProviderCodex} {
		t.Run(provider, func(t *testing.T) {
			home, e := filepath.EvalSymlinks(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			if e = os.Chmod(home, 0700); e != nil {
				t.Fatal(e)
			}
			kc := &fakeKeychain{}
			files := map[string]string{"keep.txt": "keep"}
			if provider == ProviderCodex {
				files["auth.json"] = `{"auth_mode":"chatgpt","tokens":{"access_token":"partial"},"keep":{"setting":true}}`
			} else {
				files[".credentials.json"] = `{"claudeAiOauth":{"accessToken":"partial"},"mcpOAuth":{"keep":"mcp"}}`
				files[".claude.json"] = `{"oauthAccount":{"accountUuid":"partial"},"userID":"identity","projects":{"keep":true}}`
				if e = kc.Write(home, []byte(files[".credentials.json"])); e != nil {
					t.Fatal(e)
				}
			}
			for name, body := range files {
				if e = os.WriteFile(filepath.Join(home, name), []byte(body), 0600); e != nil {
					t.Fatal(e)
				}
			}
			for i := 0; i < 2; i++ {
				if e = eraseNativeCredential(provider, home, kc); e != nil {
					t.Fatal(e)
				}
			}
			for name := range files {
				raw, e := os.ReadFile(filepath.Join(home, name))
				if e != nil {
					t.Fatal(e)
				}
				if strings.Contains(string(raw), "partial") || strings.Contains(string(raw), "identity") || !strings.Contains(string(raw), "keep") {
					t.Fatalf("cleanup retained auth or removed nonauth %s: %s", name, raw)
				}
			}
			if provider == ProviderClaude {
				raw, e := kc.Read(home)
				if e != nil || strings.Contains(string(raw), "claudeAiOauth") || !strings.Contains(string(raw), "mcpOAuth") {
					t.Fatalf("Keychain cleanup changed unrelated credentials: %s %v", raw, e)
				}
			}
		})
	}
}

func TestVerifySavedDistinguishesUnavailableFromCorruptedCredential(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	r := snapshot(t, s)
	if _, e := s.UpdateAvailability(r.Revision, a.ID, AuthNeedsLogin, false, a.ExhaustedUntil); e != nil {
		t.Fatal(e)
	}
	if e := s.VerifySaved(a.ID); e != nil {
		t.Fatalf("valid needs-login snapshot refused: %v", e)
	}
	r = snapshot(t, s)
	unavailable := Login{ID: strings.Repeat("f", 32), Provider: ProviderClaude, Label: "no grant", Identity: identityDigest(ProviderClaude, "unavailable"), AuthState: AuthNeedsLogin}
	if _, _, e := s.ImportUnavailable(r.Revision, unavailable); e != nil {
		t.Fatal(e)
	}
	if e := s.VerifySaved(unavailable.ID); e != nil {
		t.Fatalf("metadata-only needs-login record refused: %v", e)
	}
	if _, e := durableReplace(s.root, s.savedPath(a), []byte(`{"revision":1,"identity":"wrong","credential":{"auth":null}}`), writeOps{}); e != nil {
		t.Fatal(e)
	}
	if e := s.VerifySaved(a.ID); e == nil {
		t.Fatal("needs-login label excused corrupt canonical record")
	}
}

func TestEnrollmentCleanupRemovesOnlyAuthStoresAndRefusesSymlinks(t *testing.T) {
	s := testStore(t)
	home := filepath.Join(s.path, "enrollment", "cleanup")
	if e := os.Mkdir(home, 0700); e != nil {
		t.Fatal(e)
	}
	kc := &fakeKeychain{}
	if e := kc.Write(home, []byte(`{"claudeAiOauth":{"accessToken":"secret"}}`)); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(home, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"secret"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := eraseNativeCredential(ProviderClaude, home, kc); e != nil {
		t.Fatal(e)
	}
	if e := eraseNativeCredential(ProviderClaude, home, kc); e != nil {
		t.Fatal(e)
	}
	if _, e := kc.Read(home); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("terminal Keychain grant survived cleanup")
	}
	if _, e := os.Stat(home); e != nil {
		t.Fatal("cleanup erased native home")
	}
	target := filepath.Join(home, "outside")
	if e := os.WriteFile(target, []byte(`{"tokens":{"access_token":"keep"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, filepath.Join(home, "auth.json")); e != nil {
		t.Fatal(e)
	}
	if e := eraseNativeCredential(ProviderCodex, home, nil); !errors.Is(e, ErrUnsafePath) {
		t.Fatalf("credential cleanup followed symlink: %v", e)
	}
	raw, e := os.ReadFile(target)
	if e != nil || !strings.Contains(string(raw), "keep") {
		t.Fatal("cleanup modified symlink target")
	}
}
