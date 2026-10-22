package logins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	s.keychain = &fakeKeychain{}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func codexCredential(id, token string) Credential {
	return Credential{Auth: json.RawMessage(`{"auth_mode":"chatgpt","tokens":{"account_id":"` + id + `","access_token":"` + token + `","refresh_token":"refresh-` + token + `"}}`)}
}
func claudeCredential(id, token string) Credential {
	return Credential{Auth: json.RawMessage(`{"claudeAiOauth":{"accessToken":"` + token + `","refreshToken":"refresh-` + token + `","expiresAt":1234}}`), Identity: json.RawMessage(`{"oauthAccount":{"accountUuid":"` + id + `","organizationUuid":"org"},"userID":"` + id + `"}`)}
}
func add(t *testing.T, s *Store, p, label string, c Credential) Login {
	t.Helper()
	r, e := s.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	_, l, e := s.Add(r.Revision, p, label, c)
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func snapshot(t *testing.T, s *Store) Registry {
	t.Helper()
	r, e := s.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func beginStopped(t *testing.T, s *Store, l Login) Switch {
	t.Helper()
	r := snapshot(t, s)
	op, e := s.BeginSwitch(r.Revision, l.Provider, l.ID, "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopping"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopped"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	return op
}
func activate(t *testing.T, s *Store, l Login) Runtime {
	t.Helper()
	op := beginStopped(t, s, l)
	if e := s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	rt, e := s.CommitSwitch(op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	return rt
}

func TestCaptureLatestNativeRefreshAcrossABA(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "old"))
	b := add(t, s, ProviderCodex, "B", codexCredential("b", "b-token"))
	rt := activate(t, s, a)
	if _, e := s.PrepareActivation(a.ID); !errors.Is(e, ErrInUse) {
		t.Fatalf("active parked copy admitted: %v", e)
	}
	// A native writer atomically refreshed the only authoritative active store.
	if _, e := durableReplace(s.root, "provider/codex/native/auth.json", codexCredential("a", "latest").Auth, writeOps{}); e != nil {
		t.Fatal(e)
	}
	if e := s.ValidateRuntime(rt); e != nil {
		t.Fatalf("same-account refresh invalidated runtime: %v", e)
	}
	activate(t, s, b)
	again := activate(t, s, a)
	raw, e := os.ReadFile(filepath.Join(again.Home, "auth.json"))
	if e != nil || !strings.Contains(string(raw), `"latest"`) {
		t.Fatalf("lost active refresh: %s %v", raw, e)
	}
	if again.Home != rt.Home || again.Epoch != 3 {
		t.Fatalf("home/epoch: before=%+v after=%+v", rt, again)
	}
}

func TestPendingSwitchRejectsOldEpochAndRequiresStoppedProof(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	b := add(t, s, ProviderCodex, "B", codexCredential("b", "b"))
	rt := activate(t, s, a)
	r := snapshot(t, s)
	op, e := s.BeginSwitch(r.Revision, ProviderCodex, b.ID, "manual", []Restart{{SourceID: "discussion", ConversationID: "native"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ValidateRuntime(rt); !errors.Is(e, ErrPending) {
		t.Fatalf("pending accepted old epoch: %v", e)
	}
	if e = s.CaptureOutgoing(op.ID); !errors.Is(e, ErrInUse) {
		t.Fatalf("captured before stop: %v", e)
	}
	if e = s.InstallIncoming(op.ID); !errors.Is(e, ErrInUse) {
		t.Fatalf("installed before capture: %v", e)
	}
	op.Phase = "stopping"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopped"
	if e = s.SaveSwitch(op); !errors.Is(e, ErrInUse) {
		t.Fatalf("stopped phase with live row: %v", e)
	}
}

func TestSwitchRestartOnlyOwnsStoppedSourceAfterCommit(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	r := snapshot(t, s)
	op, e := s.BeginSwitch(r.Revision, ProviderCodex, a.ID, "manual", []Restart{{SourceID: "source", ConversationID: "native"}})
	if e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopping"
	op.Sessions[0].Phase = "claimed"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	if e = s.CancelSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	if len(snapshot(t, s).Switches) != 1 {
		t.Fatal("cancel abandoned a claimed source")
	}
	op = snapshot(t, s).Switches[ProviderCodex]
	op.Phase = "stopped"
	op.Sessions[0].Phase = "stopped"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RuntimeForSwitch(op.ID, "source"); !errors.Is(e, ErrPending) {
		t.Fatalf("early restart: %v", e)
	}
	if e = s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	rt, e := s.CommitSwitch(op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ValidateSwitchRestart(op.ID, "other", rt); !errors.Is(e, ErrPending) {
		t.Fatalf("unowned restart: %v", e)
	}
	if e = s.ValidateSwitchRestart(op.ID, "source", rt); e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteSwitch(op.ID); !errors.Is(e, ErrInUse) {
		t.Fatalf("lost resume obligation: %v", e)
	}
}

type fakeKeychain struct {
	values map[string][]byte
	writes int
	fail   bool
}

func (k *fakeKeychain) Read(home string) ([]byte, error) {
	if k.values[home] == nil {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), k.values[home]...), nil
}
func (k *fakeKeychain) Write(home string, b []byte) error {
	k.writes++
	if k.fail {
		return errors.New("synthetic unavailable")
	}
	if k.values == nil {
		k.values = map[string][]byte{}
	}
	k.values[home] = append([]byte(nil), b...)
	return nil
}
func (k *fakeKeychain) Delete(home string) error { delete(k.values, home); return nil }
func TestClaudePartialKeychainIdentityTransactionReplays(t *testing.T) {
	s := testStore(t)
	kc := &fakeKeychain{}
	s.keychain = kc
	a := add(t, s, ProviderClaude, "A", claudeCredential("a", "a-token"))
	b := add(t, s, ProviderClaude, "B", claudeCredential("b", "b-token"))
	rt := activate(t, s, a)
	// Unrelated native identity/settings and MCP credential keys survive swaps.
	for name, body := range map[string]string{".claude.json": `{"oauthAccount":{"accountUuid":"a","organizationUuid":"org"},"userID":"a","projects":{"project":{"trust":true}}}`, ".credentials.json": `{"mcpOAuth":{"private":"keep"}}`} {
		if _, e := durableReplace(s.root, "provider/claude/native/"+name, []byte(body), writeOps{}); e != nil {
			t.Fatal(e)
		}
	}
	op := beginStopped(t, s, b)
	if e := s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	// Simulate interruption after Keychain changes and before identity changes.
	s.writeOps.rename = func(old, new string) error {
		if strings.HasSuffix(new, ".claude.json") {
			return errors.New("synthetic interrupted")
		}
		return s.root.Rename(old, new)
	}
	if e := s.InstallIncoming(op.ID); e == nil {
		t.Fatal("partial transaction accepted")
	}
	if snapshot(t, s).Switches[ProviderClaude].Phase != "installing" {
		t.Fatal("missing install intent")
	}
	if e := s.ValidateRuntime(rt); !errors.Is(e, ErrPending) {
		t.Fatalf("mixed identity admitted: %v", e)
	}
	s.writeOps = writeOps{}
	if e := s.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	next, e := s.CommitSwitch(op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if next.Identity != b.Identity {
		t.Fatal("destination mismatch")
	}
	if e = s.CompleteSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	for name, needle := range map[string]string{".claude.json": "projects", ".credentials.json": "mcpOAuth"} {
		raw, e := os.ReadFile(filepath.Join(rt.Home, name))
		if e != nil || !strings.Contains(string(raw), needle) {
			t.Fatalf("lost unrelated %s: %s %v", name, raw, e)
		}
		if name == ".credentials.json" && strings.Contains(string(raw), "claudeAiOauth") {
			t.Fatal("provider grant copied into common context")
		}
	}
	activate(t, s, a)
	auth, e := kc.Read(s.savedNativeHome(a.ID))
	if e != nil || !strings.Contains(string(auth), "a-token") {
		t.Fatal("A credentials lost after partial B promotion")
	}
}

func TestClaudeCanonicalGrantHomeSurvivesNativeRefreshAndABA(t *testing.T) {
	for _, mode := range []string{"keychain", "file"} {
		t.Run(mode, func(t *testing.T) {
			s := testStore(t)
			if mode == "file" {
				s.keychain = nil
			}
			a := add(t, s, ProviderClaude, "A", claudeCredential("a", "original"))
			b := add(t, s, ProviderClaude, "B", claudeCredential("b", "b"))
			rt := activate(t, s, a)
			env, e := s.PrepareEnvironment(rt, []string{"PATH=/bin"})
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(strings.Join(env, "\n"), "CLAUDE_CONFIG_DIR="+rt.Home) || !strings.Contains(strings.Join(env, "\n"), "CLAUDE_SECURESTORAGE_CONFIG_DIR="+s.savedNativeHome(a.ID)) {
				t.Fatalf("native home split missing: %v", env)
			}
			// This is the native writer's sole canonical store, active or parked.
			if e = s.writeClaudeAuth(a.ID, claudeCredential("a", "native-refreshed").Auth); e != nil {
				t.Fatal(e)
			}
			if e = s.ValidateRuntime(rt); e != nil {
				t.Fatalf("same identity refresh changed epoch: %v", e)
			}
			activate(t, s, b)
			activate(t, s, a)
			got, e := s.claudeAuth(a.ID)
			if e != nil || !strings.Contains(string(got), "native-refreshed") {
				t.Fatalf("canonical native refresh lost: %s %v", got, e)
			}
			meta, e := os.ReadFile(filepath.Join(s.path, s.savedPath(a)))
			if e != nil || strings.Contains(string(meta), "native-refreshed") || strings.Contains(string(meta), "original") {
				t.Fatalf("grant duplicated in metadata: %s %v", meta, e)
			}
			if _, e = os.Stat(filepath.Join(rt.Home, ".credentials.json")); !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("common grant shadow created: %v", e)
			}
		})
	}
}

func TestUnavailableNativeIdentityCanReloginWithoutOldCredentials(t *testing.T) {
	s := testStore(t)
	c := codexCredential("a", "renewed")
	identity, e := CodexIdentity(c.Auth)
	if e != nil {
		t.Fatal(e)
	}
	r := snapshot(t, s)
	_, l, e := s.ImportUnavailable(r.Revision, Login{ID: strings.Repeat("a", 32), Provider: ProviderCodex, Label: "expired", Identity: identity, AuthState: AuthNeedsLogin})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	if _, e = s.PrepareActivation(l.ID); !errors.Is(e, ErrIneligible) {
		t.Fatalf("missing native grant admitted: %v", e)
	}
	r = snapshot(t, s)
	if _, _, e = s.Relogin(r.Revision, l.ID, c); e != nil {
		t.Fatal(e)
	}
	activate(t, s, l)
}

func TestReloginCASAndWrongIdentity(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	r := snapshot(t, s)
	if _, _, e := s.Relogin(r.Revision-1, a.ID, codexCredential("a", "new")); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale CAS accepted: %v", e)
	}
	if _, _, e := s.Relogin(r.Revision, a.ID, codexCredential("b", "new")); !errors.Is(e, ErrIdentityChanged) {
		t.Fatalf("wrong identity accepted: %v", e)
	}
	if _, _, e := s.Relogin(r.Revision, a.ID, codexCredential("a", "new")); e != nil {
		t.Fatal(e)
	}
	activate(t, s, a)
}

func TestPrivatePathRejectsSymlinksModesAndHardlinks(t *testing.T) {
	for _, kind := range []string{"symlink", "mode", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
			path := filepath.Join(s.path, s.savedPath(a))
			switch kind {
			case "symlink":
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				other := filepath.Join(s.path, "other")
				if e = os.WriteFile(other, raw, 0600); e != nil {
					t.Fatal(e)
				}
				if e = os.Remove(path); e != nil {
					t.Fatal(e)
				}
				if e = os.Symlink(other, path); e != nil {
					t.Fatal(e)
				}
			case "mode":
				if e := os.Chmod(path, 0644); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				if e := os.Link(path, filepath.Join(s.path, "linked")); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.PrepareActivation(a.ID); !errors.Is(e, ErrUnsafePath) {
				t.Fatalf("unsafe file accepted: %v", e)
			}
		})
	}
}

func TestSelectionIsFiniteAndUnknownLimitRemainsExhausted(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	b := add(t, s, ProviderCodex, "B", codexCredential("b", "b"))
	c := add(t, s, ProviderCodex, "C", codexCredential("c", "c"))
	activate(t, s, a)
	r := snapshot(t, s)
	if _, e := s.UpdateAvailability(r.Revision, b.ID, AuthReady, true, time.Time{}); e != nil {
		t.Fatal(e)
	}
	next, e := s.NextEligible(ProviderCodex, nil, time.Now())
	if e != nil || next.ID != c.ID {
		t.Fatalf("selection %+v %v", next, e)
	}
	if _, e = s.NextEligible(ProviderCodex, []string{c.ID}, time.Now()); !errors.Is(e, ErrNoEligible) {
		t.Fatalf("revisited or invented unknown reset: %v", e)
	}
}

func TestCodexStorageModeRefusesKeyringWithoutTouchingConfig(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	op := beginStopped(t, s, a)
	raw := []byte("cli_auth_credentials_store = 'keyring'\nmodel = 'preserve'\n")
	if _, e := durableReplace(s.root, "provider/codex/native/config.toml", raw, writeOps{}); e != nil {
		t.Fatal(e)
	}
	if e := s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.InstallIncoming(op.ID); !errors.Is(e, ErrIneligible) {
		t.Fatalf("conflicting storage admitted: %v", e)
	}
	got, e := os.ReadFile(filepath.Join(s.Home(ProviderCodex), "config.toml"))
	if e != nil || string(got) != string(raw) {
		t.Fatal("refusal changed config")
	}
}

func TestActiveReloginKeepsIncomingGrantSeparateFromOutgoingCapture(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "original"))
	old := activate(t, s, a)
	if _, e := durableReplace(s.root, "provider/codex/native/auth.json", codexCredential("a", "latest-outgoing").Auth, writeOps{}); e != nil {
		t.Fatal(e)
	}
	r := snapshot(t, s)
	op, e := s.BeginRelogin(r.Revision, a.ID, codexCredential("a", "new-grant"), "relogin", nil)
	if e != nil {
		t.Fatal(e)
	}
	if !op.Relogin || op.FromID != op.ToID {
		t.Fatalf("bad relogin journal %+v", op)
	}
	if e = s.ValidateRuntime(old); !errors.Is(e, ErrPending) {
		t.Fatalf("admission stayed open: %v", e)
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
	saved, e := s.readSaved(snapshot(t, s).Logins[0])
	if e != nil || !strings.Contains(string(saved.Auth), "latest-outgoing") {
		t.Fatalf("outgoing authority lost: %s %v", saved.Auth, e)
	}
	// Restarted coordinator reuses the durable incoming grant.
	if e = s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	if e = s.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	next, e := s.CommitSwitch(op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if next.Epoch != old.Epoch+1 || next.Identity != old.Identity || next.Home != old.Home {
		t.Fatalf("bad reauth runtime %+v", next)
	}
	if e = s.CompleteSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(filepath.Join(next.Home, "auth.json"))
	if e != nil || !strings.Contains(string(got), "new-grant") {
		t.Fatalf("installed outgoing instead: %s %v", got, e)
	}
	if _, e = os.Stat(filepath.Join(s.path, "pending", op.ID+".json")); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("staged secret survived commit: %v", e)
	}
	parked, e := s.readSaved(snapshot(t, s).Logins[0])
	if e != nil || !strings.Contains(string(parked.Auth), "new-grant") {
		t.Fatalf("saved old grant: %s %v", parked.Auth, e)
	}
}

func TestCredentialRevisionAndRemovalRecoverAfterRegistryWriteFailure(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "original"))
	r := snapshot(t, s)
	s.writeOps.rename = func(old, new string) error {
		if new == "registry.json" {
			return errors.New("synthetic crash before registry commit")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e := s.Relogin(r.Revision, a.ID, codexCredential("a", "new")); e == nil {
		t.Fatal("missing injected registry failure")
	}
	s.writeOps = writeOps{}
	if e := s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	r = snapshot(t, s)
	if r.Logins[0].CredentialRevision != a.CredentialRevision+1 {
		t.Fatal("revision not recovered")
	}
	if _, e := s.Remove(r.Revision, a.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(s.path, "saved", a.ID)); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("removed credential retained: %v", e)
	}
	// A crash after removing registry membership but before secret deletion.
	if e := s.writeSaved(a, codexCredential("a", "orphan")); e != nil {
		t.Fatal(e)
	}
	if e := s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(s.path, "saved", a.ID)); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("unreferenced credential retained: %v", e)
	}
}

