package daemon

// Lane A of the auto-upgrade plan (docs/specifications/auto-upgrade-plan.md, L2):
// the three pieces an unattended `swarm daemon restart` needs from this package —
// the environment the daemon SAVED when it last started (daemon.env), a spawn that
// can be handed that saved environment instead of the caller's, and a lock probe
// that answers rule 0's "is there a daemon at all".
//
// FAILING-FIRST: SavedEnvPath, LoadSavedEnv, ClientConfig.Env and LockFree do not
// exist yet; Open does not write daemon.env yet.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
)

// TestOpenWritesSavedEnv fences the first half of L2: a started daemon persists
// exactly persist.FilterEnv of its own environment, 0600, and every later start
// rewrites it — so the saved set never goes stale behind an interactive restart.
func TestOpenWritesSavedEnv(t *testing.T) {
	cfg := daemonConfig(t)

	orig := daemonEnviron
	t.Cleanup(func() { daemonEnviron = orig })

	// AWS_SECRET_ACCESS_KEY is deliberately present: FilterEnv must drop it, so the
	// saved file is the S-2 allowlist and not a wider exposure class.
	first := []string{"PATH=/first/bin", "HOME=/home/first", "LC_SWARM_PROBE=one", "AWS_SECRET_ACCESS_KEY=must-not-be-saved"}
	daemonEnviron = func() []string { return first }

	d := openDaemon(t, cfg)

	path := SavedEnvPath(cfg.StateDir)
	if path != filepath.Join(cfg.StateDir, "daemon.env") {
		t.Fatalf("SavedEnvPath = %q, want <stateDir>/daemon.env", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat saved env: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("saved env mode = %v, want 0600", got)
	}
	got, err := LoadSavedEnv(cfg.StateDir)
	if err != nil {
		t.Fatalf("LoadSavedEnv: %v", err)
	}
	assertEnvEqual(t, "first start", got, persist.FilterEnv(first))

	// A second start must REWRITE the file from the environment of the day.
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	second := []string{"PATH=/second/bin", "LC_SWARM_PROBE=two"}
	daemonEnviron = func() []string { return second }

	openDaemon(t, cfg)

	got2, err := LoadSavedEnv(cfg.StateDir)
	if err != nil {
		t.Fatalf("LoadSavedEnv after restart: %v", err)
	}
	assertEnvEqual(t, "second start", got2, persist.FilterEnv(second))
}

// TestLoadSavedEnvMissing fences rule 3's predicate: "nothing saved" must be
// distinguishable from every other read failure, which is what lets the converge
// exit 3 and leave the running daemon alone.
func TestLoadSavedEnvMissing(t *testing.T) {
	dir := t.TempDir()
	env, err := LoadSavedEnv(dir)
	if err == nil {
		t.Fatalf("LoadSavedEnv on an empty state dir returned %v, want an error", env)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadSavedEnv error = %v, want one satisfying errors.Is(err, os.ErrNotExist)", err)
	}
}

// TestLoadSavedEnvEmptyFile fences the nil/non-nil distinction ClientConfig.Env is
// built on: a saved file that exists but holds nothing must read back as an EMPTY,
// NON-NIL slice. Read back as nil it would mean "no env supplied" at the spawn, and
// the unattended restart would silently inherit the timer's environment — the exact
// failure L2 exists to prevent, arrived at through the safest-looking file on disk.
func TestLoadSavedEnvEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(SavedEnvPath(dir), nil, 0o600); err != nil {
		t.Fatalf("write empty saved env: %v", err)
	}
	env, err := LoadSavedEnv(dir)
	if err != nil {
		t.Fatalf("LoadSavedEnv on an empty file: %v", err)
	}
	if env == nil {
		t.Fatal("LoadSavedEnv on an existing empty file returned a NIL slice; a nil Env means "+
			"\"inherit the caller's environment\" at the spawn, so it must be empty-but-non-nil")
	}
	if len(env) != 0 {
		t.Fatalf("LoadSavedEnv on an empty file = %q, want no entries", env)
	}
}

