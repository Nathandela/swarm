package accountconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func codexContextLease(_ string, install func() error) error { return install() }
func codexContextFixture(t *testing.T) projectionFixture {
	t.Helper()
	f := fixture(t, "codex")
	put(t, filepath.Join(f.source, "auth.json"), "ambient synthetic auth never copied")
	put(t, filepath.Join(f.candidate, "auth.json"), "selected synthetic auth")
	put(t, filepath.Join(f.source, "config.toml"), "model_reasoning_effort=\"high\"\n[projects.\""+f.cwd+"\"]\ntrust_level=\"trusted\"\n[mcp_servers.fixture]\ncommand=\"/bin/false\"\ncwd=\"./opaque-mcp-cwd\"\n")
	return f
}
func readNativeCodexFixtureSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if toml.Unmarshal(raw, &out) != nil {
		t.Fatal("invalid installed TOML")
	}
	return out
}

func TestCodexContextRetainsUserTierAcrossAccountsAndConcurrentProjects(t *testing.T) {
	f := codexContextFixture(t)
	project := filepath.Join(f.cwd, ".codex", "config.toml")
	put(t, project, "model_reasoning_effort=\"low\"\n[sandbox_workspace_write]\nnetwork_access=false\n")
	if err := os.Chmod(project, 0664); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.source, "skills", "sample", "SKILL.md"), "sample")
	put(t, filepath.Join(f.source, "plugins", "cache", "sample"), "plugin")
	put(t, filepath.Join(f.source, ".tmp", "marketplaces", "fixture"), "marketplace")
	put(t, filepath.Join(f.source, "hooks.json"), `{"hooks":{}}`)
	called := 0
	lease := func(g string, install func() error) error {
		called++
		if len(g) != 64 {
			t.Fatal("missing generation")
		}
		return install()
	}
	c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, lease)
	if err != nil {
		t.Fatal(err)
	}
	installed := readNativeCodexFixtureSettings(t, filepath.Join(f.candidate, "config.toml"))
	source := readNativeCodexFixtureSettings(t, filepath.Join(f.source, "config.toml"))
	if !reflect.DeepEqual(installed, source) {
		t.Fatal("ordinary native settings changed or project tier flattened")
	}
	next := filepath.Join(filepath.Dir(f.cwd), "second-project")
	put(t, filepath.Join(next, ".codex", "config.toml"), "model_reasoning_effort=\"medium\"\n")
	d, err := PrepareCodexContext(f.candidate, next, f.env, lease)
	if err != nil || d.GlobalGeneration != c.GlobalGeneration {
		t.Fatalf("shared global cohort depends on cwd: %v", err)
	}
	if called != 2 {
		t.Fatal("existing cohort bypassed writer lease")
	}
	second := filepath.Join(filepath.Dir(f.candidate), "second-account")
	if err := os.Mkdir(second, 0700); err != nil {
		t.Fatal(err)
	}
	e, err := PrepareCodexContext(second, next, f.env, lease)
	if err != nil || e.GlobalGeneration != c.GlobalGeneration {
		t.Fatalf("account rotation changed source cohort: %v", err)
	}
	sources, err := ValidateCodexProjectSettings(f.cwd, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if err := RevalidateCodexProjectSettings(sources); err != nil {
		t.Fatal(err)
	}
	put(t, project, "model_reasoning_effort=\"medium\"\n")
	if err := RevalidateCodexProjectSettings(sources); err == nil {
		t.Fatal("changed project accepted")
	}
	for path, want := range map[string]string{filepath.Join(f.source, "auth.json"): "ambient synthetic auth never copied", filepath.Join(f.candidate, "auth.json"): "selected synthetic auth"} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatal("configuration preparation changed credential authority")
		}
	}
	for _, a := range c.Assets {
		if a.SHA256 == "absent" {
			continue
		}
		target, err := os.Readlink(filepath.Join(f.candidate, a.Name))
		if err != nil || target != a.Target {
			t.Fatal("ordinary native asset not retained")
		}
	}
}

