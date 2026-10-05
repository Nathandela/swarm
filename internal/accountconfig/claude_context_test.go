package accountconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeContextPreservesNativeTiersAndSelectedIdentity(t *testing.T) {
	f := fixture(t, "claude")
	put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"high","hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`)
	put(t, filepath.Join(f.home, ".claude.json"), `{"oauthAccount":{"organizationUuid":"owner-org"},"primaryApiKey":"owner-key","mcpServers":{"local":{"command":"synthetic"}},"projects":{"/project":{"hasTrustDialogAccepted":true,"lastCost":123}}}`)
	put(t, filepath.Join(f.candidate, ".claude.json"), `{"oauthAccount":{"organizationUuid":"selected-org"},"primaryApiKey":"selected-key","userID":"selected-user"}`)
	put(t, filepath.Join(f.source, "skills", "example", "SKILL.md"), "synthetic skill")
	put(t, filepath.Join(f.cwd, ".claude", "settings.json"), `{"effortLevel":"low"}`)
	called := false
	c, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, func(g string, install func() error) error {
		called = true
		if len(g) != 64 {
			t.Fatal("missing generation")
		}
		return install()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("installation bypassed caller lease")
	}
	raw, err := os.ReadFile(filepath.Join(f.candidate, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if json.Unmarshal(raw, &settings) != nil || settings["effortLevel"] != "high" {
		t.Fatal("project settings flattened into global tier")
	}
	raw, err = os.ReadFile(filepath.Join(f.candidate, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if json.Unmarshal(raw, &state) != nil {
		t.Fatal("bad state")
	}
	if state["primaryApiKey"] != "selected-key" || state["userID"] != "selected-user" || state["oauthAccount"].(map[string]any)["organizationUuid"] != "selected-org" {
		t.Fatal("owner identity copied")
	}
	project := state["projects"].(map[string]any)["/project"].(map[string]any)
	if project["lastCost"] != nil || project["hasTrustDialogAccepted"] != true {
		t.Fatal("copied unrelated native state")
	}
	if err := RevalidateClaudeContext(c, f.candidate); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(filepath.Dir(f.cwd), "second-project")
	if err := os.Mkdir(next, 0700); err != nil {
		t.Fatal(err)
	}
	d, err := PrepareClaudeContext(f.candidate, next, f.env, func(g string, install func() error) error { return install() })
	if err != nil {
		t.Fatal(err)
	}
	if d.GlobalGeneration != c.GlobalGeneration {
		t.Fatal("global generation depends on project")
	}
	put(t, filepath.Join(f.source, "skills", "example", "SKILL.md"), "changed")
	if err := RevalidateClaudeContext(c, f.candidate); err != nil {
		t.Fatal("owner-native content update changed root custody")
	}
	if err := os.Rename(filepath.Join(f.source, "skills"), filepath.Join(f.source, "skills-old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.source, "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := RevalidateClaudeContext(c, f.candidate); err == nil {
		t.Fatal("asset root replacement admitted")
	}
}

func TestClaudeContextRefusesAuthSettingsAndLeaseDenialBeforeWrites(t *testing.T) {
	for _, settings := range []string{`{"env":{"ANTHROPIC_API_KEY":"synthetic"}}`, `{"apiKeyHelper":"synthetic"}`} {
		f := fixture(t, "claude")
		put(t, filepath.Join(f.source, "settings.json"), settings)
		if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, func(g string, install func() error) error { return install() }); err == nil {
			t.Fatal("auth settings admitted")
		}
		if _, err := os.Stat(filepath.Join(f.candidate, "settings.json")); !os.IsNotExist(err) {
			t.Fatal("refusal wrote destination")
		}
	}
	f := fixture(t, "claude")
	put(t, filepath.Join(f.source, "settings.json"), `{"model":"sonnet"}`)
	if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, func(g string, install func() error) error { return Conflict("busy") }); err == nil {
		t.Fatal("lease denial ignored")
	}
	if _, err := os.Stat(filepath.Join(f.candidate, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("lease denial wrote destination")
	}
}

func TestClaudeContextExplicitOwnerRefreshPreservesHistoryAndHoldsPrivateEdits(t *testing.T) {
	for _, privateEdit := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner-refresh", true: "private-edit-held"}[privateEdit], func(t *testing.T) {
			f := fixture(t, "claude")
			put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"high"}`)
			lease := func(_ string, install func() error) error { return install() }
			old, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
			if err != nil {
				t.Fatal(err)
			}
			history, err := PrepareClaudeHistoryAlias(old, f.candidate, "-existing", true, lease)
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"low"}`)
			if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease); err == nil {
				t.Fatal("automatic source drift refreshed")
			}
			if privateEdit {
				put(t, filepath.Join(f.candidate, "settings.json"), `{"language":"Spanish"}`)
			}
			next, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease, true)
			if privateEdit {
				if err == nil {
					t.Fatal("native private configuration overwritten")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if next.GlobalGeneration == old.GlobalGeneration {
				t.Fatal("owner generation did not change")
			}
			continued, err := PrepareClaudeHistoryAlias(next, f.candidate, history.ProjectKey, false, lease)
			if err != nil || continued.SourcePath != history.SourcePath || continued.Device != history.Device || continued.Inode != history.Inode {
				t.Fatal("refresh changed original history authority")
			}
		})
	}
}

