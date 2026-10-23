package loginmigration

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
)

func logicalClaudeFixture(t *testing.T) (fixture, string, string) {
	t.Helper()
	f := newFixture(t)
	owner := filepath.Join(filepath.Dir(f.state), "owner")
	canonical := filepath.Join(filepath.Dir(f.state), "relocated", "state", "claude")
	if err := os.MkdirAll(owner, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(canonical, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canonical, filepath.Join(owner, ".claude")); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(owner, ".claude.json"), `{"hasCompletedOnboarding":true,"theme":"dark","projects":{"/trusted/project":{"hasTrustDialogAccepted":true}},"oauthAccount":{"accountUuid":"cached-account"},"userID":"cached-user","primaryApiKey":"synthetic-private-key"}`)
	return f, owner, canonical
}

func assertFilteredPreferences(t *testing.T, state string) {
	t.Helper()
	var prefs map[string]json.RawMessage
	if err := readJSON(filepath.Join(providerHome(state, "claude"), ".claude.json"), &prefs); err != nil {
		t.Fatal(err)
	}
	if string(prefs["hasCompletedOnboarding"]) != "true" || string(prefs["theme"]) != `"dark"` || !strings.Contains(string(prefs["projects"]), "hasTrustDialogAccepted") {
		t.Fatal("native onboarding/trust/preferences lost")
	}
	for _, key := range []string{"oauthAccount", "userID", "primaryApiKey"} {
		if _, exists := prefs[key]; exists {
			t.Fatal("cached native identity or API key entered common context")
		}
	}
}

func TestFirstActivationPreservesLogicalDefaultClaudePreferences(t *testing.T) {
	f, owner, canonical := logicalClaudeFixture(t)
	source := persist.Meta{ID: f.id, AgentType: "claude", Cwd: f.project, Env: []string{"HOME=" + owner}}
	inputs, err := contextSources(f.state, "claude", []persist.Meta{source})
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].Home != filepath.Join(owner, ".claude") || !inputs[0].ParentPreferences || inputs[0].Home == canonical {
		t.Fatal("logical default preference origin was erased")
	}
	if err = PrepareProviderContext(f.state, "claude", []persist.Meta{source}); err != nil {
		t.Fatal(err)
	}
	assertFilteredPreferences(t, f.state)
	raw, err := os.ReadFile(filepath.Join(owner, ".claude.json"))
	if err != nil || !strings.Contains(string(raw), "synthetic-private-key") {
		t.Fatal("source preferences were changed")
	}
}

func TestDefaultClaudePreferencesSurviveAbsentConfigDirectory(t *testing.T) {
	f, owner, _ := logicalClaudeFixture(t)
	if err := os.Remove(filepath.Join(owner, ".claude")); err != nil {
		t.Fatal(err)
	}
	source := persist.Meta{ID: f.id, AgentType: "claude", Cwd: f.project, Env: []string{"HOME=" + owner}}
	if err := PrepareProviderContext(f.state, "claude", []persist.Meta{source}); err != nil {
		t.Fatal(err)
	}
	assertFilteredPreferences(t, f.state)
	missing := contextSource{Home: filepath.Join(owner, ".claude"), ParentPreferences: true, Conversation: conversation}
	if _, err := planContext(f.state+"-uninitialized", "claude", []contextSource{missing}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("missing referenced history was accepted: %v", err)
	}
}

func TestExplicitClaudeConfigNeverInheritsSiblingPreferences(t *testing.T) {
	f, owner, _ := logicalClaudeFixture(t)
	explicit := filepath.Join(owner, ".claude")
	source, err := contextSourceFromEnv("claude", []string{"HOME=" + owner, "CLAUDE_CONFIG_DIR=" + explicit})
	if err != nil {
		t.Fatal(err)
	}
	if source.ParentPreferences {
		t.Fatal("explicit config selected owner sibling")
	}
	plan, err := planContext(f.state, "claude", []contextSource{source})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Files {
		if file.Target == ".claude.json" {
			t.Fatal("explicit config inherited unrelated sibling preferences")
		}
	}
}

