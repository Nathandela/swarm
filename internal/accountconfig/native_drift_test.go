package accountconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Native CLIs persist ordinary preferences (/fast, /model, /config, mcp add)
// into the managed account profile. Swarm accepts that drift and launches.

const driftOwnerClaudeSettings = `{"model":"opus","effortLevel":"high","permissions":{"allow":["Read"]}}`

func driftClaudeInstall(t *testing.T) (projectionFixture, Projection) {
	t.Helper()
	f := fixture(t, "claude")
	put(t, filepath.Join(f.source, "settings.json"), driftOwnerClaudeSettings)
	p, err := PrepareNative(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "", "", codexContextLease, true)
	if err != nil {
		t.Fatal("initial managed install:", err)
	}
	return f, p
}

func driftCodexInstall(t *testing.T) (projectionFixture, Projection) {
	t.Helper()
	f := codexContextFixture(t)
	p, err := PrepareNative(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, "", "", codexContextLease, true)
	if err != nil {
		t.Fatal("initial managed install:", err)
	}
	return f, p
}

// Claude Code re-serialises the profile settings.json with its own edits.
func driftClaudeWrite(t *testing.T, profile string, extra map[string]any) {
	t.Helper()
	path := filepath.Join(profile, "settings.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	for k, v := range extra {
		settings[k] = v
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, string(out)+"\n")
}

// Codex's /model picker rewrites CODEX_HOME/config.toml with toml_edit quoting.
func driftCodexModelWrite(t *testing.T, profile string) {
	t.Helper()
	path := filepath.Join(profile, "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "model") {
			lines = append(lines, line)
		}
	}
	put(t, path, "model = \"gpt-picked-in-tui\"\nmodel_reasoning_effort = \"xhigh\"\n"+strings.Join(lines, "\n"))
}

func TestPrepareNative_ClaudeProfileSettingsDriftStillLaunches(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"fast":   {"fastMode": true},
		"model":  {"model": "sonnet"},
		"config": {"spinnerTipsEnabled": false},
		"bypass": {"skipDangerousModePermissionPrompt": true},
	} {
		t.Run(name, func(t *testing.T) {
			f, p := driftClaudeInstall(t)
			driftClaudeWrite(t, f.candidate, extra)
			m, err := readManifest(f.root, p.Ref)
			if err != nil {
				t.Fatal(err)
			}
			if err := RevalidateClaudeContext(*m.ClaudeContext, f.candidate); err != nil {
				t.Fatal("running session revalidation refused native drift:", err)
			}
			if _, err := PrepareNative(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "", "", codexContextLease, true); err != nil {
				t.Fatal("new discussion refused native drift:", err)
			}
			raw, err := os.ReadFile(filepath.Join(f.candidate, "settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			for key := range extra {
				if !strings.Contains(string(raw), `"`+key+`"`) {
					t.Fatal("launch without owner change discarded the native edit of", key)
				}
			}
		})
	}
}

func TestPrepareNative_ClaudeLocalMcpAddStillLaunches(t *testing.T) {
	f, _ := driftClaudeInstall(t)
	put(t, filepath.Join(f.candidate, ".claude.json"), `{"projects":{"`+f.cwd+`":{"hasTrustDialogAccepted":true,"mcpServers":{"synthetic":{"command":"true"}}}}}`)
	if _, err := PrepareNative(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "", "", codexContextLease, true); err != nil {
		t.Fatal("new discussion refused a native mcp add:", err)
	}
}

func TestPrepareNative_ClaudeOwnerChangeAfterProfileDriftRefreshesProfile(t *testing.T) {
	f, _ := driftClaudeInstall(t)
	driftClaudeWrite(t, f.candidate, map[string]any{"fastMode": true})
	put(t, filepath.Join(f.source, "settings.json"), `{"model":"opus","effortLevel":"medium"}`)
	if _, err := PrepareNative(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "", "", codexContextLease, true); err != nil {
		t.Fatal("new discussion after owner change refused a drifted profile:", err)
	}
	raw, err := os.ReadFile(filepath.Join(f.candidate, "settings.json"))
	if err != nil || !strings.Contains(string(raw), `"medium"`) {
		t.Fatalf("owner refresh did not win over the drifted profile: %v %s", err, raw)
	}
}

func TestPrepareNative_ClaudeOwnerUnknownSettingStillLaunches(t *testing.T) {
	f, _ := driftClaudeInstall(t)
	put(t, filepath.Join(f.source, "settings.json"), `{"model":"opus","fastMode":true,"viewMode":"compact"}`)
	if _, err := PrepareNative(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "", "", codexContextLease, true); err != nil {
		t.Fatal("new discussion refused an ordinary owner setting:", err)
	}
}

func TestPrepareNative_CodexProfileModelWriteStillLaunches(t *testing.T) {
	for _, ownerDrift := range []bool{false, true} {
		t.Run(map[bool]string{false: "profile-only", true: "with-owner-drift"}[ownerDrift], func(t *testing.T) {
			f, p := driftCodexInstall(t)
			driftCodexModelWrite(t, f.candidate)
			m, err := readManifest(f.root, p.Ref)
			if err != nil {
				t.Fatal(err)
			}
			if err := RevalidateCodexContext(*m.CodexContext, f.candidate); err != nil {
				t.Fatal("running session revalidation refused the native /model write:", err)
			}
			if ownerDrift {
				put(t, filepath.Join(f.source, "config.toml"), "model_reasoning_effort=\"medium\"\n[projects.\""+f.cwd+"\"]\ntrust_level=\"trusted\"\n")
			}
			if _, err := PrepareNative(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, "", "", codexContextLease, true); err != nil {
				t.Fatal("new discussion refused the native /model write:", err)
			}
		})
	}
}

// Automatic onboarding prepares without creating history (createHistory=false).
func TestPrepareNative_OnboardingFollowsOwnerDrift(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			var f projectionFixture
			if provider == "codex" {
				f, _ = driftCodexInstall(t)
				put(t, filepath.Join(f.source, "config.toml"), "model_reasoning_effort=\"medium\"\n")
			} else {
				f, _ = driftClaudeInstall(t)
				put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"low"}`)
			}
			if _, err := PrepareNative(f.root, provider, f.candidate, f.cwd, f.env, []string{provider}, "", "", codexContextLease, false); err != nil {
				t.Fatal("onboarding refused owner drift:", err)
			}
		})
	}
}

func TestPrepareNative_ResumeAfterOwnerDriftReprojects(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			var f projectionFixture
			var p Projection
			if provider == "codex" {
				f, p = driftCodexInstall(t)
				put(t, filepath.Join(f.source, "config.toml"), "model_reasoning_effort=\"medium\"\n")
			} else {
				f, p = driftClaudeInstall(t)
				put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"low"}`)
			}
			q, err := PrepareNative(f.root, provider, f.candidate, f.cwd, f.env, []string{provider}, p.Ref, "", codexContextLease, true)
			if err != nil {
				t.Fatal("resume refused owner drift:", err)
			}
			if q.Ref == p.Ref {
				t.Fatal("resume after owner drift kept the stale projection")
			}
		})
	}
}