// TestSpawnDaemonEnvironment is THE environment test: the invariant L2 exists for.
// It spawns for real through defaultSpawnDaemon — no fake seam — and reads what the
// child actually received out of the daemon log.
//
// The child is a script that ignores its argument and execs /usr/bin/env. It is not
// /usr/bin/env directly: `env daemon` does NOT ignore "daemon", it tries to exec a
// program of that name and exits 127 without printing anything.
func TestSpawnDaemonEnvironment(t *testing.T) {
	t.Setenv("LC_SWARM_PROBE", "caller") // the environment of whoever invokes the restart

	dumper := envDumpProgram(t)

	t.Run("saved env replaces the caller's", func(t *testing.T) {
		dir := shortStateDir(t)
		cfg := ClientConfig{
			StateDir:   dir,
			SocketPath: filepath.Join(dir, "daemon.sock"),
			LockPath:   filepath.Join(dir, "daemon.lock"),
			LogPath:    filepath.Join(dir, "daemon.log"),
			DaemonBin:  dumper,
			Env:        []string{"PATH=" + os.Getenv("PATH"), "LC_SWARM_PROBE=saved"},
		}
		if err := defaultSpawnDaemon(cfg); err != nil {
			t.Fatalf("defaultSpawnDaemon: %v", err)
		}
		log := waitForChildEnv(t, cfg.LogPath)

		if !strings.Contains(log, "LC_SWARM_PROBE=saved") {
			t.Errorf("child env lacks LC_SWARM_PROBE=saved; got:\n%s", log)
		}
		if strings.Contains(log, "LC_SWARM_PROBE=caller") {
			t.Errorf("child env carries the CALLER's LC_SWARM_PROBE=caller; got:\n%s", log)
		}
		if !strings.Contains(log, EnvStateDir+"=") {
			t.Errorf("child env lacks %s=; got:\n%s", EnvStateDir, log)
		}
	})

	t.Run("nil env keeps today's behaviour", func(t *testing.T) {
		dir := shortStateDir(t)
		cfg := ClientConfig{
			StateDir:   dir,
			SocketPath: filepath.Join(dir, "daemon.sock"),
			LockPath:   filepath.Join(dir, "daemon.lock"),
			LogPath:    filepath.Join(dir, "daemon.log"),
			DaemonBin:  dumper,
		}
		if err := defaultSpawnDaemon(cfg); err != nil {
			t.Fatalf("defaultSpawnDaemon: %v", err)
		}
		log := waitForChildEnv(t, cfg.LogPath)

		if !strings.Contains(log, "LC_SWARM_PROBE=caller") {
			t.Errorf("with Env nil the child must inherit the caller's env; got:\n%s", log)
		}
	})
}

// TestLockFree fences rule 0: only a lock held by a live daemon means "there is a
// daemon"; every other outcome, including a state dir that does not exist, means
// there is nothing to converge.
func TestLockFree(t *testing.T) {
	t.Run("free lock", func(t *testing.T) {
		cfg := ClientConfig{LockPath: filepath.Join(t.TempDir(), "daemon.lock")}
		if !LockFree(cfg) {
			t.Fatal("LockFree = false on an unheld lock, want true")
		}
		// The probe must not keep what it took: a held-open lock would lock the daemon
		// this very converge is about to spawn out of its own singleton. flock is
		// per-open-file-description, so a leak inside this process still contends here.
		f, err := acquireLock(cfg.LockPath)
		if err != nil {
			t.Fatalf("lock not released by LockFree: acquireLock = %v", err)
		}
		_ = releaseLock(f)
	})

	t.Run("held lock", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "daemon.lock")
		f, err := acquireLock(path)
		if err != nil {
			t.Fatalf("acquireLock: %v", err)
		}
		defer func() { _ = releaseLock(f) }() // hold it for the whole assertion

		if LockFree(ClientConfig{LockPath: path}) {
			t.Fatal("LockFree = true while the lock is held, want false")
		}
	})

	t.Run("missing state dir", func(t *testing.T) {
		cfg := ClientConfig{LockPath: filepath.Join(t.TempDir(), "no-such-dir", "daemon.lock")}
		if !LockFree(cfg) {
			t.Fatal("LockFree = false when the lock file cannot be opened at all, want true")
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// envDumpProgram writes an executable that ignores every argument and prints its
// environment, and returns its path. defaultSpawnDaemon always passes "daemon".
func envDumpProgram(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dumpenv")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec /usr/bin/env\n"), 0o700); err != nil {
		t.Fatalf("write env dumper: %v", err)
	}
	return path
}

// waitForChildEnv polls the daemon log until the spawned child's environment dump
// is complete — EnvLog is the last variable defaultSpawnDaemon appends, so its
// presence proves the whole environment was written — and returns the log.
func waitForChildEnv(t *testing.T, logPath string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err == nil {
			last = string(data)
			if strings.Contains(last, EnvLog+"=") {
				return last
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child never dumped a complete environment to %s; log so far:\n%s", logPath, last)
	return ""
}

// assertEnvEqual compares two KEY=VALUE slices element-wise, in order.
func assertEnvEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: saved env = %q, want %q", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: saved env = %q, want %q", what, got, want)
		}
	}
}
