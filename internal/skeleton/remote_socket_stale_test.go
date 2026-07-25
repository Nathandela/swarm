package skeleton

// FAILING-FIRST for slice S4b -- ADR-007 B15, first consequence: a stale remote socket
// must not take down the whole daemon.
//
// Serve aborts assembly when the remote listener fails to bind (`return nil, rerr`). While
// the remote socket existed only for operators who opted in, that abort was defensible.
// Since B15 every PROVISIONED machine opens one, so the debris of a single SIGKILL -- a
// socket inode whose owner never got to unlink it -- would stop the daemon starting AT
// ALL. That is a strictly worse failure than the one B15 fixes: "swarm is completely
// broken" rather than "remote is broken", and on the path every remote user is now on.

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// leaveStaleSocket reproduces what a killed daemon leaves on disk: a bound socket whose
// listener is gone but whose inode is not. SetUnlinkOnClose(false) is what makes it real
// -- a plain Close unlinks the path, which would leave nothing to trip over and prove
// nothing. Creating an ordinary file instead would not either: bind fails on any existing
// path, but only a socket inode is the case a daemon must recover from unattended.
func leaveStaleSocket(t *testing.T, path string) {
	t.Helper()
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("bind %s: %v", path, err)
	}
	ln.SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener on %s: %v", path, err)
	}
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("no stale socket was left at %s (lstat err=%v); the test cannot exercise what it claims", path, err)
	}
}

// TestServe_StaleRemoteSocketDoesNotStopTheDaemon requires BOTH halves. Surviving is not
// enough: a Serve that quietly skipped the remote listener would restore the daemon and
// restore PB-LIFE-7's silence at the same time, so the socket must end up served.
func TestServe_StaleRemoteSocketDoesNotStopTheDaemon(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swss") // short path keeps sun_path under the 104-byte limit
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "remote.sock")
	leaveStaleSocket(t, sock)

	// ShimBinary is a placeholder: no session is launched here, and daemon.Open only
	// touches it when reconnecting an existing one (there is none in a fresh state dir).
	sk, err := Serve(Config{
		StateDir:         dir,
		SocketPath:       filepath.Join(dir, "d.sock"),
		LockPath:         filepath.Join(dir, "d.lock"),
		LogPath:          filepath.Join(dir, "d.log"),
		ShimBinary:       "/bin/true",
		MaxSessions:      4,
		RemoteSocketPath: sock,
	})
	if sk != nil {
		t.Cleanup(func() { _ = sk.Close() })
	}
	if err != nil {
		t.Fatalf("Serve over a stale %s = %v; the daemon does not start at all because of "+
			"debris from a crash. Every session, every local client and the TUI are down, not "+
			"just remote control.", sock, err)
	}

	c, derr := net.DialTimeout("unix", sock, 2*time.Second)
	if derr != nil {
		t.Fatalf("the daemon started but nothing is listening on %s (%v): the stale socket was "+
			"survived by dropping the remote tier, which is the PB-LIFE-7 silence again", sock, derr)
	}
	_ = c.Close()
}

// TestServe_DoesNotDeleteANonSocketAtTheRemotePath is the fence around the unlink above.
//
// The reclaim is safe because daemon.Open holds the singleton flock before it runs -- but
// that lock is on <stateDir>/daemon.lock, while RemoteSocketPath may be configured to any
// path at all (SWARM_DAEMON_REMOTE_SOCK). An UNCONDITIONAL remove therefore reaches past
// the lock: it would delete a regular file an operator pointed at by mistake, and -- worse
// -- two daemons with different state dirs sharing one override path would stop contending,
// the second silently stealing the first's live socket instead of failing to bind.
//
// Narrowing the unlink to paths that ARE sockets keeps the crash-debris case (which is a
// socket) and returns everything else to failing loudly, which is what a bind did before.
func TestServe_DoesNotDeleteANonSocketAtTheRemotePath(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swnf")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "remote.sock")
	const content = "not a socket"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("seed regular file: %v", err)
	}

	sk, err := Serve(Config{
		StateDir:         dir,
		SocketPath:       filepath.Join(dir, "d.sock"),
		LockPath:         filepath.Join(dir, "d.lock"),
		LogPath:          filepath.Join(dir, "d.log"),
		ShimBinary:       "/bin/true",
		MaxSessions:      4,
		RemoteSocketPath: path,
	})
	if sk != nil {
		t.Cleanup(func() { _ = sk.Close() })
	}
	if err == nil {
		t.Fatalf("Serve bound the remote tier over a REGULAR FILE at %s; the reclaim is not "+
			"confined to sockets, so it destroys whatever happens to sit at the configured path", path)
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("the regular file at %s is gone after a failed Serve (%v); it was deleted, not "+
			"reported", path, rerr)
	}
	if string(got) != content {
		t.Fatalf("the file at %s reads %q, want %q; it was replaced", path, got, content)
	}
}