func TestClaudeCanonicalChildPreferencesTakePrecedence(t *testing.T) {
	f, owner, canonical := logicalClaudeFixture(t)
	put(t, filepath.Join(owner, ".claude.json"), "malformed parent must not be consulted when native child exists")
	put(t, filepath.Join(canonical, ".claude.json"), `{"theme":"canonical-child","oauthAccount":{"accountUuid":"cached"}}`)
	source, err := contextSourceFromEnv("claude", []string{"HOME=" + owner})
	if err != nil {
		t.Fatal(err)
	}
	if err = prepareContext(f.state, "claude", []contextSource{source}); err != nil {
		t.Fatal(err)
	}
	var prefs map[string]json.RawMessage
	if err = readJSON(filepath.Join(providerHome(f.state, "claude"), ".claude.json"), &prefs); err != nil {
		t.Fatal(err)
	}
	if string(prefs["theme"]) != `"canonical-child"` || prefs["oauthAccount"] != nil {
		t.Fatal("native child precedence or identity filter changed")
	}
}

func TestLogicalClaudePreferencesAreComparedBeforeCanonicalDedup(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(map[bool]string{false: "conflicting", true: "same-after-auth-filter"}[same], func(t *testing.T) {
			f, owner, canonical := logicalClaudeFixture(t)
			other := filepath.Join(filepath.Dir(f.state), "other-owner")
			if err := os.MkdirAll(other, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(canonical, filepath.Join(other, ".claude")); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(owner, ".claude.json"))
			if err != nil {
				t.Fatal(err)
			}
			if same {
				raw = []byte(strings.ReplaceAll(string(raw), "cached-account", "different-cache"))
			} else {
				raw = []byte(`{"theme":"different"}`)
			}
			put(t, filepath.Join(other, ".claude.json"), raw)
			a, err := contextSourceFromEnv("claude", []string{"HOME=" + owner})
			if err != nil {
				t.Fatal(err)
			}
			b, err := contextSourceFromEnv("claude", []string{"HOME=" + other})
			if err != nil {
				t.Fatal(err)
			}
			_, err = planContext(f.state, "claude", []contextSource{{Home: canonical}, a, b})
			if same && err != nil {
				t.Fatal(err)
			}
			if !same && !errors.Is(err, ErrHistoryConflict) {
				t.Fatalf("alias preference conflict silently deduplicated: %v", err)
			}
		})
	}
}

func TestFullClaudeContextAfterHistoryOnlyRetainsSettingsAndPreferences(t *testing.T) {
	f, owner, canonical := logicalClaudeFixture(t)
	put(t, filepath.Join(canonical, "settings.json"), `{"permissions":{"defaultMode":"default"}}`)
	source, err := contextSourceFromEnv("claude", []string{"HOME=" + owner})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planContext(f.state, "claude", []contextSource{{Home: canonical, HistoryOnly: true}, source})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, file := range plan.Files {
		found[file.Target] = true
	}
	if !found["settings.json"] || !found[".claude.json"] {
		t.Fatal("history-only origin suppressed full native configuration")
	}
}

func TestUnsafeLogicalClaudePreferencesRefuseBeforeMutation(t *testing.T) {
	for _, kind := range []string{"malformed", "writable", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			f, owner, _ := logicalClaudeFixture(t)
			pref := filepath.Join(owner, ".claude.json")
			switch kind {
			case "malformed":
				put(t, pref, `{"theme":`)
			case "writable":
				if err := os.Chmod(pref, 0666); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Remove(pref); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(filepath.Dir(f.state), "foreign-preferences")
				put(t, target, `{"theme":"foreign"}`)
				var err error
				if kind == "symlink" {
					err = os.Symlink(target, pref)
				} else {
					err = os.Link(target, pref)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			source, err := contextSourceFromEnv("claude", []string{"HOME=" + owner})
			if err != nil {
				t.Fatal(err)
			}
			if err = prepareContext(f.state, "claude", []contextSource{source}); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unsafe preferences silently dropped: %v", err)
			}
			if _, err = os.Stat(filepath.Join(f.state, "logins")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unsafe preference refusal mutated destination")
			}
		})
	}
}

