package skeleton

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestManagedLaunchExecutesProbedTargetAfterLauncherUpdate(t *testing.T) {
	dir := t.TempDir()
	old, next, launcher := filepath.Join(dir, "old"), filepath.Join(dir, "next"), filepath.Join(dir, "codex")
	for path, version := range map[string]string{old: "codex-cli 0.163.0", next: "codex-cli 0.164.0"} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(old, launcher); err != nil {
		t.Fatal(err)
	}
	spec := daemon.LaunchSpec{AgentType: "codex", Argv: []string{launcher, "--version"}, ClientEnv: []string{"PATH=" + dir}, Backend: &daemon.BackendSpec{Program: launcher}}
	observed, err := probeCLIIdentity(spec.AgentType, launcher, spec.ClientEnv, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = pinProbedCLIExecutable(&spec, observed); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(next, launcher); err != nil {
		t.Fatal(err)
	}
	historical := *observed
	historical.Path = launcher
	if sameProbedCLIInstallation(&historical, observed) {
		t.Fatal("retargeted historical launcher repaired old identity")
	}
	for _, path := range []string{spec.Argv[0], spec.Backend.Program} {
		output, err := exec.Command(path, "--version").Output()
		if err != nil || strings.TrimSpace(string(output)) != "codex-cli 0.163.0" {
			t.Fatal("native launch followed updated launcher instead of probed target")
		}
	}
}

func TestOpenCodeLaunchExecutesProbedTargetAfterLauncherUpdate(t *testing.T) {
	dir := t.TempDir()
	old, next, launcher := filepath.Join(dir, "old"), filepath.Join(dir, "next"), filepath.Join(dir, "opencode")
	for path, version := range map[string]string{old: "1.19.3", next: "1.19.4"} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(old, launcher); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(launcher)
	if err != nil {
		t.Fatal(err)
	}
	spec := daemon.LaunchSpec{AgentType: "opencode", Argv: []string{launcher}, ClientEnv: []string{"PATH=" + dir}, Cwd: dir, ExpectedCLIIdentity: &persist.CLIIdentity{Path: launcher, Version: "1.19.3", Fingerprint: fingerprint}}
	if err := (&coreAPI{}).qualifyOpenCodeLaunch(&spec); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(next, launcher); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(spec.Argv[0], "--version").Output()
	if err != nil || strings.TrimSpace(string(output)) != "1.19.3" {
		t.Fatal("OpenCode launch followed mutable launcher")
	}
}

func TestLateBackendPlannerUsesPinnedAgentTargetAfterLauncherUpdate(t *testing.T) {
	dir := t.TempDir()
	old, next, launcher := filepath.Join(dir, "old"), filepath.Join(dir, "next"), filepath.Join(dir, "codex")
	for path, version := range map[string]string{old: "codex-cli 0.163.0", next: "codex-cli 0.164.0"} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(old, launcher); err != nil {
		t.Fatal(err)
	}
	spec := daemon.LaunchSpec{AgentType: "codex", Argv: []string{launcher}, ClientEnv: []string{"PATH=" + dir}}
	observed, err := probeCLIIdentity("codex", launcher, spec.ClientEnv, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = pinProbedCLIExecutable(&spec, observed); err != nil {
		t.Fatal(err)
	}
	if spec.Backend != nil {
		t.Fatal("fixture must use normal late planner path")
	}
	if err = os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(next, launcher); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{}
	plan, err := d.planSessionBackend("codex", dir, filepath.Join(dir, "native.sock"), dir, spec.ClientEnv, spec.Argv)
	if err != nil || plan == nil {
		t.Fatalf("late planner failed: %v", err)
	}
	output, err := exec.Command(plan.Program, "--version").Output()
	if err != nil || strings.TrimSpace(string(output)) != "codex-cli 0.163.0" {
		t.Fatal("late backend planner followed mutable PATH instead of pinned target")
	}
	if err = os.Chmod(old, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = d.planSessionBackend("codex", dir, filepath.Join(dir, "native.sock"), dir, spec.ClientEnv, spec.Argv); err == nil {
		t.Fatal("unexecutable pinned target fell back to PATH")
	}
}
