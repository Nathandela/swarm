package protocol

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/testbin"
)

// Integration: a FEW tests against a REAL daemon.Daemon (real shim + real fake
// agent) exercising the attach/lease path through protocol.FromDaemon — the parts
// where a stub cannot prove the wire actually moves a real snapshot + lease.
//
// The session is created via the real daemon directly (LaunchSpec carries the
// argv), because agent->argv composition is the adapter's job (Epic 9); the
// protocol Launch path is covered by the stub tests. Everything the client sees —
// List, the derived Group, Attach, the real snapshot, the lease generation, and
// detach/re-attach (L3) — goes through the protocol Server over FromDaemon(d).

var (
	testBins     testbin.Binaries
	swarmBin     string
	fakeAgentBin string
)

// buildBinaries compiles the swarm binary (used as the shim) and the fake agent
// once per test run. Integration tests skip if the toolchain is unavailable.
func buildBinaries(t *testing.T) {
	t.Helper()
	testBins.Build(t, "swp-bin", func(t *testing.T, err error) {
		t.Skipf("cannot build integration binaries: %v", err)
	})
	swarmBin, fakeAgentBin = testBins.Swarm, testBins.FakeAgent
}

// realDaemon opens a real daemon over a short-pathed state dir, with cleanup.
func realDaemon(t *testing.T) *daemon.Daemon {
	t.Helper()
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "swi")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := daemon.Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		LockPath:    filepath.Join(dir, "daemon.lock"),
		MaxSessions: 16,
		ShimBinary:  swarmBin,
		LogPath:     filepath.Join(dir, "daemon.log"),
	}
	d, err := daemon.Open(cfg)
	if err != nil {
		t.Fatalf("daemon.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// launchRealSession launches a long-lived fake agent (prints a line, then idles)
// directly on the daemon and returns its meta. Cleanup terminates the shim.
func launchRealSession(t *testing.T, d *daemon.Daemon) persist.Meta {
	t.Helper()
	script := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(script, []byte("print HELLO-FROM-AGENT\nidle 60s\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	m, err := d.Launch(daemon.LaunchSpec{
		AgentType: "fake",
		Argv:      []string{fakeAgentBin, script},
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      80,
		Rows:      24,
	})
	if err != nil {
		t.Fatalf("daemon.Launch: %v", err)
	}
	t.Cleanup(func() {
		if m.ShimPID > 0 {
			_ = syscall.Kill(m.ShimPID, syscall.SIGTERM) // handler TERM->KILLs the agent group
		}
	})
	return m
}

// TestIntegration_AttachRealSnapshotAndLease is the real-daemon attach path: List
// through the protocol, attach, receive a real snapshot, and exercise the lease
// (supersede + monotonic generation) and L3 (detach releases; re-attach succeeds).
func TestIntegration_AttachRealSnapshotAndLease(t *testing.T) {
	d := realDaemon(t)
	launchRealSession(t, d)

	sock := tmpSock(t)
	srv, err := Serve(FromDaemon(d), sock)
	if err != nil {
		t.Fatalf("Serve(FromDaemon): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	c := dialClient(t, sock, []string{"attach"})

	// The session is visible with a server-computed group.
	var view SessionView
	deadline := time.Now().Add(launchTimeout)
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(views) == 1 {
			view = views[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if view.ID == "" {
		t.Fatalf("session never appeared in List within %s", launchTimeout)
	}
	if view.Group == "" {
		t.Errorf("list view has no derived group")
	}

	// Attach: a real snapshot must arrive from the real shim.
	a, err := c.Attach(view.ID)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if len(a.Snapshot()) == 0 {
		t.Fatalf("attach returned an empty snapshot from the real shim (violates A-4/S10)")
	}
	if a.Generation() == 0 {
		t.Errorf("first real attach generation = 0, want >= 1")
	}
	// Input to the controller must not error over the real pipe.
	if err := a.Input([]byte("\n")); err != nil {
		t.Errorf("Input over real attach: %v", err)
	}

	// A second attach supersedes with a higher generation (real lease).
	c2 := dialClient(t, sock, []string{"attach"})
	b, err := c2.Attach(onlyViewID(t, c2))
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	if !(b.Generation() > a.Generation()) {
		t.Errorf("supersede generation: B=%d not > A=%d", b.Generation(), a.Generation())
	}

	// L3: detach the current controller, then a fresh attach must succeed.
	if err := b.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	c3 := dialClient(t, sock, []string{"attach"})
	if _, err := c3.Attach(onlyViewID(t, c3)); err != nil {
		t.Fatalf("re-Attach after detach (L3): %v", err)
	}
}
