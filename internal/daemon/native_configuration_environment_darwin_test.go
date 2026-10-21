//go:build darwin

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestLaunchManagedClaudeRefusesUnsupportedDescendantContainment(t *testing.T) {
	cfg := daemonConfig(t)
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
		spec.AccountClaudeFallback = &persist.ClaudeFallbackPolicy{}
		return spec, nil
	}
	d := openDaemon(t, cfg)
	envFile := filepath.Join(canonicalTempDir(t), "child-env-must-not-exist.txt")
	_, err = d.Launch(LaunchSpec{
		AgentType:   "claude",
		Argv:        []string{selfExe(t), markerEnvDump, envFile},
		Cwd:         home,
		ClientEnv:   []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")},
		CLIIdentity: &persist.CLIIdentity{Path: selfExe(t), Version: "2.1.289", Fingerprint: fingerprint},
		Cols:        80,
		Rows:        24,
	})
	if err == nil || !strings.Contains(err.Error(), "did not confirm serving") {
		t.Fatalf("unsupported managed launch error = %v, want serving refusal", err)
	}
	log, readErr := os.ReadFile(cfg.LogPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(log), "managed descendant containment unavailable") {
		t.Fatalf("shim log did not record unsupported containment refusal: %s", log)
	}
	if _, statErr := os.Stat(envFile); !os.IsNotExist(statErr) {
		t.Fatalf("child environment marker exists after refused launch: %v", statErr)
	}
}
