package accountconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/codex"
)

type projectionFixture struct {
	root, home, source, candidate, cwd string
	env                                []string
}

func fixture(t *testing.T, provider string) projectionFixture {
	t.Helper()
	base := t.TempDir()
	f := projectionFixture{root: filepath.Join(base, "state"), home: filepath.Join(base, "home"), source: filepath.Join(base, "home", "."+provider), candidate: filepath.Join(base, "candidate"), cwd: filepath.Join(base, "project")}
	for _, dir := range []string{f.root, f.home, f.source, f.candidate, f.cwd} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.env = []string{"HOME=" + f.home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + filepath.Join(f.home, ".gitconfig"), "ANTHROPIC_API_KEY=synthetic-provider-key"}
	return f
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexProjection_PreservesModelPolicyForBothProcessesAndRotation(t *testing.T) {
	f := fixture(t, "codex")
	put(t, filepath.Join(f.source, "config.toml"), "model = \"gpt-original\"\nmodel_reasoning_effort = \"high\"\n[sandbox_workspace_write]\nnetwork_access = true\n")
	argv := []string{"codex", "--model", "gpt-requested", "--sandbox", "workspace-write", "prompt must remain current"}
	p, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, argv, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Generation == 0 || len(p.Ref) != 64 || p.ProviderCwd != f.cwd {
		t.Fatal("projection identity/context missing")
	}
	for _, want := range []string{`model="gpt-requested"`, `model_reasoning_effort="high"`, `sandbox_mode="workspace-write"`, `sandbox_workspace_write.network_access=true`, `cli_auth_credentials_store="file"`} {
		if !contains(p.CLIArgs, want) || !contains(p.BackendArgs, want) {
			t.Fatalf("CLI/backend missing %s", want)
		}
	}
	if !contains(p.HarmlessEnv, "HOME="+f.home) || !contains(p.HarmlessEnv, "GIT_CONFIG_GLOBAL="+filepath.Join(f.home, ".gitconfig")) {
		t.Fatal("ordinary HOME/tool environment lost")
	}
	for _, entry := range p.HarmlessEnv {
		if strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") {
			t.Fatal("ambient provider key persisted")
		}
	}
	second := filepath.Join(filepath.Dir(f.candidate), "candidate-b")
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(second, "config.toml"), `model = "gpt-b-default"`)
	before, err := os.ReadFile(filepath.Join(second, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	resume, err := prepareTestProjection(f.root, "codex", second, f.cwd, f.env, []string{"codex", "resume", "thread-id", "current retry"}, p.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if resume.Ref != p.Ref || resume.Generation != p.Generation || !contains(resume.CLIArgs, "current retry") || contains(resume.CLIArgs, "prompt must remain current") {
		t.Fatal("rotation changed projection or replayed old prompt")
	}
	if resume.CLIArgs[1] != "resume" || resume.CLIArgs[2] != "thread-id" {
		t.Fatal("projection hid resume from the existing backend adapter")
	}
	after, _ := os.ReadFile(filepath.Join(second, "config.toml"))
	if string(after) != string(before) {
		t.Fatal("destination account config was rewritten")
	}
	raw, _ := os.ReadFile(filepath.Join(f.root, "accounts", "configurations", p.Ref, "projection.json"))
	for _, forbidden := range []string{"synthetic-provider-key", "prompt must remain current", "current retry"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("secret/task content entered projection metadata")
		}
	}
}

func TestCodexProjection_ActualAdapterResumePreservesRefAcrossAccounts(t *testing.T) {
	f := fixture(t, "codex")
	put(t, filepath.Join(f.source, "config.toml"), "check_for_update_on_startup = true\n")
	cli := codex.New()
	options := map[string]string{"model": "gpt-test", "sandbox": "workspace-write"}
	fresh, err := cli.Command(adapter.LaunchSpec{Options: options})
	if err != nil {
		t.Fatal(err)
	}
	p, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, fresh, "")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := cli.Resume(adapter.ResumeSpec{ConversationID: "019a0000-0000-7000-8000-000000000000", Options: options})
	if err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(filepath.Dir(f.candidate), "candidate-b")
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Fatal(err)
	}
	next, err := prepareTestProjection(f.root, "codex", second, f.cwd, f.env, resumed, p.Ref)
	if err != nil {
		t.Fatalf("actual adapter A to B resume refused: %v", err)
	}
	if next.Ref != p.Ref || next.Generation != p.Generation || next.CLIArgs[1] != "resume" {
		t.Fatal("resume changed the immutable configuration or command form")
	}
	if next.BackendArgs[len(next.BackendArgs)-1] != "check_for_update_on_startup=false" || next.CLIArgs[len(next.CLIArgs)-1] != "check_for_update_on_startup=false" {
		t.Fatal("invocation-only no-update policy lost to frozen defaults")
	}
}

