package accountconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// A rotation or CLI-refresh successor pins the native model; owner drift since
// the source launched re-projects instead of refusing.
func TestPrepareNative_PinnedRecoveryAfterOwnerDriftReprojects(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			var f projectionFixture
			if provider == "codex" {
				f = codexContextFixture(t)
			} else {
				f = fixture(t, provider)
				put(t, filepath.Join(f.source, "settings.json"), driftOwnerClaudeSettings)
			}
			argv := []string{provider, "--model", "exact-model"}
			p, err := PrepareNative(f.root, provider, f.candidate, f.cwd, f.env, argv, "", "", codexContextLease, true)
			if err != nil {
				t.Fatal(err)
			}
			if provider == "codex" {
				put(t, filepath.Join(f.source, "config.toml"), "model_reasoning_effort=\"medium\"\n")
			} else {
				put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"low"}`)
			}
			q, err := PrepareNative(f.root, provider, f.candidate, f.cwd, p.HarmlessEnv, argv, p.Ref, "exact-model", codexContextLease, false)
			if err != nil {
				t.Fatal("pinned recovery refused owner drift:", err)
			}
			if q.Ref == p.Ref {
				t.Fatal("pinned recovery after owner drift kept the stale projection")
			}
			if _, err := PrepareNative(f.root, provider, f.candidate, f.cwd, p.HarmlessEnv, argv, p.Ref, "other-model", codexContextLease, false); err == nil {
				t.Fatal("pinned recovery admitted a model that differs from the proof")
			}
		})
	}
}

func TestPrepareNative_ClaudeOwnerEnvRefusesOnlyCredentialRouting(t *testing.T) {
	for entry, refused := range map[string]bool{
		`"BASH_DEFAULT_TIMEOUT_MS":"600000"`:        false,
		`"DISABLE_TELEMETRY":"1"`:                   false,
		`"ANTHROPIC_AUTH_TOKEN":"synthetic"`:        true,
		`"ANTHROPIC_BASE_URL":"https://proxy.test"`: true,
		`"CLAUDE_CODE_USE_BEDROCK":"1"`:             true,
		`"CLAUDE_CODE_USE_VERTEX":"1"`:              true,
		`"AWS_PROFILE":"synthetic"`:                 true,
	} {
		t.Run(entry, func(t *testing.T) {
			f := fixture(t, "claude")
			put(t, filepath.Join(f.source, "settings.json"), `{"env":{`+entry+`}}`)
			_, err := PrepareNative(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "", "", codexContextLease, true)
			if refused != (err != nil) {
				t.Fatalf("owner env %s: refused=%v, want %v (%v)", entry, err != nil, refused, err)
			}
		})
	}
}

// The owner changed again after an interrupted refresh: the stale intent is
// superseded and the refresh completes with the current owner configuration.
func TestClaudeContextInterruptedRefreshIsSupersededByLaterOwnerChange(t *testing.T) {
	f := fixture(t, "claude")
	put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"high"}`)
	old, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"medium"}`)
	other := filepath.Join(f.root, "interrupted")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	stale, err := PrepareClaudeContext(other, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeClaudeContextUpdate(f.candidate, claudeContextUpdate{1, old, stale}); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"low"}`)
	next, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, codexContextLease, true)
	if err != nil || next.GlobalGeneration == stale.GlobalGeneration {
		t.Fatal("interrupted refresh wedged after a later owner change:", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(f.candidate, "settings.json")); string(raw) != `{"effortLevel":"low"}` {
		t.Fatalf("superseded refresh did not install the current owner settings: %s", raw)
	}
	if _, err := os.Lstat(filepath.Join(f.candidate, ClaudeContextUpdateMarker)); !os.IsNotExist(err) {
		t.Fatal("superseded intent remains")
	}
}
