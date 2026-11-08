package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestManagerManagedAccountEnvironmentReachesPreflight(t *testing.T) {
	for _, mutation := range []string{"none", "environment", "runtime", "provider", "state-root", "without-proof"} {
		t.Run(mutation, func(t *testing.T) {
			f := newLoginFixture(t)
			spec := daemon.LaunchSpec{AgentType: "codex", Cwd: t.TempDir(), ClientEnv: []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}, Manager: &daemon.ManagerLaunchSpec{ConversationID: "manager"}, CLIIdentity: &persist.CLIIdentity{Path: os.Args[0], Version: "0.167.0"}, Argv: []string{os.Args[0]}, Cols: 80, Rows: 24}
			if err := f.m.d.api.bindLoginLaunch(&spec); err != nil {
				t.Fatal(err)
			}
			if spec.LoginRuntime == nil {
				t.Fatal("fixture did not select a managed account")
			}
			dir, err := os.MkdirTemp("", "mml-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			effects := 0
			stop := errors.New("fixture stops before worktree or native spawn")
			cfg := daemon.Config{StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"), LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log"), ShimBinary: os.Args[0], MaxSessions: 4, PreLaunch: func(string, daemon.LaunchSpec) (string, error) { effects++; return "", stop }}
			core, err := daemon.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = core.Close() })
			spec.Manager.Command = cfg.ShimBinary
			switch mutation {
			case "environment":
				spec.ClientEnv = append(spec.ClientEnv, "OPENAI_API_KEY=synthetic-secret")
			case "runtime":
				changed := *spec.LoginRuntime
				changed.Epoch++
				spec.LoginRuntime = &changed
			case "provider":
				spec.AgentType = "claude"
				identity := *spec.CLIIdentity
				identity.Version = "2.1.319"
				spec.CLIIdentity = &identity
			case "state-root":
				spec.LoginStateRoot = t.TempDir()
			case "without-proof":
				spec = daemon.LaunchSpec{Manager: spec.Manager, AgentType: spec.AgentType, Cwd: spec.Cwd, Argv: spec.Argv, ClientEnv: spec.ClientEnv, CLIIdentity: spec.CLIIdentity, LoginRuntime: spec.LoginRuntime, LoginStateRoot: spec.LoginStateRoot, AuthIdentity: spec.AuthIdentity}
			}
			_, err = core.Launch(spec)
			if mutation != "none" {
				if err == nil || effects != 0 || len(core.List()) != 0 {
					t.Fatalf("mutated proof had launch effects: count=%d sessions=%d err=%v", effects, len(core.List()), err)
				}
				return
			}
			if !errors.Is(err, stop) || effects != 1 {
				t.Fatalf("trusted native account never reached preflight: count=%d err=%v", effects, err)
			}
		})
	}
}

func TestManagerLaunchVersionQualificationPrecedesLoginPreparation(t *testing.T) {
	for _, version := range []string{"0.166.0", "0.167.0"} {
		t.Run(version, func(t *testing.T) {
			f := newLoginFixture(t)
			if err := f.m.store.Close(); err != nil {
				t.Fatal(err)
			}
			program := filepath.Join(t.TempDir(), "codex")
			if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf 'codex-cli "+version+"\\n'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			spec := daemon.LaunchSpec{AgentType: "codex", Argv: []string{program}, Cwd: t.TempDir(), ClientEnv: []string{"HOME=" + t.TempDir()}, Manager: &daemon.ManagerLaunchSpec{ConversationID: "manager"}}
			_, err := f.m.d.api.launchResolved(spec)
			if err == nil {
				t.Fatal("fixture must never launch a native process")
			}
			qualifiedError := strings.Contains(err.Error(), "qualified")
			if qualifiedError != (version != "0.167.0") {
				t.Fatalf("version gate did not precede login preparation: %v", err)
			}
		})
	}
}

func TestManagerManagedBindingCannotEraseRawUnsupportedOwnerOrigin(t *testing.T) {
	for _, selector := range []string{"CODEX_HOME=/untrusted/owner", "OPENAI_API_KEY=synthetic-secret"} {
		t.Run(selector[:5], func(t *testing.T) {
			f := newLoginFixture(t)
			spec := daemon.LaunchSpec{AgentType: "codex", Cwd: t.TempDir(), ClientEnv: []string{"HOME=" + t.TempDir(), selector}, Manager: &daemon.ManagerLaunchSpec{ConversationID: "manager"}}
			if err := f.m.d.api.bindLoginLaunch(&spec); err == nil {
				t.Fatal("managed binding erased unsupported raw owner origin")
			}
		})
	}
}