func TestCodexProjection_AuthenticatedInvocationModelPreservesFrozenPolicy(t *testing.T) {
	f := fixture(t, "codex")
	put(t, filepath.Join(f.source, "config.toml"), "model = \"gpt-first-model\"\nmodel_reasoning_effort = \"high\"\n")
	cli := codex.New()
	fresh, err := cli.Command(adapter.LaunchSpec{Options: map[string]string{"model": "gpt-first-model", "sandbox": "workspace-write"}})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, fresh, "")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(f.root, "accounts", "configurations", frozen.Ref, "projection.json")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := cli.Resume(adapter.ResumeSpec{ConversationID: "019a0000-0000-7000-8000-000000000000", Options: map[string]string{"model": "gpt-current-model", "sandbox": "workspace-write"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, resumed, frozen.Ref); err == nil {
		t.Fatal("ordinary argv changed the frozen model without native authority")
	}
	next, err := PrepareWithModel(f.root, "codex", f.candidate, f.cwd, f.env, resumed, frozen.Ref, "gpt-current-model")
	if err != nil {
		t.Fatal(err)
	}
	if next.Ref != frozen.Ref || next.Generation != frozen.Generation {
		t.Fatal("invocation model changed the frozen configuration identity")
	}
	for _, argv := range [][]string{next.CLIArgs, next.BackendArgs} {
		for _, want := range []string{`model="gpt-current-model"`, `model_reasoning_effort="high"`, `sandbox_mode="workspace-write"`, `cli_auth_credentials_store="file"`} {
			if !contains(argv, want) {
				t.Fatalf("prepared invocation missing %q: %v", want, argv)
			}
		}
		if contains(argv, `model="gpt-first-model"`) {
			t.Fatal("frozen model would override the native invocation model")
		}
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("authorized invocation rewrote the frozen manifest")
	}
	for _, tc := range []struct {
		name, proof string
		argv        []string
		ref         string
	}{
		{"mismatched-proof", "gpt-other-model", resumed, frozen.Ref},
		{"missing-model-argument", "gpt-current-model", []string{"codex", "resume", "thread-id"}, frozen.Ref},
		{"alias-proof", "sonnet[1m]", []string{"codex", "resume", "thread-id", "--model", "sonnet[1m]"}, frozen.Ref},
		{"fresh-unfrozen-proof", "gpt-current-model", resumed, ""},
		{"conflicting-model-setting", "gpt-current-model", append(append([]string(nil), resumed...), "-c", `model="gpt-other-model"`), frozen.Ref},
		{"non-model-policy-drift", "gpt-current-model", append(append([]string(nil), resumed...), "-c", `model_reasoning_effort="low"`), frozen.Ref},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PrepareWithModel(f.root, "codex", f.candidate, f.cwd, f.env, tc.argv, tc.ref, tc.proof); err == nil {
				t.Fatal("invalid invocation authority or policy drift was accepted")
			}
		})
	}
}