func TestCompetingNativeAuthSettingsRefuseBeforeSwitch(t *testing.T) {
	for _, provider := range []string{ProviderCodex, ProviderClaude} {
		t.Run(provider, func(t *testing.T) {
			s := testStore(t)
			c := codexCredential("a", "a")
			name, body := "config.toml", `cli_auth_credentials_store = 'keyring'`
			if provider == ProviderClaude {
				c = claudeCredential("a", "a")
				name, body = "settings.json", `{"env":{"ANTHROPIC_AUTH_TOKEN":"outside"}}`
			}
			a := add(t, s, provider, "A", c)
			if _, e := durableReplace(s.root, filepath.Join("provider", provider, "native", name), []byte(body), writeOps{}); e != nil {
				t.Fatal(e)
			}
			r := snapshot(t, s)
			if _, e := s.BeginSwitch(r.Revision, provider, a.ID, "manual", nil); !errors.Is(e, ErrIneligible) {
				t.Fatalf("competing native authority admitted: %v", e)
			}
			if len(snapshot(t, s).Switches) != 0 {
				t.Fatal("preflight failure created downtime")
			}
		})
	}
}

func TestEnvironmentNeverCopiesCompetingAuthentication(t *testing.T) {
	rt := Runtime{Provider: ProviderClaude, Epoch: 1, Identity: identityDigest(ProviderClaude, "a"), Home: "/private/native"}
	env, e := prepareEnvironment(rt, "/private/saved", []string{"PATH=/bin", "USER=unsafe@name", "OPENAI_API_KEY=secret", "CLAUDE_CODE_OAUTH_TOKEN=secret", "CLAUDE_SECURESTORAGE_CONFIG_DIR=/external"})
	if e != nil {
		t.Fatal(e)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "/external") || strings.Contains(joined, "unsafe@") {
		t.Fatalf("conflicting authority retained: %s", joined)
	}
	if !strings.Contains(joined, "CLAUDE_SECURESTORAGE_CONFIG_DIR=/private/saved") {
		t.Fatal(joined)
	}
}
