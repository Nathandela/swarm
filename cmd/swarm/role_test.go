package main

// F8 / R2 — exec-level tests of the `swarm shim --config <path>` role: build the
// swarm binary and the fake agent, launch the shim against a scripted agent, and
// assert (a) it exits with the agent's code, (b) the session dir holds the G3
// side-files, and (c) the shim leads its own session (E4.1). A second case
// forces the setsid re-exec path and checks the agent's exit code still
// propagates.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

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

// buildRoleBinaries builds swarm + the fake agent into a temp dir.
func buildRoleBinaries(t *testing.T) (swarmBin, fakeAgent string) {
	t.Helper()
	dir := t.TempDir()
	swarmBin = filepath.Join(dir, "swarm")
	fakeAgent = filepath.Join(dir, "swarm-fake-agent")
	buildBinary(t, swarmBin, "github.com/Nathandela/swarm/cmd/swarm")
	buildBinary(t, fakeAgent, "github.com/Nathandela/swarm/cmd/swarm-fake-agent")
	return swarmBin, fakeAgent
}

// writeLaunchConfig writes a shim launch config that runs the fake agent through
// script, returning the config path and the session dir.
func writeLaunchConfig(t *testing.T, fakeAgent, script string) (cfgPath, sessionDir string) {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	sessionDir = t.TempDir()
	// UNIX socket paths are length-capped, so keep the dir short.
	sockDir, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatalf("mktemp socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })

	data, err := json.Marshal(shimLaunchConfig{
		SessionID:  "role-test",
		Argv:       []string{fakeAgent, scriptPath},
		Cwd:        t.TempDir(),
		Env:        []string{"PATH=" + os.Getenv("PATH")},
		SocketPath: filepath.Join(sockDir, "s"),
		SessionDir: sessionDir,
		Cols:       80,
		Rows:       24,
		GraceMS:    5000,
	})
	if err != nil {
		t.Fatalf("marshal launch config: %v", err)
	}
	cfgPath = filepath.Join(t.TempDir(), "launch.json")
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatalf("write launch config: %v", err)
	}
	return cfgPath, sessionDir
}

func assertSideFiles(t *testing.T, sessionDir string) {
	t.Helper()
	for _, name := range []string{shim.SnapshotFile, shim.ExitFile, shim.TranscriptFile} {
		if _, err := os.Stat(filepath.Join(sessionDir, name)); err != nil {
			t.Errorf("side-file %s missing after shim role run: %v", name, err)
		}
	}
}

func TestRunShim_LaunchesAgentPersistsAndLeadsSession(t *testing.T) {
	swarmBin, fakeAgent := buildRoleBinaries(t)
	// The idle keeps the shim alive long enough to observe its session id while
	// it is still running.
	cfgPath, sessionDir := writeLaunchConfig(t, fakeAgent, "print role-ok\nidle 2s\nexit 5\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, swarmBin, "shim", "--config", cfgPath)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start swarm shim: %v", err)
	}
	pid := cmd.Process.Pid

	// Spawned without a new group, the shim's in-place setsid succeeds, so it
	// becomes its own session leader: getsid(pid) == pid (E4.1 verified).
	if !becomesSessionLeader(pid, 3*time.Second) {
		t.Errorf("shim pid %d never became its own session leader (getsid != pid) — setsid was not guaranteed; stderr:\n%s", pid, stderr.String())
	}

	err := cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("swarm shim wait err = %v, want an ExitError; stderr:\n%s", err, stderr.String())
	}
	if ee.ExitCode() != 5 {
		t.Errorf("swarm shim exit = %d, want 5; stderr:\n%s", ee.ExitCode(), stderr.String())
	}
	assertSideFiles(t, sessionDir)
}

func TestRunShim_ReExecsToAcquireSessionWhenGroupLeader(t *testing.T) {
	swarmBin, fakeAgent := buildRoleBinaries(t)
	cfgPath, sessionDir := writeLaunchConfig(t, fakeAgent, "print role-ok\nexit 5\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, swarmBin, "shim", "--config", cfgPath)
	// Start the shim as a process-group leader (but NOT a session leader): its
	// in-place setsid then fails EPERM and it must re-exec itself with Setsid to
	// acquire a session. The agent's exit code must still propagate through the
	// re-exec, and the grandchild must produce the side-files.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()

	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("swarm shim run err = %v, want an ExitError; stderr:\n%s", err, stderr.String())
	}
	if ee.ExitCode() != 5 {
		t.Errorf("swarm shim exit = %d, want 5 (propagated through the setsid re-exec); stderr:\n%s", ee.ExitCode(), stderr.String())
	}
	assertSideFiles(t, sessionDir)
}

// becomesSessionLeader polls until getsid(pid) == pid (pid leads its session) or
// the deadline passes.
func becomesSessionLeader(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sid, err := unix.Getsid(pid); err == nil && sid == pid {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