func TestFrozenClaudeProjectionPreservesLogicalPreferenceSelector(t *testing.T) {
	f, owner, canonical := logicalClaudeFixture(t)
	history := filepath.Join(canonical, "projects")
	put(t, filepath.Join(history, claude.New().ProjectDirName(f.project), conversation+".jsonl"), "native transcript")
	info, err := os.Stat(history)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	projection := map[string]any{"SchemaVersion": 3, "Provider": "claude", "Cwd": f.project, "ClaudeContext": map[string]any{"SourceProfile": map[string]string{"Path": filepath.Join(owner, ".claude"), "Canonical": canonical}, "PreferencesPath": filepath.Join(owner, ".claude.json")}, "ClaudeHistoryAlias": map[string]any{"SourcePath": history, "Device": uint64(stat.Dev), "Inode": stat.Ino}}
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	ref := digest(raw)
	put(t, filepath.Join(f.state, "accounts", "configurations", ref, "projection.json"), raw)
	source, err := projectionHome(f.state, ref, "claude", f.project)
	if err != nil {
		t.Fatal(err)
	}
	if source.Home != filepath.Join(owner, ".claude") || !source.ParentPreferences {
		t.Fatal("frozen logical preference selector lost")
	}
	var registry oldRegistry
	if err = readJSON(filepath.Join(f.state, "accounts", "registry.json"), &registry); err != nil {
		t.Fatal(err)
	}
	account := registry.Accounts[f.account]
	account.Provider, account.Auth = "claude", "invalid"
	generation := account.Generations[account.Current]
	generation.Identity = strings.Repeat("c", 64)
	account.Generations[account.Current] = generation
	registry.Accounts[f.account] = account
	put(t, filepath.Join(f.state, "accounts", "registry.json"), registry)
	f.meta["agent_type"] = "claude"
	f.meta["account_binding"].(map[string]any)["provider"] = "claude"
	f.meta["account_projection_ref"] = ref
	// A canonical source is visited first; the frozen logical projection must
	// still contribute its selected sibling preferences through the real caller.
	f.meta["env"] = []string{"HOME=" + owner, "CLAUDE_CONFIG_DIR=" + canonical}
	put(t, filepath.Join(f.state, f.id, "meta.json"), f.meta)
	if err = Run(f.state); err != nil {
		t.Fatal(err)
	}
	assertFilteredPreferences(t, f.state)
	projection["ClaudeContext"].(map[string]any)["PreferencesPath"] = filepath.Join(owner, "unrelated.json")
	raw, err = json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	ref = digest(raw)
	put(t, filepath.Join(f.state, "accounts", "configurations", ref, "projection.json"), raw)
	if _, err = projectionHome(f.state, ref, "claude", f.project); !errors.Is(err, ErrUnsupported) {
		t.Fatal("arbitrary projection preference path accepted")
	}
}

func TestPreferenceSelectorJournalAndWriterBoundaries(t *testing.T) {
	f, owner, canonical := logicalClaudeFixture(t)
	source, err := contextSourceFromEnv("claude", []string{"HOME=" + owner})
	if err != nil {
		t.Fatal(err)
	}
	plan := migration{SchemaVersion: 1, Phase: "planned", Contexts: map[string][]contextSource{"claude": {source}}}
	if err = validateMigration(f.state, plan); err != nil {
		t.Fatal(err)
	}
	plan.Contexts = map[string][]contextSource{"codex": {source}}
	if err = validateMigration(f.state, plan); !errors.Is(err, ErrUnsupported) {
		t.Fatal("Codex gained Claude preference selector")
	}
	plan.Contexts = map[string][]contextSource{"claude": {{Home: canonical, ParentPreferences: true}}}
	if err = validateMigration(f.state, plan); !errors.Is(err, ErrUnsupported) {
		t.Fatal("nondefault path gained parent preference authority")
	}
	plan.Contexts = map[string][]contextSource{"claude": {source}}
	environmentWriters = func(homes, ids []string) ([]processcontain.Identity, error) {
		seen := map[string]bool{}
		for _, home := range homes {
			seen[home] = true
		}
		if !seen[source.Home] || !seen[canonical] {
			t.Fatal("writer scan lost logical or canonical native alias")
		}
		return nil, nil
	}
	if err = revalidateMigrationWriters(f.state, &plan); err != nil {
		t.Fatal(err)
	}
}
