package daemon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
)

// TestAutoStart_ColdStateDirSpawnsAndConnects asserts E5.9/D-1 (scenario 1): a
// client call finding no live daemon spawns one detached and connects
// transparently. Against a cold state dir with no pre-started daemon,
// EnsureDaemon returns a working connection, and a daemon is now genuinely
// running (a subsequent Open loses the singleton lock to it).
//
// The detached spawn is exercised for real via an injected spawner that launches
// this test binary in daemon mode (setsid, stdio→log) — the production spawner
// (nil) does the same with `swarm daemon`, wired at the cmd layer.
func TestAutoStart_ColdStateDirSpawnsAndConnects(t *testing.T) {
	dir := shortStateDir(t)
	cc := ClientConfig{
		SocketPath: filepath.Join(dir, "daemon.sock"),
		LockPath:   filepath.Join(dir, "daemon.lock"),
		StateDir:   dir,
		DaemonBin:  selfExe(t),
		LogPath:    filepath.Join(dir, "daemon.log"),
	}

	var spawnCount atomic.Int32
	var spawned *exec.Cmd
	cc.spawnDaemon = func(c ClientConfig) error {
		spawnCount.Add(1)
		cmd := exec.Command(c.DaemonBin, markerDaemonRun)
		cmd.Env = append(os.Environ(),
			envDaemonState+"="+c.StateDir,
			envDaemonSock+"="+c.SocketPath,
			envDaemonLock+"="+c.LockPath,
			envDaemonLog+"="+c.LogPath,
		)
		logf, err := os.Create(c.LogPath)
		if err != nil {
			return err
		}
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detached (D-1)
		if err := cmd.Start(); err != nil {
			return err
		}
		spawned = cmd
		return nil
	}
	t.Cleanup(func() {
		if spawned != nil {
			killTree(spawned.Process.Pid)
			_, _ = spawned.Process.Wait()
		}
	})

	conn, err := EnsureDaemon(cc)
	if err != nil {
		t.Fatalf("EnsureDaemon (cold): %v", err)
	}
	if conn == nil {
		t.Fatalf("EnsureDaemon returned a nil connection")
	}
	_ = conn.Close()
	if spawnCount.Load() != 1 {
		t.Fatalf("spawnDaemon called %d times; want exactly 1 on cold start", spawnCount.Load())
	}

	// A daemon is genuinely running now: opening one over the same lock loses.
	if _, err := Open(Config{
		StateDir:   cc.StateDir,
		SocketPath: cc.SocketPath,
		LockPath:   cc.LockPath,
	}); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Open after auto-start error = %v; want ErrAlreadyRunning (a daemon is up)", err)
	}

	// D-1 says stdio goes to the log file; the spawner created it.
	if _, err := os.Stat(cc.LogPath); err != nil {
		t.Fatalf("daemon log file not created: %v", err)
	}
}

// TestAutoStart_IdempotentWhenAlreadyRunning asserts E5.9/D-7: once a daemon is
// up, EnsureDaemon connects to the existing one and does NOT spawn a second. The
// singleton guarantee (S12, tested in singleton_test.go) plus this idempotency
// is the D-1 "transparently" property under concurrency.
func TestAutoStart_IdempotentWhenAlreadyRunning(t *testing.T) {
	dir := shortStateDir(t)
	cc := ClientConfig{
		SocketPath: filepath.Join(dir, "daemon.sock"),
		LockPath:   filepath.Join(dir, "daemon.lock"),
		StateDir:   dir,
		DaemonBin:  selfExe(t),
		LogPath:    filepath.Join(dir, "daemon.log"),
	}

	var spawnCount atomic.Int32
	var spawned *exec.Cmd
	cc.spawnDaemon = func(c ClientConfig) error {
		spawnCount.Add(1)
		cmd := exec.Command(c.DaemonBin, markerDaemonRun)
		cmd.Env = append(os.Environ(),
			envDaemonState+"="+c.StateDir,
			envDaemonSock+"="+c.SocketPath,
			envDaemonLock+"="+c.LockPath,
			envDaemonLog+"="+c.LogPath,
		)
		logf, err := os.Create(c.LogPath)
		if err != nil {
			return err
		}
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		spawned = cmd
		return nil
	}
	t.Cleanup(func() {
		if spawned != nil {
			killTree(spawned.Process.Pid)
			_, _ = spawned.Process.Wait()
		}
	})

	c1, err := EnsureDaemon(cc)
	if err != nil {
		t.Fatalf("EnsureDaemon (first): %v", err)
	}
	_ = c1.Close()

	c2, err := EnsureDaemon(cc)
	if err != nil {
		t.Fatalf("EnsureDaemon (second): %v", err)
	}
	_ = c2.Close()

	if spawnCount.Load() != 1 {
		t.Fatalf("spawnDaemon called %d times; want exactly 1 (idempotent)", spawnCount.Load())
	}
}
