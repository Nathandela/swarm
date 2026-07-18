package detect

// v0.5 (bead agents-tracker-e5i) — model discovery. The launch form pre-fills the
// model each CLI is ACTUALLY configured to use and cycles its real choices, read
// best-effort from the CLI's on-disk config. These tests pin the pure parsers
// (real-shaped fixtures, incl. hidden-visibility filtering and priority sort), the
// size cap, the env-resolved probe against temp dirs, and the graceful fallback
// when a config is absent or corrupt.

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return data
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// wantCodexModels is the expected discovery from the fixture: the four
// visibility=="list" models in ascending-priority order (the fixture lists them
// out of order and mixes in two hidden entries).
var wantCodexModels = []adapter.ModelChoice{
	{ID: "gpt-5.6-sol", Display: "GPT-5.6-Sol"},
	{ID: "gpt-5.6-terra", Display: "GPT-5.6-Terra"},
	{ID: "gpt-5.6-luna", Display: "GPT-5.6-Luna"},
	{ID: "gpt-5.5", Display: "GPT-5.5"},
}

func TestTOMLTopLevelString(t *testing.T) {
	cfg := readTestdata(t, "codex_config.toml")
	if got := tomlTopLevelString(cfg, "model"); got != "gpt-5.6-sol" {
		// The `model` inside [profiles.fast] must NOT win — only the bare top-level key.
		t.Errorf("tomlTopLevelString(model) = %q, want %q (top-level only, not the table key)", got, "gpt-5.6-sol")
	}

	cases := []struct {
		name string
		toml string
		key  string
		want string
	}{
		{"double-quoted", `model = "gpt-5.6-sol"`, "model", "gpt-5.6-sol"},
		{"single-quoted", `model = 'gpt-5.5'`, "model", "gpt-5.5"},
		{"trailing comment", `model = "gpt-5.5"  # my default`, "model", "gpt-5.5"},
		{"commented out ignored", "# model = \"nope\"\nmodel = \"real\"", "model", "real"},
		{"absent key", "other = 1\n", "model", ""},
		{"key only under a table is not top-level", "[profiles.x]\nmodel = \"x\"\n", "model", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := tomlTopLevelString([]byte(c.toml), c.key); got != c.want {
				t.Errorf("tomlTopLevelString(%q) = %q, want %q", c.toml, got, c.want)
			}
		})
	}
}

func TestParseCodexModelsCache(t *testing.T) {
	got := parseCodexModelsCache(readTestdata(t, "codex_models_cache.json"))
	if !reflect.DeepEqual(got, wantCodexModels) {
		t.Errorf("parseCodexModelsCache =\n  %+v\nwant (list-only, priority-sorted):\n  %+v", got, wantCodexModels)
	}

	if got := parseCodexModelsCache([]byte("{not valid json")); got != nil {
		t.Errorf("corrupt cache must yield nil, got %+v", got)
	}
	if got := parseCodexModelsCache([]byte(`{"models":[]}`)); got != nil {
		t.Errorf("empty models must yield nil, got %+v", got)
	}
	if got := parseCodexModelsCache([]byte(`{"models":[{"slug":"x","display_name":"X","visibility":"hide","priority":1}]}`)); got != nil {
		t.Errorf("an all-hidden cache must yield nil, got %+v", got)
	}
}

func TestClaudeModelFromJSON(t *testing.T) {
	if got := claudeModelFromJSON(readTestdata(t, "claude_settings.json")); got != "fable" {
		t.Errorf("claudeModelFromJSON = %q, want %q", got, "fable")
	}
	if got := claudeModelFromJSON([]byte(`{"env":{}}`)); got != "" {
		t.Errorf("settings without a model key must yield \"\", got %q", got)
	}
	if got := claudeModelFromJSON([]byte("{ corrupt")); got != "" {
		t.Errorf("corrupt settings must yield \"\", got %q", got)
	}
}

