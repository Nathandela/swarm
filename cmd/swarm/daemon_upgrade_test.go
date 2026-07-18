package main

// bd agents-tracker-5jl — the client-side reuse of `swarm daemon restart`. runTUI
// injects a daemonRestarter into the TUI so a client that finds an OLDER daemon can
// auto-restart it instead of asking the user. This exercises that closure against a
// REAL daemon: it must perform the same D-8 stop+spawn restart and hand back a
// freshly-connected client to the replacement. Sessions survive by design (shims own
// the PTYs; proven at the daemon layer in internal/daemon realkill/survival), so this
// covers the cmd/swarm wiring end to end.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
)

// ccFromSmokeEnv rebuilds the daemon.ClientConfig the smoke daemon runs under, so the
// in-process restarter targets the same socket/lock/state as that daemon.
func ccFromSmokeEnv(env []string, swarmBin string) daemon.ClientConfig {
	m := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return daemon.ClientConfig{
		StateDir:   m[daemon.EnvStateDir],
		SocketPath: m[daemon.EnvSocket],
		LockPath:   m[daemon.EnvLock],
		LogPath:    m[daemon.EnvLog],
		DaemonBin:  swarmBin,
	}
}

// readDaemonPID reads the "PID STARTTIME" daemon pidfile and returns the pid.
func readDaemonPID(t *testing.T, stateDir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "daemon.pid"))
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		t.Fatalf("empty pidfile at %s", stateDir)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("parse pid %q: %v", fields[0], err)
	}
	return pid
}

func TestDaemonRestarter_RestartsRealDaemonAndReconnects(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real daemon")
	}
	swarmBin, fakeAgentBin := buildRoleBinaries(t)
	env := startSmokeDaemon(t, swarmBin, fakeAgentBin)
	cc := ccFromSmokeEnv(env, swarmBin)

	oldPID := readDaemonPID(t, cc.StateDir)

	// The restarter that runTUI injects into the TUI: a real stop+spawn restart plus a
	// reconnect to the replacement.
	client, err := daemonRestarter(cc)()
	if err != nil {
		t.Fatalf("daemonRestarter: %v", err)
	}
	// The detached replacement outlives the original smoke daemon's cleanup, so kill it
	// by its (replacement) pidfile. Registered after startSmokeDaemon's cleanup, so it
	// runs first (LIFO).
	t.Cleanup(func() {
		data, err := os.ReadFile(filepath.Join(cc.StateDir, "daemon.pid"))
		if err != nil {
			return
		}
		if f := strings.Fields(string(data)); len(f) > 0 {
			if pid, err := strconv.Atoi(f[0]); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	if cl, ok := client.(interface{ Close() error }); ok {
		defer cl.Close()
	}

	// The reconnected client speaks the full protocol against the replacement.
	if _, err := client.List(); err != nil {
		t.Fatalf("List on the reconnected client: %v", err)
	}

	// The daemon was actually replaced (the pidfile names a fresh, live daemon).
	newPID := readDaemonPID(t, cc.StateDir)
	if newPID == oldPID {
		t.Fatalf("restart must replace the daemon; pidfile still names the original pid %d", oldPID)
	}
	if err := syscall.Kill(newPID, 0); err != nil {
		t.Fatalf("the replacement daemon (pid %d) is not alive: %v", newPID, err)
	}
}
