package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
)

func nativeClaudeEnvironmentFixture(t *testing.T, stateRoot string) (accounts.Binding, string, string) {
	t.Helper()
	store, err := accounts.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	candidate, err := store.CreateCandidate(accounts.ProviderClaude, accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","subscriptionType":"max","scopes":["user:inference"]}}`,
		".claude.json":      `{"oauthAccount":{"accountUuid":"environment-fixture","organizationUuid":"synthetic-org","emailAddress":"fixture@example.invalid"}}`,
	} {
		if err := os.WriteFile(filepath.Join(candidate.ProfilePath, name), []byte(raw), 0600); err != nil {
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
	_, account, err := store.Admit(registry.Revision, candidate, "environment-fixture")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.CurrentBinding(account.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	home := canonicalTempDir(t)
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	return binding, candidate.ProfilePath, home
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeConfigurationEnvironmentUsesExactLaunchOriginWithoutCredentials(t *testing.T) {
	saved := []string{"HOME=/saved", "CODEX_HOME=/saved/codex", "CLAUDE_CONFIG_DIR=/saved/claude", "XDG_CONFIG_HOME=/saved/xdg", "CLAUDE_CODE_NO_MODEL_FALLBACK=false", "CLAUDE_CODE_USE_MANTLE=true", "CLAUDE_CODE_REMOTE_SETTINGS_PATH=false", "CLAUDE_CODE_MANAGED_SETTINGS_PATH=0", "ANTHROPIC_API_KEY=synthetic-do-not-capture", "CODEX_ACCESS_TOKEN=synthetic-do-not-capture", "CLAUDE_CODE_DIAGNOSTICS_FILE=/untrusted/diagnostics", "UNRELATED_SECRET=synthetic-do-not-capture"}
	want := saved[1:8]
	if got := NativeConfigurationEnvironment(saved); !reflect.DeepEqual(got, want) {
		t.Fatalf("configuration capture dropped selector or retained credential: %v", got)
	}
	original := daemonEnviron
	t.Cleanup(func() { daemonEnviron = original })
	live := []string{"CODEX_HOME=/live/codex", "CLAUDE_CODE_USE_ANTHROPIC_AWS=1", "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD=true"}
	daemonEnviron = func() []string { return live }
	d := &Daemon{savedEnv: saved}
	if got := d.NativeConfigurationEnvironment(nil); !reflect.DeepEqual(got, want) {
		t.Fatal("nil client borrowed live origin instead of saved origin")
	}
	client := []string{"CODEX_HOME=/client/first", "CODEX_HOME=/client/last", "CLAUDE_CONFIG_DIR=/client/claude"}
	if got := d.NativeConfigurationEnvironment(client); !reflect.DeepEqual(got, client) {
		t.Fatal("explicit selector input changed or saved source borrowed")
	}
	if got := d.NativeConfigurationEnvironment([]string{}); got == nil || len(got) != 0 {
		t.Fatal("explicit empty input borrowed daemon source")
	}
	d.savedEnv = nil
	if got := d.NativeConfigurationEnvironment(nil); !reflect.DeepEqual(got, live) {
		t.Fatal("absent saved origin did not use live daemon")
	}
	d.savedEnv = []string{}
	if got := d.NativeConfigurationEnvironment(nil); !reflect.DeepEqual(got, live) {
		t.Fatal("empty saved origin did not use live daemon")
	}
}

func TestLaunchNativeConfigurationSourceSurvivesSavedEnvironmentRestart(t *testing.T) {
	original := daemonEnviron
	t.Cleanup(func() { daemonEnviron = original })
	startup := []string{"PATH=" + os.Getenv("PATH"), "HOME=/synthetic-owner", "CODEX_HOME=/synthetic-owner/custom-codex", "CLAUDE_CONFIG_DIR=/synthetic-owner/custom-claude", "CLAUDE_CODE_NO_MODEL_FALLBACK=false", "CLAUDE_CODE_REMOTE_SETTINGS_PATH=0", "AWS_SECRET_ACCESS_KEY=synthetic-not-saved"}
	daemonEnviron = func() []string { return startup }
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadSavedEnv(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(saved, "\n"), "synthetic-not-saved") {
		t.Fatal("unrelated secret persisted with source selectors")
	}
	if !reflect.DeepEqual(NativeConfigurationEnvironment(saved), NativeConfigurationEnvironment(startup)) {
		t.Fatal("saved environment stripped original native origin")
	}
	// Unattended restart is started with the saved environment; later process
	// changes must not replace the daemon's captured launch origin.
	daemonEnviron = func() []string { return saved }
	cfg.FinalizeLaunch = func(_ string, spec LaunchSpec) (LaunchSpec, error) {
		if !reflect.DeepEqual(spec.AccountOriginalConfigurationEnv, NativeConfigurationEnvironment(startup)) {
			t.Error("assembled nil-client launch lost saved native source")
		}
		if len(NativeConfigurationEnvironment(spec.ClientEnv)) != 0 {
			t.Error("ordinary client policy was widened")
		}
		return spec, nil
	}
	d = openDaemon(t, cfg)
	daemonEnviron = func() []string { return []string{"CODEX_HOME=/wrong-live-owner"} }
	file := filepath.Join(t.TempDir(), "remote-child-env.txt")
	m, err := d.Launch(LaunchSpec{AgentType: "fake", Argv: []string{selfExe(t), markerEnvDump, file}, Cwd: t.TempDir(), ClientEnv: nil, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Kill(m.ID) })
	waitFile(t, file, pollTimeout)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if envValue(string(raw), "HOME") != "/synthetic-owner" || len(NativeConfigurationEnvironment(strings.Split(string(raw), "\n"))) != 0 {
		t.Fatal("saved ordinary origin changed or native credential selectors reached child")
	}
	metaRaw, err := os.ReadFile(filepath.Join(cfg.StateDir, m.ID, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metaRaw), "custom-codex") || strings.Contains(string(metaRaw), "custom-claude") || strings.Contains(string(metaRaw), "AccountOriginalConfigurationEnv") {
		t.Fatal("ephemeral captured source was persisted as child environment")
	}
}
