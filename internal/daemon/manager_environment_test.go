package daemon

import (
	"strings"
	"testing"
)

func TestManagerEnvironmentRetainsRefusalWithoutCredentialValues(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		env := []string{"HOME=/native/home", "OPENAI_API_KEY=synthetic-secret", "ANTHROPIC_API_KEY=synthetic-secret", "CLAUDE_CODE_USE_VERTEX=true"}
		safe := ManagerLaunchEnvironment(agent, env)
		if strings.Contains(strings.Join(safe, "\n"), "synthetic-secret") || ManagerNativeEnvironmentSupported(agent, safe) {
			t.Fatalf("manager lost refusal or kept credentials: %v", safe)
		}
		if ManagerNativeEnvironmentSupported(agent, []string{strings.TrimSuffix(managerOriginMarker(agent), "=1")}) {
			t.Fatal("malformed refusal marker cleared authority guard")
		}
	}
	if !ManagerNativeEnvironmentSupported("claude", []string{"CLAUDE_CODE_USE_VERTEX=false", "CLAUDE_CODE_USE_BEDROCK=0"}) {
		t.Fatal("disabled selectors refused")
	}
	if !strings.Contains(strings.Join(OwnerLaunchEnvironment("claude", []string{"ANTHROPIC_API_KEY=worker-key"}), "\n"), "worker-key") {
		t.Fatal("ordinary worker environment changed")
	}
}

func TestManagerSavedEnvironmentRetainsFilteredUnsupportedOrigin(t *testing.T) {
	original := daemonEnviron
	daemonEnviron = func() []string { return []string{"HOME=/native/home", "ANTHROPIC_AUTH_TOKEN=synthetic-secret"} }
	t.Cleanup(func() { daemonEnviron = original })
	dir := t.TempDir()
	if err := writeSavedEnv(dir); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadSavedEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(saved, "\n"), "synthetic-secret") || ManagerNativeEnvironmentSupported("claude", saved) {
		t.Fatal("daemon policy forgot unsupported native account origin")
	}
	if !ManagerNativeEnvironmentSupported("codex", saved) {
		t.Fatal("another provider's native account was blocked")
	}
}
