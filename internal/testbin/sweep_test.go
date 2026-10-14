package testbin

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestKillProcessesOf_KillsADetachedSurvivorOfATestBinary pins the post-run sweep. Shims are
// built to outlive the daemon that spawned them (ADR-001), so a test that launches a session and
// returns without killing it leaves the shim and its agent running forever: 306 such processes,
// 1.7-10.6 days old, were found on the owner's machine. Everything started from a binary this
// run built is identified by /proc/<pid>/exe and must be killed when the run ends.
func TestKillProcessesOf_KillsADetachedSurvivorOfATestBinary(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "swarm")
	raw, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skip("no /bin/sleep")
	}
	if err := os.WriteFile(exe, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "600")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detached, as a shim is
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	KillProcessesOf(exe)

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("a process running a test-built binary survived the sweep")
	}
}

// TestKillProcessesOf_MatchesABinaryBuiltUnderASymlinkedDir pins the sweep when TMPDIR is a
// symlink: /proc/<pid>/exe names the resolved path, so an unresolved build path matched nothing
// and the sweep was a silent no-op.
func TestKillProcessesOf_MatchesABinaryBuiltUnderASymlinkedDir(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "tmp")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skip("no /bin/sleep")
	}
	exe := filepath.Join(link, "swarm")
	if err := os.WriteFile(exe, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "600")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	KillProcessesOf(exe)

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("a binary built under a symlinked dir survived the sweep")
	}
}
