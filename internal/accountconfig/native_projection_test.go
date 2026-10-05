package accountconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNativeProjectionFreezesCustomOriginThroughScrubAndRotation(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			f := fixture(t, provider)
			custom := filepath.Join(filepath.Dir(f.source), "custom-profile")
			if err := os.Rename(f.source, custom); err != nil {
				t.Fatal(err)
			}
			selector := "CLAUDE_CONFIG_DIR"
			if provider == "codex" {
				selector = "CODEX_HOME"
				put(t, filepath.Join(custom, "config.toml"), "model_reasoning_effort=\"high\"\n")
			} else {
				put(t, filepath.Join(custom, "settings.json"), `{"effortLevel":"high"}`)
			}
			env := appendEnvOrigin(f.env, selector, custom)
			argv := []string{provider, "--model", "exact-model"}
			p, err := PrepareNative(f.root, provider, f.candidate, f.cwd, env, argv, "", "", codexContextLease, true)
			if err != nil {
				t.Fatal(err)
			}
			q, err := PrepareNative(f.root, provider, f.candidate, f.cwd, p.HarmlessEnv, argv, p.Ref, "exact-model", codexContextLease, false)
			if err != nil || q.Ref != p.Ref || q.Generation != p.Generation {
				t.Fatalf("scrubbed-env resume lost original native context: %v", err)
			}
			next := filepath.Join(filepath.Dir(f.candidate), "next-account")
			if err := os.Mkdir(next, 0700); err != nil {
				t.Fatal(err)
			}
			r, err := PrepareNative(f.root, provider, next, f.cwd, p.HarmlessEnv, argv, p.Ref, "exact-model", codexContextLease, false)
			if err != nil || r.Ref != p.Ref {
				t.Fatalf("rotation changed immutable context: %v", err)
			}
			path, err := NativeHistoryAuthority(f.root, p.Ref, provider, f.cwd, p.Generation)
			if err != nil || path != custom {
				t.Fatalf("wrong shared history authority: %v", err)
			}
			for _, invalid := range []struct {
				provider, cwd string
				generation    uint64
			}{{"other", f.cwd, p.Generation}, {provider, f.cwd + "/../project", p.Generation}, {provider, f.cwd, p.Generation + 1}} {
				if _, err := NativeHistoryAuthority(f.root, p.Ref, invalid.provider, invalid.cwd, invalid.generation); err == nil {
					t.Fatal("cross-context history authority admitted")
				}
			}
		})
	}
}

func TestNativeProjectionRejectsIncompleteOrCrossProviderProvenance(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			f := fixture(t, provider)
			p, err := PrepareNative(f.root, provider, f.candidate, f.cwd, f.env, []string{provider}, "", "", codexContextLease, true)
			if err != nil {
				t.Fatal(err)
			}
			m, err := readManifest(f.root, p.Ref)
			if err != nil {
				t.Fatal(err)
			}
			for _, mutate := range []func(*manifest){
				func(m *manifest) { m.CodexProjectSources = nil; m.ClaudeProjectSources = nil },
				func(m *manifest) {
					if provider == "codex" {
						m.ClaudeProjectSources = []source{{Path: "/other", SHA256: "absent"}}
					} else {
						m.CodexProjectSources = []source{{Path: "/other", SHA256: "absent"}}
					}
				},
				func(m *manifest) {
					if provider == "claude" {
						h := *m.ClaudeHistoryAlias
						h.ProjectKey = "unrelated"
						h.SourcePath = filepath.Join(m.ClaudeContext.SourceProfile.Canonical, "projects", h.ProjectKey)
						m.ClaudeHistoryAlias = &h
					} else {
						m.CodexHistoryAlias = nil
					}
				},
			} {
				bad := m
				mutate(&bad)
				raw, _ := json.Marshal(bad)
				if _, err := ProjectionCompatibility(raw); err == nil {
					t.Fatal("incomplete context passed intrinsic validation")
				}
			}
			// A newly added auth selector must hold even if the initial layer was absent.
			path, content := filepath.Join(f.cwd, ".codex", "config.toml"), "model_provider=\"custom\"\n"
			if provider == "claude" {
				path, content = filepath.Join(f.cwd, ".claude", "settings.json"), `{"env":{"ANTHROPIC_API_KEY":"synthetic"}}`
			}
			put(t, path, content)
			if err := Revalidate(f.root, p.Ref, provider, f.candidate, f.cwd); err == nil {
				t.Fatal("late project auth selector admitted")
			}
		})
	}
}

func TestNetworkOverrideRemovalPreservesExplicitPolicy(t *testing.T) {
	args := []string{"codex", "-c", "sandbox_workspace_write.network_access=true", "--sandbox", "workspace-write", "-c", "sandbox_workspace_write.network_access=false", "--model", "exact-model"}
	want := []string{"codex", "--sandbox", "workspace-write", "-c", "sandbox_workspace_write.network_access=false", "--model", "exact-model"}
	if got := WithoutSynthesizedNetworkOverride(args); !reflect.DeepEqual(got, want) {
		t.Fatal("unrelated explicit policy was removed")
	}
}

func TestNativeProjectionRejectsUnrepresentedClaudeSelectorsBeforeWrites(t *testing.T) {
	for _, entry := range []string{
		"CLAUDE_CODE_USE_ANTHROPIC_AWS=true", "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD=true", "CLAUDE_CODE_USE_MANTLE=true",
		"CLAUDE_CODE_REMOTE_SETTINGS_PATH=/owner/policy", "CLAUDE_CODE_REMOTE_SETTINGS_PATH=false", "CLAUDE_CODE_REMOTE_SETTINGS_PATH=0",
		"CLAUDE_CODE_MANAGED_SETTINGS_PATH=/owner/policy", "CLAUDE_CODE_MANAGED_SETTINGS_PATH=false", "CLAUDE_CODE_MANAGED_SETTINGS_PATH=0",
		"XDG_CONFIG_HOME=/owner/xdg", "XDG_CONFIG_HOME=false", "XDG_CONFIG_HOME=0",
	} {
		t.Run(entry, func(t *testing.T) {
			f := fixture(t, "claude")
			before, err := os.ReadDir(f.candidate)
			if err != nil {
				t.Fatal(err)
			}
			_, err = PrepareNative(f.root, "claude", f.candidate, f.cwd, append(f.env, entry), []string{"claude"}, "", "", codexContextLease, true)
			if err == nil {
				t.Fatal("unrepresented configuration selector admitted")
			}
			after, err := os.ReadDir(f.candidate)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("refused admission changed candidate profile")
			}
		})
	}
}