func TestClaudeContextImmutableMarkerAndNoRewrite(t *testing.T) {
	f := fixture(t, "claude")
	put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"high"}`)
	lease := func(g string, install func() error) error { return install() }
	c, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(f.candidate, "settings.json")
	before, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.candidate, ".claude.json"), `{"numStartups":999,"oauthAccount":{"organizationUuid":"selected"}}`)
	if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(settingsPath)
	if !os.SameFile(before, after) {
		t.Fatal("same global generation rewrote live native settings")
	}
	raw, _ := os.ReadFile(filepath.Join(f.candidate, ".claude.json"))
	var state map[string]any
	_ = json.Unmarshal(raw, &state)
	if state["numStartups"] != float64(999) {
		t.Fatal("reuse overwrote live native state")
	}
	markerPath := filepath.Join(f.candidate, ClaudeContextMarker)
	raw, _ = os.ReadFile(markerPath)
	var marker map[string]any
	_ = json.Unmarshal(raw, &marker)
	marker["PreferencesPath"] = "/unrelated/synthetic"
	raw, _ = json.Marshal(marker)
	put(t, markerPath, string(raw))
	if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease); err == nil {
		t.Fatal("marker changed under unchanged generation")
	}
	if err := RevalidateClaudeContext(ClaudeContext{GlobalGeneration: c.GlobalGeneration}, f.candidate); err == nil {
		t.Fatal("forged context accepted")
	}
}

func TestClaudeContextOwnedSourcesAndProjectAuthGuard(t *testing.T) {
	f := fixture(t, "claude")
	if err := os.Chmod(f.source, 0775); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"high"}`)
	if err := os.Chmod(filepath.Join(f.source, "settings.json"), 0664); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.home, ".claude.json"), `{"projects":{}}`)
	if err := os.Chmod(filepath.Join(f.home, ".claude.json"), 0664); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, func(g string, install func() error) error { return install() }); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.cwd, ".claude", "settings.local.json"), `{"env":{"ANTHROPIC_API_KEY":"synthetic"}}`)
	if _, err := ValidateClaudeProjectSettings(f.cwd, f.env); err == nil {
		t.Fatal("project auth selector admitted")
	}
	put(t, filepath.Join(f.cwd, ".claude", "settings.local.json"), `{"effortLevel":"low"}`)
	sources, err := ValidateClaudeProjectSettings(f.cwd, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateClaudeProjectSources(sources); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.cwd, ".claude", "settings.local.json"), `{"effortLevel":"medium"}`)
	if err := validateClaudeProjectSources(sources); err == nil {
		t.Fatal("project settings mutation admitted")
	}
}

func TestClaudeContextFiltersLegacyOwnerState(t *testing.T) {
	f := fixture(t, "claude")
	put(t, filepath.Join(f.source, "settings.json"), `{"model":"sonnet","worktree":{"symlinkDirectories":["node_modules"]},"oauthAccount":{"organizationUuid":"owner"},"cachedGrowthBookFeatures":{"synthetic":true},"userID":"owner","numStartups":100,"/synthetic/project":{"lastCost":123}}`)
	if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, func(g string, install func() error) error { return install() }); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(f.candidate, "settings.json"))
	var settings map[string]any
	_ = json.Unmarshal(raw, &settings)
	if len(settings) != 2 || settings["model"] != "sonnet" || settings["worktree"] == nil {
		t.Fatal("legacy state copied or ordinary worktree settings dropped")
	}
}

func TestClaudeContextInvocationAuthGuard(t *testing.T) {
	if _, err := ValidateClaudeInvocationSettings([]string{"claude", "--settings", `{"env":{"ANTHROPIC_API_KEY":"synthetic"}}`}); err == nil {
		t.Fatal("invocation auth selector admitted")
	}
	f := fixture(t, "claude")
	path := filepath.Join(f.home, "observer.json")
	put(t, path, `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`)
	sources, err := ValidateClaudeInvocationSettings([]string{"claude", "--settings", path})
	if err != nil || len(sources) != 1 {
		t.Fatal("ordinary observer overlay refused")
	}
	put(t, path, `{"env":{"ANTHROPIC_AUTH_TOKEN":"synthetic"}}`)
	if err := RevalidateClaudeProjectSettings(sources); err == nil {
		t.Fatal("invocation source mutation admitted")
	}
}

// Explicit read-only owner replay: all writes stay in testing.T's private dir.
func TestClaudeContextOwnerSnapshot(t *testing.T) {
	ownerHome := os.Getenv("SWARM_TEST_CLAUDE_OWNER_HOME")
	if ownerHome == "" {
		t.Skip("explicit owner source replay required")
	}
	f := fixture(t, "claude")
	put(t, filepath.Join(f.candidate, ".claude.json"), `{"oauthAccount":{"accountUuid":"synthetic-selected","organizationUuid":"synthetic-selected-org"},"userID":"synthetic-selected-user"}`)
	c, err := PrepareClaudeContext(f.candidate, f.cwd, []string{"HOME=" + ownerHome}, func(g string, install func() error) error { return install() })
	if err != nil {
		t.Fatal(err)
	}
	if err := RevalidateClaudeContext(c, f.candidate); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.candidate, ".claude.json"))
	if err != nil {
		t.Fatal("private replay unavailable")
	}
	var selected map[string]any
	_ = json.Unmarshal(raw, &selected)
	if selected["userID"] != "synthetic-selected-user" || selected["oauthAccount"].(map[string]any)["organizationUuid"] != "synthetic-selected-org" {
		t.Fatal("owner account metadata leaked into snapshot")
	}
}

func TestClaudeContextRefusesLegacyConfigOverride(t *testing.T) {
	f := fixture(t, "claude")
	put(t, filepath.Join(f.candidate, ".config.json"), `{"oauthAccount":{"organizationUuid":"other"}}`)
	if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, func(g string, install func() error) error { return install() }); err == nil {
		t.Fatal("legacy config can shadow enrolled metadata")
	}
	if _, err := os.Stat(filepath.Join(f.candidate, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("legacy config refusal wrote settings")
	}
}
