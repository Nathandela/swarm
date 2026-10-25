package shim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

const claudeRunTestConfigEnv = "SWARM_TEST_CLAUDE_RUN_CONFIG"

// TestMain dispatches before building the fake agent. Only this dedicated shim
// process installs subreaper custody; the shared test process never does.
func runClaudeRefusalHelper(path string) {
	fail := func(err error) { fmt.Fprintln(os.Stderr, "owned refusal helper:", err); os.Exit(1) }
	raw, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fail(err)
	}
	started, err := procstart.StartTime(os.Getpid())
	if err != nil {
		fail(err)
	}
	identity, _ := json.Marshal(processcontain.Identity{PID: os.Getpid(), StartTime: started})
	if err := os.WriteFile(path+".identity", identity, 0600); err != nil {
		fail(err)
	}
	if _, err := Run(cfg); !errors.Is(err, errLoginResolution) {
		fail(fmt.Errorf("expected pre-start login refusal, got %v", err))
	}
}

func runClaudeRefusalChild(t *testing.T, cfg Config) processcontain.Identity {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, selfExe(t))
	child.Env = replaceEnv(replaceEnv(os.Environ(), "HOME", dir), claudeRunTestConfigEnv, path)
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("actual Run refusal child: %v %s", err, output)
	}
	raw, err = os.ReadFile(path + ".identity")
	if err != nil {
		t.Fatal(err)
	}
	var identity processcontain.Identity
	if json.Unmarshal(raw, &identity) != nil || identity.PID != child.Process.Pid || identity.StartTime <= 0 {
		t.Fatal("refusal child did not capture its exact kernel identity")
	}
	return identity
}

func TestClaudeRunRefusalsPublishRemovableOwnedProof(t *testing.T) {
	for _, kind := range []string{"missing-image", "changed-image", "unqualified-image", "auth-env", "stale-epoch"} {
		t.Run(kind, func(t *testing.T) {
			cfg, _, birth, login := claudeBirthFixture(t)
			cfg.Env = helperEnv(modeInfo, "PATH=/usr/bin:/bin")
			switch kind {
			case "missing-image":
				if err := os.Remove(cfg.Argv[0]); err != nil {
					t.Fatal(err)
				}
			case "changed-image":
				replacement := cfg.Argv[0] + ".replacement"
				if err := os.WriteFile(replacement, []byte("#!/bin/sh\nexit 97\n"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, cfg.Argv[0]); err != nil {
					t.Fatal(err)
				}
			case "auth-env":
				cfg.Env = append(cfg.Env, "ANTHROPIC_API_KEY=synthetic-refusal")
			case "stale-epoch":
				r, err := birth.store.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := birth.store.SelectClaudeProfile(r.Revision, login.ID); err != nil {
					t.Fatal(err)
				}
			}
			owner := runClaudeRefusalChild(t, cfg)
			raw, err := os.ReadFile(filepath.Join(cfg.SessionDir, NativeProcessFile))
			if err != nil {
				t.Fatalf("ordinary refusal has no owned native scope: %v", err)
			}
			var native NativeProcessInfo
			if json.Unmarshal(raw, &native) != nil || native.SchemaVersion != ClaudeNativeWriterSchemaVersion || native.ShimPID != owner.PID || native.ShimStartTime != owner.StartTime || native.Runtime != *cfg.LoginRuntime || native.SessionID != cfg.SessionID || native.Cwd != cfg.Cwd || native.CLIFingerprint != cfg.CLIIdentity.Fingerprint || native.PID != 0 || native.StartTime != 0 || native.PGID != 0 || native.BackendPID != 0 {
				t.Fatal("ordinary refusal lost exact zero-PID custody")
			}
			stoppedRaw, err := os.ReadFile(filepath.Join(cfg.SessionDir, NativeStoppedFile))
			if err != nil {
				t.Fatal(err)
			}
			var stopped NativeStoppedInfo
			if json.Unmarshal(stoppedRaw, &stopped) != nil || stopped.SchemaVersion != ClaudeNativeWriterSchemaVersion || stopped.Native != native || !stopped.WritersStopped || !stopped.DescendantsContained {
				t.Fatal("actual refused shim lacks positive contained stop proof")
			}
			// A late new shim must neither adopt custody nor replace the original proof.
			_ = runClaudeRefusalChild(t, cfg)
			for name, want := range map[string][]byte{NativeProcessFile: raw, NativeStoppedFile: stoppedRaw} {
				got, err := os.ReadFile(filepath.Join(cfg.SessionDir, name))
				if err != nil || string(got) != string(want) {
					t.Fatal("late duplicate overwrote original refusal proof")
				}
			}
			disk, err := persist.NewStore(cfg.LoginStateRoot)
			if err != nil {
				t.Fatal(err)
			}
			retired, err := disk.RetireClaudeReservation(cfg.SessionID, nil)
			if err != nil || retired.ClaudeRetirement == nil || retired.ClaudeRetirement.Source != logins.ClaudeRetirementStopped || retired.ClaudeRetirement.Native != native {
				t.Fatalf("ordinary refused Run not removable after exact owner exit: %v", err)
			}
			if err := disk.Delete(cfg.SessionID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
