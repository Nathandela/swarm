package skeleton

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
)

// Shared account fixtures perform only portable filesystem and daemon setup.
// Native subprocess fixtures remain in the Linux-only test files.
const accountFixtureBearer = "synthetic-private-account-bearer"
const accountFixtureRefresh = "synthetic-private-account-refresh"

func accountTestState(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sacct-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func accountTestManager(t *testing.T, root string) *accountManager {
	t.Helper()
	// Manager startup probes only version; an empty PATH keeps these tests
	// independent of real installed providers and all native authentication.
	path := os.Getenv("PATH")
	t.Setenv("PATH", "/no-account-test-providers")
	m := openAccountManager(root, "", nil)
	t.Setenv("PATH", path)
	if m.unavailable != nil {
		t.Fatalf("private manager unavailable: %v", m.unavailable)
	}
	m.native = map[string]*persist.CLIIdentity{"codex": {Path: "/fixture/codex", Version: "0.160.0"}, "claude": {Path: "/fixture/claude", Version: "2.1.289"}}
	t.Cleanup(m.close)
	return m
}

func accountTestPut(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func accountTestCodexRaw(id, email string) []byte {
	claims, _ := json.Marshal(map[string]string{"email": email})
	raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": id, "access_token": accountFixtureBearer, "refresh_token": accountFixtureRefresh, "id_token": "fixture." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture"}})
	return raw
}

func accountTestCandidate(t *testing.T, m *accountManager, provider, id string) accounts.Candidate {
	t.Helper()
	c, err := m.store.CreateCandidate(provider, accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	if provider == "codex" {
		accountTestPut(t, filepath.Join(c.ProfilePath, "auth.json"), accountTestCodexRaw(id, "fixture@example.test"))
	} else {
		identity, _ := json.Marshal(map[string]any{"oauthAccount": map[string]string{"accountUuid": id, "organizationUuid": "fixture-org", "emailAddress": "fixture@example.test"}})
		credentials, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": accountFixtureBearer, "refreshToken": accountFixtureRefresh, "subscriptionType": "max", "scopes": []string{"user:inference"}}})
		accountTestPut(t, filepath.Join(c.ProfilePath, ".claude.json"), identity)
		accountTestPut(t, filepath.Join(c.ProfilePath, ".credentials.json"), credentials)
	}
	c, err = m.store.VerifyCandidate(c)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func accountTestRegistry(t *testing.T, m *accountManager) accounts.Registry {
	t.Helper()
	r, err := m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func accountTestAdmit(t *testing.T, m *accountManager, c accounts.Candidate) accounts.Account {
	t.Helper()
	_, account, err := m.store.Admit(accountTestRegistry(t, m).Revision, c, "Fixture account")
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func accountTestCore(t *testing.T, m *accountManager, adjust func(*daemon.Config)) *daemon.Daemon {
	t.Helper()
	cfg := daemon.Config{StateDir: m.stateRoot, SocketPath: filepath.Join(m.stateRoot, "d.sock"), LockPath: filepath.Join(m.stateRoot, "d.lock"), LogPath: filepath.Join(m.stateRoot, "d.log"), ShimBinary: "/bin/true", MaxSessions: 8}
	if adjust != nil {
		adjust(&cfg)
	}
	core, err := daemon.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	return core
}

func accountTestBinding(t *testing.T, m *accountManager, account accounts.Account) accounts.Binding {
	t.Helper()
	binding, err := m.store.CurrentBinding(account.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func accountTestLaunchEnvironment(t *testing.T, root string) (home, cwd string, env []string) {
	t.Helper()
	home, cwd = filepath.Join(root, "ordinary-home"), filepath.Join(root, "project")
	for _, directory := range []string{home, filepath.Join(home, ".codex"), cwd} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	accountTestPut(t, filepath.Join(home, ".codex", "auth.json"), []byte("synthetic-ambient-file"))
	accountTestPut(t, filepath.Join(home, ".codex", "config.toml"), []byte("model = \"gpt-default\"\nmodel_reasoning_effort = \"high\"\n[sandbox_workspace_write]\nnetwork_access = true\n"))
	return home, cwd, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "OPENAI_API_KEY=synthetic-ambient-key"}
}