func TestCodexContextRebasesOnlyPinnedSchemaSourceRelativePaths(t *testing.T) {
	f := codexContextFixture(t)
	put(t, filepath.Join(f.source, "config.toml"), `model_instructions_file="./instructions.md"
js_repl_node_path="../bin/node"
js_repl_node_module_dirs=["modules","~/other-modules"]
sqlite_home="./db"
log_dir="./logs"
model_catalog_json="~/catalog.json"
experimental_compact_prompt_file="compact.md"
ordinary_unknown="./unchanged"
[sandbox_workspace_write]
writable_roots=["../shared"]
network_access=false
[mcp_servers.fixture]
command="./not-rebased"
args=["./argument"]
cwd="./opaque-mcp-cwd"
[permissions.fixture.filesystem]
"./cwd-relative"="write"
[[skills.config]]
path="skills/sample/SKILL.md"
enabled=false
[otel.exporter.otlp-http]
endpoint="https://synthetic.invalid"
protocol="binary"
[otel.exporter.otlp-http.tls]
ca-certificate="tls/ca.pem"
client-certificate="tls/client.pem"
client-private-key="tls/key.pem"
[marketplaces.local]
source_type="local"
source="./unchanged-native-cwd"
`)
	c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	got := readNativeCodexFixtureSettings(t, filepath.Join(f.candidate, "config.toml"))
	for key, want := range map[string]string{"model_instructions_file": filepath.Join(f.source, "instructions.md"), "js_repl_node_path": filepath.Join(f.home, "bin", "node"), "sqlite_home": filepath.Join(f.source, "db"), "log_dir": filepath.Join(f.source, "logs"), "model_catalog_json": filepath.Join(f.home, "catalog.json"), "experimental_compact_prompt_file": filepath.Join(f.source, "compact.md"), "ordinary_unknown": "./unchanged"} {
		if got[key] != want {
			t.Fatalf("wrong native path semantics for %s", key)
		}
	}
	modules := got["js_repl_node_module_dirs"].([]any)
	if modules[0] != filepath.Join(f.source, "modules") || modules[1] != filepath.Join(f.home, "other-modules") {
		t.Fatal("module path semantics changed")
	}
	mcp := got["mcp_servers"].(map[string]any)["fixture"].(map[string]any)
	if mcp["command"] != "./not-rebased" || mcp["cwd"] != "./opaque-mcp-cwd" {
		t.Fatal("rewrote opaque native fields")
	}
	skill := got["skills"].(map[string]any)["config"].([]any)[0].(map[string]any)
	if skill["path"] != filepath.Join(f.source, "skills", "sample", "SKILL.md") {
		t.Fatal("skill origin lost")
	}
	tls := got["otel"].(map[string]any)["exporter"].(map[string]any)["otlp-http"].(map[string]any)["tls"].(map[string]any)
	if tls["client-private-key"] != filepath.Join(f.source, "tls", "key.pem") {
		t.Fatal("OTEL origin lost")
	}
	if err := RevalidateCodexContext(c, f.candidate); err != nil {
		t.Fatal(err)
	}
}

func TestCodexContextHoldsChangesWithoutOverwritingNativeWrites(t *testing.T) {
	for _, kind := range []string{"source-config", "candidate-config", "source-alias", "asset-root", "asset-alias", "marker", "policy", "tmp-alias"} {
		t.Run(kind, func(t *testing.T) {
			f := codexContextFixture(t)
			put(t, filepath.Join(f.source, "plugins", "cache", "file"), "initial")
			alias := filepath.Join(f.home, "codex-alias")
			if err := os.Symlink(f.source, alias); err != nil {
				t.Fatal(err)
			}
			f.env = append(f.env, "CODEX_HOME="+alias)
			c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "source-config":
				put(t, filepath.Join(f.source, "config.toml"), "model=\"gpt-changed\"\n")
			case "candidate-config":
				put(t, filepath.Join(f.candidate, "config.toml"), "model=\"gpt-native-write\"\n")
			case "source-alias":
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.candidate, alias); err != nil {
					t.Fatal(err)
				}
			case "asset-root":
				if err := os.Rename(filepath.Join(f.source, "plugins"), filepath.Join(f.source, "plugins-old")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(f.source, "plugins"), 0700); err != nil {
					t.Fatal(err)
				}
			case "asset-alias":
				if err := os.Remove(filepath.Join(f.candidate, "plugins")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.cwd, filepath.Join(f.candidate, "plugins")); err != nil {
					t.Fatal(err)
				}
			case "marker":
				put(t, filepath.Join(f.candidate, CodexContextMarker), `{}`)
			case "policy":
				put(t, filepath.Join(f.candidate, "managed_config.toml"), "model=\"other\"\n")
			case "tmp-alias":
				if err := os.Symlink(f.cwd, filepath.Join(f.candidate, ".tmp")); err != nil {
					t.Fatal(err)
				}
			}
			if err := RevalidateCodexContext(c, f.candidate); err == nil {
				t.Fatal("changed context accepted")
			}
			before, _ := os.ReadFile(filepath.Join(f.candidate, "config.toml"))
			if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease); err == nil {
				t.Fatal("changed cohort admitted")
			}
			after, _ := os.ReadFile(filepath.Join(f.candidate, "config.toml"))
			if string(before) != string(after) {
				t.Fatal("hold overwrote native changes")
			}
		})
	}
}

