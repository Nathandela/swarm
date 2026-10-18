package daemon

import (
	"reflect"
	"strings"
	"testing"
)

func TestOpenCodeConfigurationPreservesExplicitOriginsAndRejectsAmbiguity(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	want := []string{"HOME=/owner", "XDG_DATA_HOME=/native/data", "XDG_CONFIG_HOME=/native/config", "OPENCODE_CONFIG=/project/native.json", "OPENCODE_DISABLE_PROJECT_CONFIG=true"}
	got, err := d.OpenCodeEnvironment(append(append([]string(nil), want...), "UNRELATED_SECRET=do-not-copy"))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("native origins changed: %q %v", got, err)
	}
	for name, env := range map[string][]string{
		"inline config":    {"OPENCODE_CONFIG_CONTENT={\"apiKey\":\"synthetic-secret\"}"},
		"custom tui":       {"OPENCODE_TUI_CONFIG=/owner/tui.json"},
		"pure":             {"OPENCODE_PURE=true"},
		"unknown selector": {"OPENCODE_UNKNOWN_POLICY=synthetic-secret"},
		"duplicate":        {"XDG_DATA_HOME=/one", "XDG_DATA_HOME=/two"},
		"relative":         {"OPENCODE_CONFIG=relative.json"},
		"control":          {"XDG_CONFIG_HOME=/owner\nconfig"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := d.OpenCodeEnvironment(env)
			if err == nil {
				t.Fatal("unsupported origin silently dropped")
			}
			if strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("refusal exposed config value")
			}
		})
	}
}

func TestOpenCodeUnsupportedOriginSurvivesDaemonEnvironmentSave(t *testing.T) {
	old := daemonEnviron
	defer func() { daemonEnviron = old }()
	daemonEnviron = func() []string {
		return []string{"HOME=/owner", "OPENCODE_CONFIG_CONTENT=synthetic-secret", "XDG_DATA_HOME=/native"}
	}
	dir := t.TempDir()
	if err := writeSavedEnv(dir); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadSavedEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(saved, "\n"), "synthetic-secret") {
		t.Fatal("unsupported config content saved")
	}
	d := &Daemon{savedEnv: saved}
	if _, err := d.OpenCodeEnvironment(nil); err == nil {
		t.Fatal("restart forgot unsupported native origin")
	}
}
