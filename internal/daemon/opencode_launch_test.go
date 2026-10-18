package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
)

func TestOpenCodeLaunchReservesExactRootBeforeAnyNativeExecution(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	pidFile := filepath.Join(t.TempDir(), "native.pid")
	spec := announceSpec(t, pidFile)
	spec.AgentType = "opencode"
	spec.RequiredRuntime = persist.RequiredRuntimeOpenCode
	spec.InitialPrompt = "genuine initial input"
	spec.Argv = append(spec.Argv, "--model", "opencode/deepseek-v4.1-flash-free", "--agent", "plan", "--prompt", spec.InitialPrompt)
	spec.OpenCodeEnv = []string{"HOME=" + t.TempDir(), "XDG_DATA_HOME=" + t.TempDir()}
	var reserved persist.Meta
	_, err := d.launch(spec, func(phase launchPhase, m persist.Meta) error {
		if phase == phaseReserved {
			reserved = m
			return errInjectedCrash
		}
		return nil
	})
	if !errors.Is(err, errInjectedCrash) {
		t.Fatalf("reservation failed: %v", err)
	}
	if !adapter.IsOpenCodeConversationID(reserved.ConversationID) || reserved.OpenCodeBinding == nil || reserved.OpenCodeBinding.Phase != persist.OpenCodePhasePlanned {
		t.Fatalf("missing preallocated root: %+v", reserved)
	}
	if reserved.OpenCodeBinding.Initial == nil || reserved.OpenCodeBinding.Initial.Phase != persist.OpenCodeInitialPrepared || reserved.OpenCodeBinding.Initial.MessageID != "" {
		t.Fatal("initial custody was not prepared without invented receipt")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatal("native process ran before root persistence")
	}
	loaded, err := d.store.Load(reserved.ID)
	if err != nil || loaded.ConversationID != reserved.ConversationID {
		t.Fatal("root not durable", err)
	}
	var binding opencodebridge.Binding
	path := filepath.Join(cfg.StateDir, reserved.ID, shim.OpenCodeBindingFile)
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &binding) != nil {
		t.Fatal("private bridge binding missing", err)
	}
	if binding.RootID != reserved.ConversationID || binding.Directory != reserved.ProviderCwd() || binding.BackendGeneration != reserved.OpenCodeBinding.Generation || binding.SessionInstance != binding.BackendGeneration || !binding.InitialPrompt {
		t.Fatalf("private custody differs: %+v", binding)
	}
	metaRaw, err := os.ReadFile(filepath.Join(cfg.StateDir, reserved.ID, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metaRaw), binding.Token) || strings.Contains(string(metaRaw), spec.InitialPrompt) {
		t.Fatal("secret or prompt leaked into metadata")
	}
	for _, name := range []string{shim.OpenCodeBindingFile, "opencode-tui.mjs", "opencode-tui.json"} {
		info, err := os.Stat(filepath.Join(cfg.StateDir, reserved.ID, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private artifact %s not 0600: %v", name, err)
		}
	}
	reserved.OpenCodeBinding.ProfileEnv[0] = "HOME=/changed"
	live, _ := d.Get(reserved.ID)
	if live.OpenCodeBinding.ProfileEnv[0] == "HOME=/changed" {
		t.Fatal("launch probe metadata aliases core")
	}
}

func TestOpenCodePlanWithholdsPromptPreservesFlagsAndHasNoBackend(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	dir := filepath.Join(cfg.StateDir, "planned")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := LaunchSpec{AgentType: "opencode", RequiredRuntime: persist.RequiredRuntimeOpenCode, Cwd: t.TempDir(), Argv: []string{"/native/opencode", "--model", "opencode/model", "--agent", "plan", "--prompt", "hello"}, InitialPrompt: "hello", OpenCodeEnv: []string{"XDG_DATA_HOME=/native"}}
	m := persist.Meta{ID: "planned", AgentType: "opencode", Cwd: spec.Cwd, RequiredRuntime: persist.RequiredRuntimeOpenCode}
	if err := d.prepareOpenCodeLaunch(&spec, &m, dir); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.Argv, []string{"/native/opencode", "--model", "opencode/model", "--agent", "plan"}) {
		t.Fatalf("native flags changed: %q", spec.Argv)
	}
	if spec.Backend != nil || spec.OpenCodeLaunch == nil {
		t.Fatal("standalone runtime lost its shim embargo or gained a backend")
	}
	if !slices.Equal(m.OpenCodeBinding.ProfileEnv, spec.OpenCodeEnv) {
		t.Fatal("profile origin was dropped")
	}
}

func TestOpenCodeInitialEncodedBoundIncludesRequestedChoices(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	dir := t.TempDir()
	spec := LaunchSpec{AgentType: "opencode", RequiredRuntime: persist.RequiredRuntimeOpenCode, Cwd: t.TempDir(), Argv: []string{"/native/opencode"}, InitialPrompt: strings.Repeat("\x01", 10800), Options: map[string]string{"model": "opencode/" + strings.Repeat("m", 503), "agent": strings.Repeat("a", 128)}, OpenCodeEnv: []string{}}
	m := persist.Meta{ID: "native", AgentType: "opencode", Cwd: spec.Cwd}
	if d.prepareOpenCodeLaunch(&spec, &m, dir) == nil {
		t.Fatal("oversized initial envelope was deferred until after native launch")
	}
}