func TestCodexContextRefusesAuthSelectorsAndLeaseDenialBeforeWrites(t *testing.T) {
	for _, settings := range []string{`model_provider="foreign"`, `cli_auth_credentials_store="keyring"`, `forced_login_method="api"`, `[model_providers.other]
env_key="SYNTHETIC_KEY"`, `[agents.other]
config_file="other.toml"`, `[features.network_proxy]
credential_broker={url="synthetic"}`, `[features.network_proxy]
credentials={synthetic="secret"}`} {
		for _, project := range []bool{false, true} {
			f := codexContextFixture(t)
			path := filepath.Join(f.source, "config.toml")
			if project {
				path = filepath.Join(f.cwd, ".codex", "config.toml")
			}
			put(t, path, settings)
			var err error
			if project {
				_, err = ValidateCodexProjectSettings(f.cwd, f.env)
			} else {
				_, err = PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
			}
			if err == nil {
				t.Fatal("alternate auth route admitted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("source contents exposed")
			}
			if _, err := os.Stat(filepath.Join(f.candidate, "config.toml")); !os.IsNotExist(err) {
				t.Fatal("selector refusal wrote private configuration")
			}
		}
	}
	f := codexContextFixture(t)
	if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, func(string, func() error) error { return Conflict("busy") }); err == nil {
		t.Fatal("lease denial ignored")
	}
	if _, err := os.Stat(filepath.Join(f.candidate, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("lease denial wrote configuration")
	}
}

func TestCodexContextAssetUpdatesRetainCustodyAndAmbientAuthReplacementIsIrrelevant(t *testing.T) {
	f := codexContextFixture(t)
	put(t, filepath.Join(f.source, "plugins", "cache", "file"), "initial")
	c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.source, "plugins", "cache", "file"), "native mutable cache update")
	put(t, filepath.Join(f.source, "auth.new"), "replacement ambient auth")
	if err := os.Rename(filepath.Join(f.source, "auth.new"), filepath.Join(f.source, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := RevalidateCodexContext(c, f.candidate); err != nil {
		t.Fatal("ordinary cache update or unrelated ambient auth invalidated cohort")
	}
	raw, _ := os.ReadFile(filepath.Join(f.candidate, "auth.json"))
	if string(raw) != "selected synthetic auth" {
		t.Fatal("ambient auth replaced selected authority")
	}
}

func TestCodexNativeArgumentsKeepOrdinaryOverridesAndHoldAuthSelectors(t *testing.T) {
	for _, argv := range [][]string{
		{"codex", "--model", "gpt-fixture", "-c", "model_reasoning_effort=high"},
		{"codex", "-c", "model_provider=openai", "--config=cli_auth_credentials_store=\"file\""},
		{"codex", "-csandbox_workspace_write.network_access=false"},
		{"codex", "-c", "mcp_servers.fixture.env.API_TOKEN=\"synthetic-tool-token\""},
	} {
		if err := ValidateCodexNativeArguments(argv); err != nil {
			t.Fatalf("ordinary explicit invocation refused: %v", err)
		}
	}
	for _, argv := range [][]string{
		{"codex", "-c", "model_provider=foreign"},
		{"codex", "-cmodel_provider=foreign"},
		{"codex", "--config=model_providers.other.env_key=\"SYNTHETIC_API_TOKEN\""},
		{"codex", "--profile", "alternate"},
		{"codex", "-palternate"},
		{"codex", "--remote=unix:///tmp/foreign.sock"},
		{"codex", "-c", "project_root_markers=[\"custom-marker\"]"},
		{"codex", "-c", "agents.other.config_file=\"other.toml\""},
	} {
		if err := ValidateCodexNativeArguments(argv); err == nil {
			t.Fatal("alternate authentication or project discovery accepted")
		}
	}
}

func TestCodexContextHookStatePortableAcrossAccountsAndPrivateWritesHeld(t *testing.T) {
	f := codexContextFixture(t)
	sourceCfg := filepath.Join(f.source, "config.toml")
	sourceHooks := filepath.Join(f.source, "hooks.json")
	put(t, sourceHooks, `{"hooks":{}}`)
	cfg := "model=\"gpt-synthetic\"\n[hooks.state.\"" + sourceCfg + ":PreToolUse:0:0\"]\nenabled=false\ntrusted_hash=\"original-trust\"\n[hooks.state.\"" + sourceHooks + ":PostToolUse:0:0\"]\nenabled=false\n[hooks.state.\"plugin:fixture:hooks.json:PreToolUse:0:0\"]\nenabled=false\n"
	put(t, sourceCfg, cfg)
	c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	installed := readNativeCodexFixtureSettings(t, filepath.Join(f.candidate, "config.toml"))
	state := installed["hooks"].(map[string]any)["state"].(map[string]any)
	key := filepath.Join(f.candidate, "config.toml") + ":PreToolUse:0:0"
	handler, ok := state[key].(map[string]any)
	if !ok || handler["enabled"] != false || handler["trusted_hash"] != "original-trust" || state["plugin:fixture:hooks.json:PreToolUse:0:0"] == nil || state[sourceCfg+":PreToolUse:0:0"] != nil {
		t.Fatal("native hook trust/disabled state lost")
	}
	other := filepath.Join(filepath.Dir(f.candidate), "other-account")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	d, err := PrepareCodexContext(other, f.cwd, f.env, codexContextLease)
	if err != nil || !reflect.DeepEqual(c, d) {
		t.Fatalf("hook state made global cohort account-dependent: %v", err)
	}
	state[key].(map[string]any)["enabled"] = true
	raw, err := toml.Marshal(installed)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.candidate, "config.toml"), string(raw))
	if err := RevalidateCodexContext(c, f.candidate); err == nil {
		t.Fatal("private native hook write erased or admitted")
	}
}
func TestCodexContextHookStateCollisionAndWrongNativeSourceHeld(t *testing.T) {
	for _, collision := range []bool{false, true} {
		f := codexContextFixture(t)
		cfg := filepath.Join(f.source, "config.toml")
		put(t, cfg, "[hooks.state.\""+cfg+":PreToolUse:0:0\"]\nenabled=false\n")
		if collision {
			file, _ := os.ReadFile(cfg)
			put(t, cfg, string(file)+"[hooks.state.\""+filepath.Join(f.candidate, "config.toml")+":PreToolUse:0:0\"]\nenabled=true\n")
			if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease); err == nil {
				t.Fatal("colliding native hook state admitted")
			}
			continue
		}
		c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(filepath.Join(f.candidate, "config.toml"))
		raw = []byte(strings.ReplaceAll(string(raw), filepath.Join(f.candidate, "config.toml"), cfg))
		put(t, filepath.Join(f.candidate, "config.toml"), string(raw))
		if err := RevalidateCodexContext(c, f.candidate); err == nil {
			t.Fatal("semantically inactive owner key admitted as private trust")
		}
	}
}
func TestCodexContextExplicitOwnerRefreshPreservesHistoryAndHoldsNativeDivergence(t *testing.T) {
	for _, privateWrite := range []bool{false, true} {
		f := codexContextFixture(t)
		old, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
		if err != nil {
			t.Fatal(err)
		}
		history, err := PrepareCodexHistoryAlias(old, f.candidate, true, codexContextLease)
		if err != nil {
			t.Fatal(err)
		}
		put(t, filepath.Join(f.source, "config.toml"), "model_reasoning_effort=\"medium\"\n")
		if privateWrite {
			put(t, filepath.Join(f.candidate, "config.toml"), "model_reasoning_effort=\"low\"\n")
		}
		before, _ := os.ReadFile(filepath.Join(f.candidate, "config.toml"))
		if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease); err == nil {
			t.Fatal("automatic source refresh admitted")
		}
		next, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease, true)
		if privateWrite {
			if err == nil {
				t.Fatal("explicit source refresh overwrote native private write")
			}
			after, _ := os.ReadFile(filepath.Join(f.candidate, "config.toml"))
			if string(after) != string(before) {
				t.Fatal("native private write lost")
			}
			continue
		}
		if err != nil || next.GlobalGeneration == old.GlobalGeneration {
			t.Fatalf("explicit quiescent owner refresh held: %v", err)
		}
		if err := RevalidateCodexContext(old, f.candidate); err == nil {
			t.Fatal("old frozen projection silently refreshed")
		}
		if _, err := PrepareCodexHistoryAlias(next, f.candidate, false, codexContextLease); err != nil {
			t.Fatalf("context refresh did not commit history within its lease: %v", err)
		}
		updated, err := PrepareCodexHistoryAlias(next, f.candidate, false, codexContextLease, true)
		if err != nil || updated.SourcePath != history.SourcePath || updated.Device != history.Device || updated.Inode != history.Inode || updated.GlobalGeneration != next.GlobalGeneration {
			t.Fatalf("explicit refresh moved native history: %v", err)
		}
	}
}