func TestProjection_SourceProfileAliasIsFrozenAndBoundProfilesStayStrict(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			f := fixture(t, provider)
			canonical := filepath.Join(f.home, "runtime", provider)
			if err := os.MkdirAll(canonical, 0o700); err != nil {
				t.Fatal(err)
			}
			if provider == "codex" {
				put(t, filepath.Join(canonical, "config.toml"), `model = "gpt-test"`)
			} else {
				put(t, filepath.Join(canonical, "settings.json"), `{"model":"sonnet"}`)
			}
			if err := os.Remove(f.source); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(canonical, f.source); err != nil {
				t.Fatal(err)
			}
			p, err := prepareTestProjection(f.root, provider, f.candidate, f.cwd, f.env, []string{provider}, "")
			if err != nil {
				t.Fatalf("explicit ambient profile alias refused: %v", err)
			}
			if err := Revalidate(f.root, p.Ref, provider, f.candidate, f.cwd); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(f.home, "different-profile")
			if err := os.Mkdir(other, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(f.source); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other, f.source); err != nil {
				t.Fatal(err)
			}
			if err := Revalidate(f.root, p.Ref, provider, f.candidate, f.cwd); err == nil || !strings.Contains(err.Error(), "source-profile-alias-changed") {
				t.Fatal("retargeted source alias not rejected")
			}
			boundAlias := filepath.Join(f.home, "bound-alias")
			if err := os.Symlink(f.candidate, boundAlias); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareTestProjection(f.root, provider, boundAlias, f.cwd, f.env, []string{provider}, ""); err == nil {
				t.Fatal("source alias support weakened private bound profile validation")
			}
		})
	}
}

func TestClaudeProjection_AlternateOAuthStoreCannotOverridePrivateAccount(t *testing.T) {
	for _, xdg := range []bool{false, true} {
		t.Run(fmt.Sprintf("xdg-%t", xdg), func(t *testing.T) {
			f := fixture(t, "claude")
			store := filepath.Join(f.home, ".config", "anthropic")
			if xdg {
				base := filepath.Join(f.home, "xdg")
				f.env = append(f.env, "XDG_CONFIG_HOME="+base)
				store = filepath.Join(base, "anthropic")
			}
			projection, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "")
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(store, "credentials", "default.json"), "synthetic bearer must remain unread")
			if err := Revalidate(f.root, projection.Ref, "claude", f.candidate, f.cwd); err == nil {
				t.Fatal("ambient OAuth store appearing after freeze was accepted")
			}
			if _, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, ""); err == nil || !strings.Contains(err.Error(), "alternate-native-profile-store") || strings.Contains(err.Error(), "synthetic bearer") {
				t.Fatal("ambient OAuth store could override selected private account or exposed credentials")
			}
		})
	}
}

func TestClaudeProjection_RejectsRemotePolicyAtEitherProfileAndAfterFreeze(t *testing.T) {
	for _, name := range []string{"remote-settings.json", "remote-settings-consent.json", "remote-settings-helper-consent", "managed-settings.json", "managed-settings.d/20-hooks.json", "managed-mcp.json"} {
		for _, source := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-source-%t", name, source), func(t *testing.T) {
				f := fixture(t, "claude")
				profile := f.candidate
				if source {
					profile = f.source
				}
				p, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "")
				if err != nil {
					t.Fatal(err)
				}
				put(t, filepath.Join(profile, name), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"synthetic-must-not-run"}]}]}}`)
				if err := Revalidate(f.root, p.Ref, "claude", f.candidate, f.cwd); err == nil {
					t.Fatal("policy arriving after projection freeze was accepted")
				}
				for _, prior := range []string{"", p.Ref} {
					if _, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, prior); err == nil || strings.Contains(err.Error(), "synthetic-must-not-run") {
						t.Fatal("native policy omitted from source/destination or resume proof")
					}
				}
			})
		}
	}
}

func TestClaudeProjection_ManagedDropinDirectoryIsInventoried(t *testing.T) {
	f := fixture(t, "claude")
	p, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(f.root, p.Ref)
	if err != nil {
		t.Fatal(err)
	}
	recorded := false
	for _, source := range m.Sources {
		if source.Path == "/etc/claude-code/managed-settings.d" && source.SHA256 == "absent" {
			recorded = true
		}
	}
	if !recorded {
		t.Fatal("normal projection did not freeze the system policy drop-in directory absence")
	}
	policy := filepath.Join(f.root, "synthetic-system-policy")
	var absent manifest
	if err := collectClaudePolicies(&absent, policy, "managed-policy-not-characterized"); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(policy, "managed-settings.d", "20-hooks.json"), `{"hooks":{}}`)
	if err := validateSources(absent.Sources); err == nil {
		t.Fatal("policy drop-in appearance did not invalidate frozen absence")
	}
	if err := collectClaudePolicies(&manifest{}, policy, "managed-policy-not-characterized"); err == nil {
		t.Fatal("present drop-in policy was accepted")
	}
}

func TestClaudeProjection_ComposesPermissionsAndSwarmHooksInPrivateFile(t *testing.T) {
	f := fixture(t, "claude")
	put(t, filepath.Join(f.source, "settings.json"), `{"model":"sonnet","permissions":{"allow":["Read(*)"],"deny":["Bash(rm:*)"]}}`)
	hooks := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"swarm hook Stop"}]}]},"permissions":{"allow":["Read(*)","Bash(git:*)"]}}`
	argv := []string{"claude", "--settings", hooks, "--model", "opus", "current task"}
	p, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, argv, "")
	if err != nil {
		t.Fatal(err)
	}
	values := flagValues(p.CLIArgs, "--settings")
	if len(values) != 1 || len(flagValues(p.CLIArgs, "--setting-sources")) != 1 || !contains(p.CLIArgs, "current task") || !contains(p.CLIArgs, "opus") {
		t.Fatal("Claude settings composition lost launch flags")
	}
	raw, err := os.ReadFile(values[0])
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if json.Unmarshal(raw, &settings) != nil {
		t.Fatal("invalid projection settings")
	}
	if !swarmHooks(settings["hooks"]) || settings["model"] != "sonnet" {
		t.Fatal("projection dropped source model or Swarm hooks")
	}
	permissions := settings["permissions"].(map[string]any)
	if !reflect.DeepEqual(permissions["allow"], []any{"Read(*)", "Bash(git:*)"}) || !reflect.DeepEqual(permissions["deny"], []any{"Bash(rm:*)"}) {
		t.Fatal("permission lists lost entries or duplicated them")
	}
	info, _ := os.Stat(values[0])
	if info.Mode().Perm() != 0o600 {
		t.Fatal("projected settings are not private")
	}
	if err := Revalidate(f.root, p.Ref, "claude", f.candidate, f.cwd); err != nil {
		t.Fatal(err)
	}
	put(t, values[0], `{"model":"changed"}`)
	if err := Revalidate(f.root, p.Ref, "claude", f.candidate, f.cwd); err == nil {
		t.Fatal("altered settings projection admitted")
	}
}