func TestReadCapped(t *testing.T) {
	dir := t.TempDir()

	small := filepath.Join(dir, "small.txt")
	writeFile(t, small, []byte("hello"))
	if data, ok := readCapped(small); !ok || string(data) != "hello" {
		t.Errorf("readCapped(small) = (%q, %v), want (\"hello\", true)", data, ok)
	}

	if _, ok := readCapped(filepath.Join(dir, "absent.txt")); ok {
		t.Error("readCapped of an absent file must report ok=false")
	}

	big := filepath.Join(dir, "big.txt")
	writeFile(t, big, bytes.Repeat([]byte("a"), maxConfigSize+4096))
	data, ok := readCapped(big)
	if !ok {
		t.Fatal("readCapped(big) ok=false")
	}
	if len(data) != maxConfigSize {
		t.Errorf("readCapped(big) len = %d, want the %d-byte cap", len(data), maxConfigSize)
	}
}

func TestProbeModels_Codex(t *testing.T) {
	t.Run("CODEX_HOME takes precedence", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		writeFile(t, filepath.Join(home, "config.toml"), readTestdata(t, "codex_config.toml"))
		writeFile(t, filepath.Join(home, "models_cache.json"), readTestdata(t, "codex_models_cache.json"))

		configured, models := ProbeModels("codex")
		if configured != "gpt-5.6-sol" {
			t.Errorf("configured = %q, want %q", configured, "gpt-5.6-sol")
		}
		if !reflect.DeepEqual(models, wantCodexModels) {
			t.Errorf("models =\n  %+v\nwant\n  %+v", models, wantCodexModels)
		}
	})

	t.Run("falls back to HOME/.codex when CODEX_HOME unset", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", "")
		t.Setenv("HOME", home)
		dot := filepath.Join(home, ".codex")
		writeFile(t, filepath.Join(dot, "config.toml"), readTestdata(t, "codex_config.toml"))
		writeFile(t, filepath.Join(dot, "models_cache.json"), readTestdata(t, "codex_models_cache.json"))

		configured, models := ProbeModels("codex")
		if configured != "gpt-5.6-sol" || len(models) != 4 {
			t.Errorf("HOME fallback: configured=%q models=%d, want gpt-5.6-sol / 4", configured, len(models))
		}
	})

	t.Run("config present but cache absent yields the configured default and no choices", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		writeFile(t, filepath.Join(home, "config.toml"), readTestdata(t, "codex_config.toml"))

		configured, models := ProbeModels("codex")
		if configured != "gpt-5.6-sol" {
			t.Errorf("configured = %q, want %q", configured, "gpt-5.6-sol")
		}
		if models != nil {
			t.Errorf("absent cache must yield nil models, got %+v", models)
		}
	})

	t.Run("empty config dir yields empty results", func(t *testing.T) {
		t.Setenv("CODEX_HOME", t.TempDir())
		configured, models := ProbeModels("codex")
		if configured != "" || models != nil {
			t.Errorf("absent config: got (%q, %+v), want (\"\", nil)", configured, models)
		}
	})
}

func TestProbeModels_Claude(t *testing.T) {
	t.Run("reads the configured default, no enumeration", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeFile(t, filepath.Join(home, ".claude", "settings.json"), readTestdata(t, "claude_settings.json"))

		configured, models := ProbeModels("claude")
		if configured != "fable" {
			t.Errorf("configured = %q, want %q", configured, "fable")
		}
		if models != nil {
			t.Errorf("claude has no model enumeration; models must be nil, got %+v", models)
		}
	})

	t.Run("absent settings yields empty", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		configured, models := ProbeModels("claude")
		if configured != "" || models != nil {
			t.Errorf("absent settings: got (%q, %+v), want (\"\", nil)", configured, models)
		}
	})

	t.Run("corrupt settings yields empty", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeFile(t, filepath.Join(home, ".claude", "settings.json"), []byte("{ not json"))
		configured, _ := ProbeModels("claude")
		if configured != "" {
			t.Errorf("corrupt settings must yield \"\", got %q", configured)
		}
	})
}

func TestProbeModels_UnknownName(t *testing.T) {
	configured, models := ProbeModels("reference")
	if configured != "" || models != nil {
		t.Errorf("unknown adapter: got (%q, %+v), want (\"\", nil)", configured, models)
	}
}
