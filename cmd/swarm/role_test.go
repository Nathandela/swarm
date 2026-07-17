package main

// F8 — exec-level test of the `swarm shim --config <path>` role: build the
// swarm binary and the fake agent, launch the shim against a scripted agent,
// and assert the process exits with the agent's code and the session dir holds
// the G3 side-files.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shim"
)

func buildBinary(t *testing.T, out, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build %s: %v", pkg, err)
	}
}

func TestRunShim_ExecLevelLaunchesAgentAndPersists(t *testing.T) {
	binDir := t.TempDir()
	swarmBin := filepath.Join(binDir, "swarm")
	fakeAgent := filepath.Join(binDir, "swarm-fake-agent")
	buildBinary(t, swarmBin, "github.com/Nathandela/swarm/cmd/swarm")
	buildBinary(t, fakeAgent, "github.com/Nathandela/swarm/cmd/swarm-fake-agent")

	scriptPath := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(scriptPath, []byte("print role-ok\nexit 5\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}

	sessionDir := t.TempDir()
	// UNIX socket paths are length-capped, so keep the dir short.
	sockDir, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatalf("mktemp socket dir: %v", err)
	}
	defer os.RemoveAll(sockDir)
	socketPath := filepath.Join(sockDir, "s")

	launch := shimLaunchConfig{
		SessionID:  "role-test",
		Argv:       []string{fakeAgent, scriptPath},
		Cwd:        t.TempDir(),
		Env:        []string{"PATH=" + os.Getenv("PATH")},
		SocketPath: socketPath,
		SessionDir: sessionDir,
		Cols:       80,
		Rows:       24,
		GraceMS:    5000,
	}
	cfgPath := filepath.Join(t.TempDir(), "launch.json")
	data, err := json.Marshal(launch)
	if err != nil {
		t.Fatalf("marshal launch config: %v", err)
	}
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatalf("write launch config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, swarmBin, "shim", "--config", cfgPath)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	var ee *exec.ExitError
	if !errors.As(runErr, &ee) {
		t.Fatalf("swarm shim run err = %v, want an ExitError carrying the agent's code; stderr:\n%s", runErr, stderr.String())
	}
	if ee.ExitCode() != 5 {
		t.Errorf("swarm shim exit = %d, want 5 (the agent's exit code); stderr:\n%s", ee.ExitCode(), stderr.String())
	}

	for _, name := range []string{shim.SnapshotFile, shim.ExitFile, shim.TranscriptFile} {
		if _, err := os.Stat(filepath.Join(sessionDir, name)); err != nil {
			t.Errorf("side-file %s missing after shim role run: %v", name, err)
		}
	}
}
