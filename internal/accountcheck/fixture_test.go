package accountcheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
)

func checkFixture(t *testing.T, mode, checkMode string) (string, Config, string) {
	t.Helper()
	base := t.TempDir()
	state, home, cwd, fixture := filepath.Join(base, "state"), filepath.Join(base, "home"), filepath.Join(base, "scratch"), filepath.Join(base, "fixture")
	for _, path := range []string{state, home, cwd, fixture} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	candidate, err := store.CreateCandidate(accounts.ProviderClaude, accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		".claude.json":      `{"oauthAccount":{"accountUuid":"check-fixture","organizationUuid":"check-fixture-org"}}`,
		".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-bearer","refreshToken":"synthetic-refresh","subscriptionType":"max","scopes":["user:inference"]}}`,
	} {
		if err := os.WriteFile(filepath.Join(candidate.ProfilePath, name), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err = store.VerifyCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	_, account, err := store.Admit(registry.Revision, candidate, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.CurrentBinding(account.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "mode"), []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{StateRoot: state, Binding: binding, CLI: persist.CLIIdentity{Path: exe, Version: "2.1.288", Fingerprint: fingerprint}, Mode: checkMode, Model: "claude-synthetic", Cwd: cwd, Env: []string{"HOME=" + home, "CHECK_FIXTURE_DIR=" + fixture}, Deadline: time.Now().Add(20 * time.Second)}
	t.Cleanup(func() {
		var child processcontain.Identity
		raw, _ := os.ReadFile(filepath.Join(fixture, "writer.json"))
		if json.Unmarshal(raw, &child) == nil {
			_ = processcontain.SignalIdentity(child, syscall.SIGKILL)
		}
	})
	return exe, cfg, fixture
}
