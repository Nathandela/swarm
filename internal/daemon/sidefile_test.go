package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// TestSidefile_MergeExitIntoMeta asserts E5.5/G6: when a shim has exited leaving
// its exit.json + final-snapshot.bin side-files, the daemon merges them into the
// meta on reconciliation — the exit code lands in meta and the process dimension
// becomes exited (not lost, which is reserved for a shim that vanished without an
// exit report).
func TestSidefile_MergeExitIntoMeta(t *testing.T) {
	cfg := daemonConfig(t)
	id := "merged01"
	sessionDir := filepath.Join(cfg.StateDir, id)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	// A dead PID: spawn something trivial and reap it.
	dead := exec.Command(selfExe(t), markerCatchTerm, filepath.Join(t.TempDir(), "x"))
	if err := dead.Start(); err != nil {
		t.Fatalf("start throwaway: %v", err)
	}
	deadPID := dead.Process.Pid
	deadStart, _ := processStartTime(deadPID)
	_ = dead.Process.Kill()
	_, _ = dead.Process.Wait()

	// Write the shim's side-files, then a running meta pointing at the dead shim.
	writeExitFile(t, sessionDir, 7, "")
	if err := os.WriteFile(filepath.Join(sessionDir, shim.SnapshotFile), makeFinalSnapshot(t, "final grid"), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	writeRunningMeta(t, cfg.StateDir, id, deadPID, deadStart)

	d := openDaemon(t, cfg)
	got := waitStatus(t, d, id, status.ProcessExited, pollTimeout)
	if got.Status.Process != status.ProcessExited {
		t.Fatalf("merged session process = %q; want exited", got.Status.Process)
	}
	if got.ExitCode == nil {
		t.Fatalf("merged session ExitCode = nil; want 7 from exit.json")
	}
	if *got.ExitCode != 7 {
		t.Fatalf("merged session ExitCode = %d; want 7", *got.ExitCode)
	}
}

// TestSidefile_ShimNeverWritesMeta asserts E5.5/G6 from the other side: a bare
// shim (no daemon) run to completion writes ONLY its own side-files
// (transcript, final snapshot, exit report) and never meta.json — the daemon is
// the sole meta writer.
func TestSidefile_ShimNeverWritesMeta(t *testing.T) {
	stateDir := shortStateDir(t)
	id := "bareshim1"
	sessionDir := filepath.Join(stateDir, id)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	scriptPath := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(scriptPath, []byte("print hello\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	sock := shimSocketPath(stateDir, id)
	lc := shimLaunchConfig{
		SessionID:  id,
		Argv:       []string{fakeAgentBin, scriptPath},
		Cwd:        t.TempDir(),
		Env:        []string{"PATH=" + os.Getenv("PATH")},
		SocketPath: sock,
		SessionDir: sessionDir,
		Cols:       80,
		Rows:       24,
		GraceMS:    2000,
	}
	cfgPath := filepath.Join(t.TempDir(), "shim.json")
	writeJSON(t, cfgPath, lc)

	cmd := exec.Command(swarmBin, "shim", "--config", cfgPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bare shim: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(pollTimeout):
		killTree(cmd.Process.Pid)
		t.Fatalf("bare shim did not exit within %s", pollTimeout)
	}

	// The shim wrote its side-files but never a meta.json (G6).
	waitFile(t, filepath.Join(sessionDir, shim.ExitFile), pollTimeout)
	if _, err := os.Stat(filepath.Join(sessionDir, "meta.json")); err == nil {
		t.Fatalf("shim wrote meta.json; only the daemon may write it (G6)")
	}
	for _, f := range []string{shim.ExitFile, shim.SnapshotFile, shim.TranscriptFile} {
		if _, err := os.Stat(filepath.Join(sessionDir, f)); err != nil {
			t.Fatalf("expected shim side-file %s missing: %v", f, err)
		}
	}
}

// writeExitFile writes an exit.json side-file matching shim.ExitInfo.
func writeExitFile(t *testing.T, sessionDir string, code int, sig string) {
	t.Helper()
	ei := shim.ExitInfo{ExitCode: code, ExitSignal: sig, FinishedAt: time.Now()}
	data, err := json.Marshal(ei)
	if err != nil {
		t.Fatalf("marshal exit info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, shim.ExitFile), data, 0o600); err != nil {
		t.Fatalf("write exit.json: %v", err)
	}
}