func TestClaudeProjection_PreservesCaptureAndManagedObserverHooksOnRotation(t *testing.T) {
	f := fixture(t, "claude")
	argv, err := claude.New().Command(adapter.LaunchSpec{InitialPrompt: "synthetic current task"})
	if err != nil {
		t.Fatal(err)
	}
	inline := flagValues(argv, "--settings")
	if len(inline) != 1 {
		t.Fatal("native adapter lost its capture hook injection")
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(inline[0]), &settings); err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]any)
	for _, event := range []string{"SessionStart", "StopFailure"} {
		hooks[event] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "swarm hook " + event}}}}
	}
	raw, _ := json.Marshal(settings)
	for i := range argv {
		if argv[i] == inline[0] {
			argv[i] = string(raw)
		}
	}
	first, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, argv, "")
	if err != nil {
		t.Fatal(err)
	}
	secondProfile := filepath.Join(f.home, "second-account")
	if err := os.Mkdir(secondProfile, 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := prepareTestProjection(f.root, "claude", secondProfile, f.cwd, f.env, argv, first.Ref)
	if err != nil || first.Ref != second.Ref {
		t.Fatalf("rotation changed frozen observer/capture settings: %v", err)
	}
	raw, err = os.ReadFile(flagValues(second.CLIArgs, "--settings")[0])
	if err != nil || json.Unmarshal(raw, &settings) != nil || !reflect.DeepEqual(settings["hooks"], hooks) {
		t.Fatal("rotation lost capture or managed authentication observer hooks")
	}
	for _, invalid := range []string{
		`{"Stop":[{"hooks":[{"type":"command","command":"swarm hook SessionStart"}]}]}`,
		`{"Unknown":[{"hooks":[{"type":"command","command":"swarm hook Unknown"}]}]}`,
	} {
		var value any
		if json.Unmarshal([]byte(invalid), &value) != nil || swarmHooks(value) {
			t.Fatal("unrecognized or mismatched observer command admitted")
		}
	}
}

