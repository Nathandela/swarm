package accounts

import (
	"reflect"
	"testing"
)

func TestCodexAmbientTokenAndRefreshEndpointsCannotOverrideBoundAuthority(t *testing.T) {
	env := []string{"HOME=/ordinary/home", "PATH=/bin", "CODEX_ACCESS_TOKEN=synthetic-other-account", "CODEX_REFRESH_TOKEN_URL_OVERRIDE=https://example.invalid/refresh", "CODEX_REVOKE_TOKEN_URL_OVERRIDE=https://example.invalid/revoke"}
	got, err := ScrubEnvironment(env)
	if err != nil || !reflect.DeepEqual(got, env[:2]) {
		t.Fatal("ambient token or credential endpoint survived managed environment resolution")
	}
}

func TestClaudeRoutingAndPolicySelectorsCannotOverrideBoundAuthority(t *testing.T) {
	for _, key := range []string{"CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD", "CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_REMOTE_SETTINGS_PATH", "CLAUDE_CODE_MANAGED_SETTINGS_PATH"} {
		for _, value := range []string{"true", "false", "0", "/owner/policy"} {
			env := []string{"HOME=/ordinary/home", "PATH=/bin", key + "=" + value}
			got, err := ScrubEnvironment(env)
			if err != nil || !reflect.DeepEqual(got, env[:2]) {
				t.Fatalf("selector %s survived final account resolution", key)
			}
		}
	}
}
