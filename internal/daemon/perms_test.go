package daemon

import (
	"os"
	"syscall"
	"testing"
)

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
