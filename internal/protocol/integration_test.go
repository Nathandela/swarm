package protocol

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
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
	buildOnce    sync.Once
	swarmBin     string
	fakeAgentBin string
	buildErr     error
)

// buildBinaries compiles the swarm binary (used as the shim) and the fake agent
// once per test run. Integration tests skip if the toolchain is unavailable.
func buildBinaries(t *testing.T) {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "swp-bin")
		if err != nil {
			buildErr = err
			return
		}
		swarmBin = filepath.Join(dir, "swarm")
		fakeAgentBin = filepath.Join(dir, "swarm-fake-agent")
		for _, b := range []struct{ out, pkg string }{
			{swarmBin, "github.com/Nathandela/swarm/cmd/swarm"},
			{fakeAgentBin, "github.com/Nathandela/swarm/cmd/swarm-fake-agent"},
		} {
			cmd := exec.Command("go", "build", "-o", b.out, b.pkg)
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				buildErr = err
				return
			}
		}
	})
	if buildErr != nil {
		t.Skipf("cannot build integration binaries: %v", buildErr)
	}
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

// TestIntegration_RenameFansOutRosterEvent proves the v0.5 rename converges every
// client through the NORMAL event path against a REAL daemon: a rename over the
// protocol persists the new label AND the roster poller (FromDaemon.watch, now
// name-aware) fans it out as a Subscribe event so a second client sees it live —
// not only on the next List. This is the end-to-end "all clients converge" guard a
// stub cannot give (the stub pushes events by hand; here a real poller must detect
// the name change).
func TestIntegration_RenameFansOutRosterEvent(t *testing.T) {
	d := realDaemon(t)
	launchRealSession(t, d)

	sock := tmpSock(t)
	srv, err := Serve(FromDaemon(d), sock)
	if err != nil {
		t.Fatalf("Serve(FromDaemon): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// One client subscribes (the observer that must converge); another issues the
	// rename. A single client would also do, but two proves the fan-out crosses
	// connections.
	observer := dialClient(t, sock, nil)
	events, err := observer.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	renamer := dialClient(t, sock, nil)

	if err := renamer.Rename(onlyViewID(t, renamer), "renamed-live"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// The observer and renamer are distinct connections, so each stamps ids with its
	// OWN endpoint namespace (the Serve path uses per-connection endpoint ids; the
	// assembled daemon uses one stable id). Converge on the LOCAL id, which is stable
	// across endpoints, plus the new label.
	_, wantLocal, ok := ParseID(onlyViewID(t, renamer))
	if !ok {
		t.Fatalf("could not parse the session's local id")
	}

	// Drain the subscribe stream (it also carries the session's status changes) until
	// a roster event carries the NEW label, or fail at the deadline.
	deadline := time.After(launchTimeout)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("subscribe stream closed before the rename event arrived")
			}
			if _, evLocal, ok := ParseID(ev.Session.ID); ok && evLocal == wantLocal && ev.Session.Name == "renamed-live" {
				return // converged: the roster event carried the new name to another client
			}
		case <-deadline:
			t.Fatalf("no roster event with the new name within %s (rename did not fan out)", launchTimeout)
		}
	}
}