func TestProjection_RefusesChangedOrUnrepresentableConfiguration(t *testing.T) {
	for _, tc := range []struct{ provider, name, content, reason string }{
		{"codex", "config.toml", `model_provider = "custom"`, "credential-or-provider-settings"},
		{"codex", "config.toml", "[mcp_servers.custom]\ncommand = \"relative-helper\"", "unsupported-codex-table"},
		{"codex", "config.toml", `cli_auth_credentials_store = "keyring"`, "credential-store-policy"},
		{"claude", "settings.json", `{"apiKeyHelper":"print-secret"}`, "credential-or-provider-settings"},
		{"claude", "settings.json", `{"env":{"ANTHROPIC_AUTH_TOKEN":"synthetic"}}`, "credential-or-provider-settings"},
		{"claude", "settings.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"user-helper"}]}]}}`, "executable-hooks-not-characterized"},
	} {
		t.Run(tc.provider+"-"+tc.reason, func(t *testing.T) {
			f := fixture(t, tc.provider)
			put(t, filepath.Join(f.source, tc.name), tc.content)
			_, err := prepareTestProjection(f.root, tc.provider, f.candidate, f.cwd, f.env, []string{tc.provider}, "")
			var conflict Conflict
			if !errors.As(err, &conflict) || !strings.Contains(err.Error(), tc.reason) || strings.Contains(err.Error(), tc.content) || strings.Contains(err.Error(), f.home) {
				t.Fatal("unsafe configuration lacked a specific redacted refusal")
			}
		})
	}
	f := fixture(t, "codex")
	put(t, filepath.Join(f.source, "config.toml"), `model = "gpt-a"`)
	p, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, "")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.source, "config.toml"), `model = "gpt-changed"`)
	if err := Revalidate(f.root, p.Ref, "codex", f.candidate, f.cwd); err == nil {
		t.Fatal("changed source admitted before spawn")
	}
}

func TestProjection_NativeCustomizationsRequireCompatibleCohort(t *testing.T) {
	f := fixture(t, "codex")
	put(t, filepath.Join(f.source, "skills", "example", "SKILL.md"), "Synthetic skill: keep the requested model.")
	if _, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, ""); err == nil {
		t.Fatal("missing destination skill silently dropped")
	}
	put(t, filepath.Join(f.candidate, "skills", "example", "SKILL.md"), "Synthetic skill: keep the requested model.")
	p, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, "")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.candidate, "skills", "example", "SKILL.md"), "Changed synthetic skill.")
	if err := Revalidate(f.root, p.Ref, "codex", f.candidate, f.cwd); err == nil {
		t.Fatal("changed destination customization admitted")
	}
}

func TestClaudeProjection_TrustAndMCPCohortIgnoreAccountIdentity(t *testing.T) {
	f := fixture(t, "claude")
	trust := map[string]any{"projects": map[string]any{f.cwd: map[string]any{"hasTrustDialogAccepted": true, "allowedTools": []string{"Read"}}}, "mcpServers": map[string]any{"example": map[string]any{"command": "synthetic-mcp"}}, "oauthAccount": map[string]string{"emailAddress": "first@example.test"}}
	raw, _ := json.Marshal(trust)
	put(t, filepath.Join(f.home, ".claude.json"), string(raw))
	if _, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, ""); err == nil {
		t.Fatal("missing destination trust/MCP silently changed configuration")
	}
	trust["oauthAccount"] = map[string]string{"emailAddress": "second@example.test"}
	raw, _ = json.Marshal(trust)
	put(t, filepath.Join(f.candidate, ".claude.json"), string(raw))
	p, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "")
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, _ := os.ReadFile(filepath.Join(f.root, "accounts", "configurations", p.Ref, "projection.json"))
	if strings.Contains(string(manifestRaw), "example.test") || strings.Contains(string(manifestRaw), "synthetic-mcp") {
		t.Fatal("native identity or executable MCP configuration copied into projection")
	}
	trust["projects"] = map[string]any{f.cwd: map[string]any{"hasTrustDialogAccepted": false}}
	raw, _ = json.Marshal(trust)
	put(t, filepath.Join(f.candidate, ".claude.json"), string(raw))
	if err := Revalidate(f.root, p.Ref, "claude", f.candidate, f.cwd); err == nil {
		t.Fatal("changed native trust decisions admitted before spawn")
	}
}

