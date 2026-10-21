//go:build linux

package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestLaunchManagedClaudeInjectsDiagnosticsAndFallbackAfterPolicyFilter(t *testing.T) {
	for _, test := range []struct {
		name     string
		policy   string
		fallback string
	}{
		{"owner-unset", `{}`, ""},
		{"owner-false", `{"owner_value":"false"}`, "false"},
		{"owner-true", `{"owner_value":"true"}`, "true"},
		{"recovery-over-owner-false", `{"owner_value":"false","recovery_pinned":true}`, "true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := daemonConfig(t)
			// /tmp is a symlink on macOS; account configuration checks canonical paths.
			stateRoot, err := filepath.EvalSymlinks(cfg.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			cfg.StateDir = stateRoot
			binding, profile, home := nativeClaudeEnvironmentFixture(t, cfg.StateDir)
			fingerprint, err := persist.CLIFingerprint(selfExe(t))
			if err != nil {
				t.Fatal(err)
			}
			cfg.FinalizeLaunch = func(_ string, spec LaunchSpec) (LaunchSpec, error) {
				projection, err := accountconfig.PrepareNative(cfg.StateDir, "claude", profile, spec.Cwd, spec.ClientEnv, spec.Argv, "", "", func(_ string, install func() error) error { return install() }, true)
				if err != nil {
					return spec, err
				}
				bound := binding
				bound.ConfigurationGeneration = projection.Generation
				spec.AccountBinding = &bound
				spec.AccountProjectionRef = projection.Ref
				spec.AccountStateRoot = cfg.StateDir
				spec.AccountNativeContext = true
				var policy persist.ClaudeFallbackPolicy
				if err := json.Unmarshal([]byte(test.policy), &policy); err != nil {
					return spec, err
				}
				spec.AccountClaudeFallback = &policy
				return spec, nil
			}
			d := openDaemon(t, cfg)
			envFile := filepath.Join(t.TempDir(), "actual-child-env.txt")
			m, err := d.Launch(LaunchSpec{AgentType: "claude", Argv: []string{selfExe(t), markerEnvDump, envFile}, Cwd: canonicalTempDir(t), ClientEnv: []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CLAUDE_CODE_DIAGNOSTICS_FILE=/untrusted/diagnostics", "CLAUDE_CODE_NO_MODEL_FALLBACK=caller-must-not-win", "CODEX_HOME=/untrusted/codex", "CLAUDE_CONFIG_DIR=/untrusted/claude"}, CLIIdentity: &persist.CLIIdentity{Path: selfExe(t), Version: "2.1.289", Fingerprint: fingerprint}, Cols: 80, Rows: 24})
			if err != nil {
				log, _ := os.ReadFile(cfg.LogPath)
				t.Fatalf("%v\nshim log:\n%s", err, log)
			}
			t.Cleanup(func() { _ = d.Kill(m.ID) })
			waitFile(t, envFile, pollTimeout)
			raw, err := os.ReadFile(envFile)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(cfg.StateDir, m.ID, "claude-model-diagnostics.jsonl")
			if envValue(string(raw), "CLAUDE_CODE_DIAGNOSTICS_FILE") != want {
				t.Fatal("Swarm diagnostic route lost before actual child")
			}
			if envValue(string(raw), "CLAUDE_CODE_NO_MODEL_FALLBACK") != test.fallback {
				t.Fatal("explicit fallback policy lost or replaced by ambient value")
			}
			if envValue(string(raw), "CODEX_HOME") != "" || envValue(string(raw), "CLAUDE_CONFIG_DIR") != profile {
				t.Fatal("ambient credential profile reached child")
			}
			configRaw, err := os.ReadFile(filepath.Join(cfg.StateDir, m.ID, shimLaunchConfigFile))
			if err != nil {
				t.Fatal(err)
			}
			var config shimSpawnConfig
			if err := json.Unmarshal(configRaw, &config); err != nil {
				t.Fatal(err)
			}
			if lineIndex(config.Env, "CLAUDE_CODE_DIAGNOSTICS_FILE="+want) < 0 || envValue(strings.Join(config.Env, "\n"), "CLAUDE_CODE_NO_MODEL_FALLBACK") != test.fallback {
				t.Fatal("shim config and child policy disagree")
			}
			metaRaw, err := os.ReadFile(filepath.Join(cfg.StateDir, m.ID, "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			var meta persist.Meta
			if err := json.Unmarshal(metaRaw, &meta); err != nil || meta.AccountClaudeFallback == nil {
				t.Fatal("assembled launch did not persist fallback authority")
			}
			policyRaw, _ := json.Marshal(meta.AccountClaudeFallback)
			if strings.Contains(test.policy, `"recovery_pinned":true`) && !strings.Contains(string(policyRaw), `"recovery_pinned":true`) {
				t.Fatal("assembled recovery authority bit not persisted")
			}
		})
	}
}