func TestCodexContextDefaultHomeAliasRetainsNativeLexicalPathOrigin(t *testing.T) {
	f := codexContextFixture(t)
	actual := filepath.Join(f.home, "runtime", "codex")
	if err := os.MkdirAll(filepath.Dir(actual), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.source, actual); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, f.source); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(actual, "config.toml"), "model_instructions_file=\"../instructions.md\"\n[hooks.state.\""+filepath.Join(f.source, "config.toml")+":PreToolUse:0:0\"]\nenabled=false\n")
	c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	settings := readNativeCodexFixtureSettings(t, filepath.Join(f.candidate, "config.toml"))
	if c.NativeProfile != f.source || settings["model_instructions_file"] != filepath.Join(f.home, "instructions.md") {
		t.Fatal("default alias path rebased against canonicalparent instead of nativelexicalorigin")
	}
	state := settings["hooks"].(map[string]any)["state"].(map[string]any)
	if state[filepath.Join(f.candidate, "config.toml")+":PreToolUse:0:0"] == nil {
		t.Fatal("default alias hookstate lost")
	}
	explicit := filepath.Join(filepath.Dir(f.candidate), "explicit-account")
	if err := os.Mkdir(explicit, 0700); err != nil {
		t.Fatal(err)
	}
	env := append(append([]string{}, f.env...), "CODEX_HOME="+f.source)
	d, err := PrepareCodexContext(explicit, f.cwd, env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	settings = readNativeCodexFixtureSettings(t, filepath.Join(explicit, "config.toml"))
	if d.NativeProfile != actual || settings["model_instructions_file"] != filepath.Join(filepath.Dir(actual), "instructions.md") {
		t.Fatal("explicit CODEX_HOME did not retain canonicalnativeorigin")
	}
}

