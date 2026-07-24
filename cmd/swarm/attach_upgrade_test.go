package main

// v0.4 committee fix wave — item 1 (HIGH, the blocker): attach broke after an auto
// daemon-upgrade. The TUI's attach path multiplexed on the long-lived protocol
// client's connection; daemonRestartedMsg swaps that client for a fresh one, leaving
// the attach closure dialing the DEAD conn. The fix dials a FRESH connection per
// attach from the stored socket path (attachDialer), independent of any long-lived
// client. This test reuses the real-daemon restart harness (daemon_upgrade_test.go):
// it launches a session, performs the auto-upgrade restart, and then attaches
// successfully to the surviving session through the per-attach dialer.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/attach"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestAttachDialer_AttachesAfterAutoUpgrade(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real daemon + session")
	}
	swarmBin, fakeAgentBin := buildRoleBinaries(t)
	env := startSmokeDaemon(t, swarmBin, fakeAgentBin)
	cc := ccFromSmokeEnv(env, swarmBin)

	// A long-lived client on the ORIGINAL daemon launches an idle session (mirrors the
	// TUI's long-lived client). After the restart this conn is dead — exactly the state
	// that broke the multiplexed attach.
	longLived, err := protocol.Dial(cc.SocketPath, []string{"attach", "subscribe"})
	if err != nil {
		t.Fatalf("dial original daemon: %v", err)
	}
	script := filepath.Join(t.TempDir(), "idle.txt")
	if err := os.WriteFile(script, []byte("print ready\nidle 120s\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if _, _, err := longLived.Launch(protocol.LaunchReq{
		Agent:   "fake",
		Cwd:     t.TempDir(),
		Options: map[string]string{"script": script},
		Env:     []string{"PATH=" + os.Getenv("PATH")},
		Cols:    80,
		Rows:    24,
	}); err != nil {
		t.Fatalf("launch idle fake session: %v", err)
	}

	// Perform the auto-upgrade restart (the exact closure runTUI injects). The original
	// daemon is replaced; longLived's connection is now dead.
	reconnect, err := daemonRestarter(cc)()
	if err != nil {
		t.Fatalf("daemonRestarter: %v", err)
	}
	t.Cleanup(func() {
		if data, err := os.ReadFile(filepath.Join(cc.StateDir, "daemon.pid")); err == nil {
			if f := strings.Fields(string(data)); len(f) > 0 {
				if pid, err := strconv.Atoi(f[0]); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
	})
	if cl, ok := reconnect.(interface{ Close() error }); ok {
		defer cl.Close()
	}
	_ = longLived.Close()

	// The session survives the restart (shims own the PTYs). Find its current id on the
	// replacement daemon.
	var sessID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		views, lerr := reconnect.List()
		if lerr == nil && len(views) == 1 {
			sessID = views[0].ID
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if sessID == "" {
		t.Fatal("the launched session did not survive the restart on the replacement daemon")
	}

	// The per-attach dialer must attach successfully AFTER the upgrade, dialing a FRESH
	// conn to the replacement daemon rather than the long-lived (now dead) client's conn.
	dial := attachDialer(cc)
	var sess attach.Session
	var cleanup func()
	var aerr error
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s, cu, e := dial(sessID); e == nil {
			sess, cleanup, aerr = s, cu, nil
			break
		} else {
			aerr = e
		}
		time.Sleep(100 * time.Millisecond)
	}
	if aerr != nil || sess == nil {
		t.Fatalf("attach after auto-upgrade failed through the per-attach dialer: %v", aerr)
	}
	if cleanup == nil {
		t.Fatal("the per-attach dialer must return a cleanup that closes the fresh conn")
	}
	// The attach is live: a snapshot is retrievable (the reserved snapshot, possibly the
	// idle grid) without a panic, and the lease detaches cleanly via the cleanup.
	_ = sess.Snapshot()
	cleanup()
}
