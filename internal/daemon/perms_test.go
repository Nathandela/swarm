package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shim"
)

// wantOwnerOnly stats path and fails unless its permission bits are owner-only (no
// group/other bits) — the 0600/0700 tightness E5.7/D-6 requires.
func wantOwnerOnly(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("%s mode = %#o; want owner-only (no group/other bits)", path, perm)
	}
}

// TestPermissions_StateDirAndSocket asserts E5.7/D-6: even under a fully
// permissive umask, the daemon's own artifacts are created tight — the state
// directory is 0700 and the daemon socket is 0600. The umask is forced to 0 so a
// missing chmod/umask-bracket would be caught (the OS would otherwise widen the
// modes).
func TestPermissions_StateDirAndSocket(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	_ = d

	// State dir 0700: no group/other bits.
	di, err := os.Stat(cfg.StateDir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("state dir mode = %#o; want 0700 (no group/other bits)", perm)
	}

	// Daemon socket 0600: owner rw only.
	si, err := os.Stat(cfg.SocketPath)
	if err != nil {
		t.Fatalf("stat daemon socket: %v", err)
	}
	if perm := si.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("daemon socket mode = %#o; want 0600 (no group/other bits)", perm)
	}
}

// TestPermissions_LockUnderPrivateDir asserts E5.7/D-6: the flock lock file lives
// under the 0700 state directory and is not group/other accessible.
func TestPermissions_LockUnderPrivateDir(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	_ = d

	li, err := os.Stat(cfg.LockPath)
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if perm := li.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("lock file mode = %#o; want no group/other bits", perm)
	}
}

// TestPermissions_ShimLaunchConfig asserts E5.7/S6/ADR-004: the per-session
// shim-launch.json — which carries the crypto/rand hook token in its Env — is
// written 0600 even under a fully permissive umask. A regression widening it to
// 0644 would leak the token to any local reader, so its mode is pinned here.
func TestPermissions_ShimLaunchConfig(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m, _ := launchAnnounce(t, d)

	wantOwnerOnly(t, filepath.Join(cfg.StateDir, m.ID, shimLaunchConfigFile))
}

// TestPermissions_ShimSocketAndSideFiles asserts E5.7/D-6 for the SHIM's own
// artifacts. The shim is a separate process that inherits the daemon's umask, so
// even under a fully permissive umask the per-session shim socket, the final-
// snapshot side-file, and the exit side-file must all be owner-only (0600).
func TestPermissions_ShimSocketAndSideFiles(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	stateDir := shortStateDir(t)

	// Socket: a long-lived shim binds its per-session socket; assert it while the
	// shim is alive (the shim unlinks the socket on exit).
	const sockID = "permsock"
	spawnRealShim(t, stateDir, sockID)
	wantOwnerOnly(t, shimSocketPath(stateDir, sockID))

	// Side-files: a shim run to completion writes final-snapshot.bin then exit.json.
	const exitID = "permexit"
	sessionDir := filepath.Join(stateDir, exitID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	scriptPath := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(scriptPath, []byte("print hi\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	lc := shimLaunchConfig{
		SessionID:  exitID,
		Argv:       []string{fakeAgentBin, scriptPath},
		Cwd:        t.TempDir(),
		Env:        []string{"PATH=" + os.Getenv("PATH")},
		SocketPath: shimSocketPath(stateDir, exitID),
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

	// exit.json is written last (after the snapshot), so its presence implies both.
	waitFile(t, filepath.Join(sessionDir, shim.ExitFile), pollTimeout)
	wantOwnerOnly(t, filepath.Join(sessionDir, shim.SnapshotFile))
	wantOwnerOnly(t, filepath.Join(sessionDir, shim.ExitFile))
}
