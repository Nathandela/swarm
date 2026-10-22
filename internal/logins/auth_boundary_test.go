package logins

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectAuthFindsAncestorAndRefusesSymlink(t *testing.T) {
	for _, provider := range []string{ProviderClaude, ProviderCodex} {
		t.Run(provider, func(t *testing.T) {
			root, e := filepath.EvalSymlinks(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			cwd := filepath.Join(root, "src", "nested")
			if e = os.MkdirAll(cwd, 0700); e != nil {
				t.Fatal(e)
			}
			dir, name, body := ".claude", "settings.json", `{"env":{"CLAUDE_SECURESTORAGE_CONFIG_DIR":"/other"}}`
			if provider == ProviderCodex {
				dir, name, body = ".codex", "config.toml", `[profiles.external]
cli_auth_credentials_store = "keyring"`
			}
			if e = os.Mkdir(filepath.Join(root, dir), 0700); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(root, dir, name)
			if e = os.WriteFile(path, []byte(body), 0644); e != nil {
				t.Fatal(e)
			}
			if e = ValidateProjectAuth(provider, cwd); !errors.Is(e, ErrIneligible) {
				t.Fatalf("ancestor auth override accepted: %v", e)
			}
			if e = os.Remove(path); e != nil {
				t.Fatal(e)
			}
			target := filepath.Join(root, "config")
			if e = os.WriteFile(target, []byte(body), 0644); e != nil {
				t.Fatal(e)
			}
			if e = os.Symlink(target, path); e != nil {
				t.Fatal(e)
			}
			if e = ValidateProjectAuth(provider, cwd); !errors.Is(e, ErrUnsafePath) {
				t.Fatalf("symlink config accepted: %v", e)
			}
		})
	}
}

func TestNativeArgumentsKeepHouseOptionsButRejectAlternateAuth(t *testing.T) {
	cases := []struct {
		provider string
		argv     []string
		bad      bool
	}{
		{ProviderCodex, []string{"codex", "resume", "native-id", "-c", "sandbox_workspace_write.network_access=true", "-c", "check_for_update_on_startup=false", "--model", "native-model"}, false},
		{ProviderCodex, []string{"codex", "-c", "sandbox_mode=read-only", "-c", "model=gpt-6-luna", "app-server"}, false},
		{ProviderCodex, []string{"codex", "-c", "model_reasoning_effort=low", "-c", "cli_auth_credentials_store=file"}, false},
		{ProviderCodex, []string{"codex", "-c", "cli_auth_credentials_store=keyring"}, true},
		{ProviderCodex, []string{"codex", "-c", "model_provider=external"}, true},
		{ProviderCodex, []string{"codex", "-c", "chatgpt_base_url=https://external.invalid"}, true},
		{ProviderCodex, []string{"codex", "-c", "model_providers={openai={base_url='https://external.invalid'}}"}, true},
		{ProviderCodex, []string{"codex", "-c", "=read-only"}, true},
		{ProviderCodex, []string{"codex", "--config=cli_auth_credentials_store='keyring'"}, true},
		{ProviderCodex, []string{"codex", "-c", "model_provider='external'"}, true},
		{ProviderCodex, []string{"codex", "--profile=external"}, true},
		{ProviderCodex, []string{"codex", "-pexternal"}, true},
		{ProviderCodex, []string{"codex", "-c", "model_providers.openai.base_url='https://external.invalid'"}, true},
		{ProviderCodex, []string{"codex", "--", "--profile=just-prompt-text"}, false},
		{ProviderClaude, []string{"claude", "--settings", `{"hooks":{"Stop":[]},"env":{"UNRELATED_TOOL_SETTING":"keep"}}`, "--model", "native-model"}, false},
		{ProviderClaude, []string{"claude", "--settings", `{"apiKeyHelper":"external"}`}, true},
		{ProviderClaude, []string{"claude", "--settings", `{"env":{"ANTHROPIC_AUTH_TOKEN":"external"}}`}, true},
		{ProviderClaude, []string{"claude", "--settings", `{"env":{},"env":{}}`}, true},
	}
	for _, tc := range cases {
		e := ValidateArgsAuth(tc.provider, tc.argv)
		if (e != nil) != tc.bad {
			t.Fatalf("%v bad=%v got %v", tc.argv, tc.bad, e)
		}
	}
}