func TestProjection_NewProjectSourceAndCandidateDefaultsRefuseBeforeSpawn(t *testing.T) {
	f := fixture(t, "codex")
	p, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, "")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.cwd, ".codex", "config.toml"), `model = "project-model"`)
	if err := Revalidate(f.root, p.Ref, "codex", f.candidate, f.cwd); err == nil {
		t.Fatal("new mutable project source missed by absent-source hashes")
	}
	f = fixture(t, "codex")
	put(t, filepath.Join(f.candidate, "config.toml"), "model_context_window = 32000")
	if _, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, ""); err == nil {
		t.Fatal("unprojected candidate default changed effective settings")
	}
}

func TestProjection_SymlinksAndSelectorsFailClosed(t *testing.T) {
	f := fixture(t, "claude")
	outside := filepath.Join(f.home, "other.json")
	put(t, outside, `{"model":"sonnet"}`)
	if err := os.Symlink(outside, filepath.Join(f.source, "settings.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, ""); err == nil {
		t.Fatal("symlinked settings source accepted")
	}
	for _, selector := range []string{"CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_FEDERATION_PROFILE=work", "CODEX_PROFILE=named"} {
		f := fixture(t, "codex")
		if _, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, append(f.env, selector), []string{"codex"}, ""); err == nil {
			t.Fatal("uncharacterized selector accepted")
		}
	}
}

// Explicit opt-in runs installed binaries only with synthetic private homes and
// --help/--version. It authenticates nothing and makes no model requests. Help
// proves the option surface, not effective MCP/hooks/trust precedence.
func TestNativeHelpProjectionContract(t *testing.T) {
	if os.Getenv("SWARM_ACCOUNTS_NATIVE_HELP") != "1" {
		t.Skip("set SWARM_ACCOUNTS_NATIVE_HELP=1 for the no-auth installed-native help gate")
	}
	for _, tc := range []struct {
		binary, version string
		args, options   []string
	}{
		{"codex", "0.160.0", []string{"--help"}, []string{"--config", "--profile"}},
		{"codex", "0.160.0", []string{"app-server", "--help"}, []string{"--config", "--strict-config"}},
		{"codex", "0.160.0", []string{"resume", "00000000-0000-4000-8000-000000000000", "synthetic prompt", "-c", `model="synthetic-model"`, "-c", `sandbox_mode="workspace-write"`, "--help"}, []string{"--config"}},
		{"claude", "2.1.288", []string{"--help"}, []string{"--settings", "--setting-sources", "--strict-mcp-config"}},
		{"claude", "2.1.288", []string{"--safe-mode", "--setting-sources", "", "--settings", "{}", "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--disable-slash-commands", "--no-session-persistence", "--output-format", "json", "--max-turns", "1", "--model", "synthetic-exact-model", "--system-prompt", "Reply OK. Use no tools.", "-p", "Reply OK.", "--help"}, []string{"--safe-mode", "--tools", "--disable-slash-commands", "--no-session-persistence"}},
	} {
		t.Run(tc.binary+strings.Join(tc.args, "-"), func(t *testing.T) {
			binary, err := exec.LookPath(tc.binary)
			if err != nil {
				t.Fatal(err)
			}
			private := t.TempDir()
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + private, "CODEX_HOME=" + filepath.Join(private, "codex"), "CLAUDE_CONFIG_DIR=" + filepath.Join(private, "claude")}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			version := exec.CommandContext(ctx, binary, "--version")
			version.Env = env
			v, err := version.Output()
			if err != nil || !strings.Contains(string(v), tc.version) {
				t.Fatal("installed native version differs from characterized fixture")
			}
			help := exec.CommandContext(ctx, binary, tc.args...)
			help.Env = env
			out, err := help.Output()
			if err != nil {
				t.Fatal("native help gate failed")
			}
			for _, option := range tc.options {
				if !strings.Contains(string(out), option) {
					t.Fatalf("native help missing %s", option)
				}
			}
		})
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Ordinary legacy projections have no authenticated recovery model override.
func prepareTestProjection(stateRoot, provider, profilePath, cwd string, env, argv []string, prior string) (Projection, error) {
	return PrepareWithModel(stateRoot, provider, profilePath, cwd, env, argv, prior, "")
}
