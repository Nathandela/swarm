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
// script, returning the config path, the session dir and the control socket path.
func writeLaunchConfig(t *testing.T, fakeAgent, script string) (cfgPath, sessionDir, socketPath string) {
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
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	socketPath = filepath.Join(sockDir, "s")
	data, err := json.Marshal(shimLaunchConfig{
		SessionID:  "role-test",
		Argv:       []string{fakeAgent, scriptPath},
		Cwd:        t.TempDir(),
		Env:        []string{"PATH=" + os.Getenv("PATH")},
		SocketPath: socketPath,
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
	return cfgPath, sessionDir, socketPath
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
	cfgPath, sessionDir, socketPath := writeLaunchConfig(t, fakeAgent, "print role-ok\nidle 2s\nexit 5\n")

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
	//
	// WAIT ON THE REAL CONDITION, NOT ON A DURATION (Wave R6 review round 3, finding
	// F5(b)). This assertion used to poll getsid for a fixed 3 s and report "setsid was
	// not guaranteed" if the answer had not arrived by then -- a bounded wait treated as
	// a sync point, measuring PROCESS STARTUP LATENCY, which this rig does not control.
	// Under load it fails on a shim that would have led its session perfectly well a
	// moment later: reproduced 6/6 at GOMAXPROCS=1 with 6x hw.ncpu busy loops
	// (docs/verification/r6-chat.md, Gates).
	//
	// The condition the assertion actually depends on is "the shim has got past
	// ensureSession". `cmd/swarm.runShim` calls ensureSession BEFORE shim.Run, and
	// shim.Run binds cfg.SocketPath as its first act (internal/shim/shim.go:113-120,
	// "Bind the socket first"), so the socket existing is proof that setsid has ALREADY
	// happened -- the ordering is in the production code, not in a timeout. Waiting for
	// it makes the getsid read below a single, deterministic observation, and its
	// deadline is a runaway guard rather than the measurement.
	if !awaitPath(socketPath, 25*time.Second) {
		t.Fatalf("shim pid %d never bound its control socket %s; it never reached shim.Run at all. stderr:\n%s",
			pid, socketPath, stderr.String())
	}
	if sid, err := unix.Getsid(pid); err != nil || sid != pid {
		t.Errorf("shim pid %d is serving its control socket but getsid = %d (err %v), want %d — "+
			"setsid was not guaranteed; stderr:\n%s", pid, sid, err, pid, stderr.String())
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
	cfgPath, sessionDir, _ := writeLaunchConfig(t, fakeAgent, "print role-ok\nexit 5\n")

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

// awaitPath waits for path to exist. It replaces becomesSessionLeader's poll for a
// PROPERTY the shim holds from birth with a wait for an EVENT the shim publishes: see
// the call site for why the difference decides whether the test measures setsid or the
// host's scheduler. The deadline is a runaway guard -- a shim that never binds its
// socket never ran -- and not the quantity under test.
func awaitPath(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