func TestCodexContextEnvironmentReconstructsImplicitAndExplicitSelectors(t *testing.T) {
	f := codexContextFixture(t)
	actual := filepath.Join(f.home, "runtime-codex")
	if err := os.Rename(f.source, actual); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, f.source); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		env := append([]string{}, f.env...)
		if explicit {
			env = append(env, "CODEX_HOME="+f.source)
		}
		profile := filepath.Join(filepath.Dir(f.candidate), "implicit")
		if explicit {
			profile += "-explicit"
		}
		if err := os.Mkdir(profile, 0700); err != nil {
			t.Fatal(err)
		}
		c, err := PrepareCodexContext(profile, f.cwd, env, codexContextLease)
		if err != nil {
			t.Fatal(err)
		}
		bound := append(append([]string{}, f.env...), "CODEX_HOME="+profile)
		restored, err := CodexContextEnvironment(c, bound)
		if err != nil {
			t.Fatal(err)
		}
		d, err := PrepareCodexContext(profile, f.cwd, restored, codexContextLease)
		if err != nil || !reflect.DeepEqual(c, d) {
			t.Fatalf("resume changed frozen native selector: %v", err)
		}
		wrong := append(bound, "HOME="+f.cwd)
		if _, err := CodexContextEnvironment(c, wrong); err == nil {
			t.Fatal("different nativeHOME admitted")
		}
	}
}
