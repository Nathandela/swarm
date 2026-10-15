//go:build linux

package skeleton

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// Like newDirectoryLaunchFixture, but the assembly core is wired as Serve wires
// it, so prepareAccountLaunch sees the other discussions on the account.
func driftLaunchFixture(t *testing.T, provider string) (directoryLaunchFixture, *protocol.Client, string, string) {
	t.Helper()
	buildBinaries(t)
	m := accountTestManager(t, accountTestState(t))
	accountTestAdmit(t, m, accountTestCandidate(t, m, provider, "directory-account"))
	if _, err := m.store.SetEnabled(accountTestRegistry(t, m).Revision, provider, true); err != nil {
		t.Fatal(err)
	}
	home, cwd, env := accountTestLaunchEnvironment(t, m.stateRoot)
	version := "codex-cli 0.160.0"
	if provider == "claude" {
		version = "2.1.289 (Claude Code)"
	}
	// The executable only reports a pinned version and sleeps. It never
	// authenticates or contacts a provider.
	accountTestPut(t, filepath.Join(m.stateRoot, provider), []byte(fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%%s\\n' %q; exit; fi\nexec /bin/sleep 60\n", version)))
	if err := os.Chmod(filepath.Join(m.stateRoot, provider), 0o700); err != nil {
		t.Fatal(err)
	}
	env[1] = "PATH=" + m.stateRoot + ":/usr/bin:/bin"
	assembly := &Daemon{accounts: m}
	core := accountTestCore(t, m, func(cfg *daemon.Config) {
		cfg.ShimBinary = swarmBin
		cfg.PreLaunch = preLaunchWorktree
		cfg.PreDelete = preDeleteWorktree
		cfg.FinalizeLaunch = assembly.prepareAccountLaunch
	})
	assembly.core = core
	api := newCoreAPI(core, "", testEndpoint)
	api.accounts = m
	t.Cleanup(api.close)
	f := directoryLaunchFixture{api: api, manager: m, env: env}
	return f, configurationOwnerClient(t, api, m.stateRoot), home, cwd
}

func driftLaunch(t *testing.T, f directoryLaunchFixture, owner *protocol.Client, provider, cwd string) (string, error) {
	t.Helper()
	id, _, err := owner.Launch(protocol.LaunchReq{Agent: provider, Cwd: cwd + "/", Env: f.env, Cols: 80, Rows: 24})
	if err != nil {
		return "", err
	}
	_, local, _ := protocol.ParseID(id)
	t.Cleanup(func() { _ = f.api.core.Kill(local); _ = f.api.core.Delete(local) })
	return local, nil
}

func TestLaunch_AnIdleWriterOnTheSameAccountDoesNotBlockANewDiscussion(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			f, owner, home, cwd := driftLaunchFixture(t, provider)
			first, err := driftLaunch(t, f, owner, provider, cwd)
			if err != nil {
				t.Fatal(err)
			}
			meta, ok := f.api.core.Get(first)
			if !ok || meta.Status.Process != status.ProcessRunning {
				t.Fatal("first discussion is not running")
			}
			profile, err := f.manager.store.ProfilePath(*meta.AccountBinding)
			if err != nil {
				t.Fatal(err)
			}
			// The running native CLI writes its profile (/model) and the owner
			// changes the shared configuration (trust, settings).
			if provider == "codex" {
				raw, err := os.ReadFile(filepath.Join(profile, "config.toml"))
				if err != nil {
					t.Fatal(err)
				}
				accountTestPut(t, filepath.Join(profile, "config.toml"), append([]byte("model_verbosity = \"high\"\n"), raw...))
				owned, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
				if err != nil {
					t.Fatal(err)
				}
				accountTestPut(t, filepath.Join(home, ".codex", "config.toml"), append(owned, []byte("\n[projects.\"/synthetic/new-repo\"]\ntrust_level = \"trusted\"\n")...))
			} else {
				accountTestPut(t, filepath.Join(profile, "settings.json"), []byte(`{"fastMode":true}`))
				accountTestPut(t, filepath.Join(home, ".claude", "settings.json"), []byte(`{"effortLevel":"low"}`))
			}
			if _, err := driftLaunch(t, f, owner, provider, cwd); err != nil {
				t.Fatal("an idle writer or configuration drift blocked a new discussion:", err)
			}
		})
	}
}

func TestWriterHold_AKilledShimWithoutStoppedProofDoesNotBlock(t *testing.T) {
	m, source, _ := accountWriterProofFixture(t)
	// The shim was SIGKILLed: native-process.json exists, no stopped proof, and
	// every recorded process is gone.
	if err := verifyAccountWritersStopped(m.w.stateDir, source); err != nil {
		t.Fatal("dead writers without a stopped proof blocked:", err)
	}
	if err := os.Remove(filepath.Join(m.w.stateDir, source.ID, shim.NativeProcessFile)); err != nil {
		t.Fatal(err)
	}
	if err := verifyAccountWritersStopped(m.w.stateDir, source); err != nil {
		t.Fatal("a dead shim without native-process.json blocked:", err)
	}
}

func TestWriterHold_LiveProcessesStillHold(t *testing.T) {
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"running", "shim-live", "shim-live-without-native-file", "native-live", "native-group-live", "backend-live", "meta-shim-live", "unreadable-native-file"} {
		t.Run(kind, func(t *testing.T) {
			m, source, native := accountWriterProofFixture(t)
			switch kind {
			case "running":
				source.Status.Process = status.ProcessRunning
			case "shim-live", "shim-live-without-native-file":
				source.ShimPID, source.ShimStartTime = os.Getpid(), start
				native.ShimPID, native.ShimStartTime = source.ShimPID, source.ShimStartTime
			case "native-live":
				native.PID, native.PGID, native.StartTime = os.Getpid(), os.Getpid(), start
			case "native-group-live":
				// The leader is gone but a detached member of its group survives.
				native.PGID = syscall.Getpgrp()
			case "backend-live":
				native.BackendPID, native.BackendStartTime = os.Getpid(), start
			case "meta-shim-live":
				// The file names another (dead) shim; the live meta shim still holds.
				source.ShimPID, source.ShimStartTime = os.Getpid(), start
			}
			path := filepath.Join(m.w.stateDir, source.ID, shim.NativeProcessFile)
			writeAccountProofFixture(t, path, native)
			if kind == "unreadable-native-file" {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "shim-live-without-native-file" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := verifyAccountWritersStopped(m.w.stateDir, source); !errors.Is(err, accounts.ErrInUse) {
				t.Fatal("a live writer did not hold:", err)
			}
		})
	}
}
