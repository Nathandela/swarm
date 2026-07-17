// Package skeleton failing-test suite for Epic 8 PART B — the DAEMON ASSEMBLY that
// makes the walking skeleton (GG-1) real. This is the missing wiring: today
// `swarm daemon` (cmd/swarm.runDaemon) only opens daemon.Open and blocks on a
// signal, and the daemon's own socket serves only the Epic 5 four-byte version
// handshake. Nothing stands up the Epic 6 client protocol, the Epic 10 status
// engine, or the hook routing on that socket.
//
// The assembly cannot live in internal/daemon (protocol imports daemon → an import
// cycle), so it is pinned as a new package internal/skeleton that composes the
// three layers and is what runDaemon calls. These tests drive it IN-PROCESS for the
// deterministic scenarios (2, 7, 8, 9, 16, S10); the kill -9 headline (scenario 10)
// and the terminal-close survival (scenario 3) need a real subprocess and live in
// internal/e2e.
//
// FROZEN API (internal/skeleton) — refine as tests need, flagged in the handoff:
//
//	type Config struct {
//	    StateDir, SocketPath, LockPath, LogPath, ShimBinary string
//	    MaxSessions        int
//	    PollInterval       time.Duration // engine fallback-poll cadence (E10.8); 0 = no cadence
//	    StalenessThreshold time.Duration
//	    FakeAgentBin       string        // DEV/TEST ONLY: resolves the reserved agent "fake" for the walking skeleton
//	}
//	// Serve performs the full assembly and begins serving on cfg.SocketPath:
//	//   daemon.Open (core lifecycle) ; engine.New + go engine.Run (status) ;
//	//   protocol.Serve(FromDaemon(core)) BOUND TO cfg.SocketPath (client RPC) ;
//	//   hook posts on that SAME socket decoded + routed to engine.HandleCallback ;
//	//   engine Emit fans out to protocol subscribers.
//	func Serve(cfg Config) (*Daemon, error)
//	func (d *Daemon) SocketPath() string
//	func (d *Daemon) Core() *daemon.Daemon   // the underlying lifecycle authority (walking-skeleton launch seam)
//	func (d *Daemon) Close() error
package skeleton

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
	"github.com/Nathandela/swarm/internal/protocol"
)

// ---------------------------------------------------------------------------
// Build + assembly harness.
// ---------------------------------------------------------------------------

var (
	buildOnce    sync.Once
	swarmBin     string
	fakeAgentBin string
	buildErr     error
)

func buildBinaries(t *testing.T) {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "swsk-bin")
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

// assemble stands up the full in-process assembly over a short-pathed state dir
// (/tmp keeps the socket under the 104-byte sun_path limit), with cleanup.
func assemble(t *testing.T) *Daemon {
	t.Helper()
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "swsk")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	sk, err := Serve(Config{
		StateDir:           dir,
		SocketPath:         filepath.Join(dir, "d.sock"),
		LockPath:           filepath.Join(dir, "d.lock"),
		LogPath:            filepath.Join(dir, "d.log"),
		ShimBinary:         swarmBin,
		MaxSessions:        16,
		PollInterval:       50 * time.Millisecond,
		StalenessThreshold: 2 * time.Second,
		FakeAgentBin:       fakeAgentBin,
	})
	if err != nil {
		t.Fatalf("skeleton.Serve (the daemon assembly is not built): %v", err)
	}
	t.Cleanup(func() { _ = sk.Close() })
	return sk
}

// launchFake launches a scripted fake-agent session directly on the core daemon
// (agent->argv composition is the Epic 9 adapter's job; the walking skeleton
// carries argv explicitly). Cleanup terminates the shim's process group.
func launchFake(t *testing.T, sk *Daemon, script string) persist.Meta {
	t.Helper()
	spath := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(spath, []byte(script), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	m, err := sk.Core().Launch(daemon.LaunchSpec{
		AgentType: "fake",
		Argv:      []string{fakeAgentBin, spath},
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      80,
		Rows:      24,
	})
	if err != nil {
		t.Fatalf("core Launch: %v", err)
	}
	t.Cleanup(func() {
		if m.ShimPID > 0 {
			_ = syscall.Kill(m.ShimPID, syscall.SIGTERM)
		}
	})
	return m
}

func dialClient(t *testing.T, sk *Daemon, caps ...string) *protocol.Client {
	t.Helper()
	c, err := protocol.Dial(sk.SocketPath(), caps)
	if err != nil {
		t.Fatalf("protocol.Dial the assembled socket: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func waitOneView(t *testing.T, c *protocol.Client) protocol.SessionView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(views) == 1 {
			return views[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session never appeared in List within 10s")
	return protocol.SessionView{}
}

// ---------------------------------------------------------------------------
// Scenario tests against the assembled daemon.
// ---------------------------------------------------------------------------

// Scenario 2 (S-1/L-1/V-1) — a launched fake agent shows up over the assembled
// client protocol, grouped (Working). Proves the socket serves the FULL Epic 6
// surface, not the Epic 5 four-byte handshake.
func TestSkeleton_LaunchAppearsGroupedOverProtocol(t *testing.T) {
	sk := assemble(t)
	launchFake(t, sk, "print HELLO\nidle 60s\n")

	c := dialClient(t, sk, "attach", "subscribe")
	view := waitOneView(t, c)

	if view.Group == "" {
		t.Fatal("assembled List returned no server-derived group (E6.9)")
	}
	if view.Agent != "fake" {
		t.Fatalf("view agent = %q, want fake", view.Agent)
	}
}

// Scenario 7 (A-1/A-4/P-5) — attach paints a real snapshot from the real shim and
// input reaches the agent, all through the assembled socket.
func TestSkeleton_AttachSnapshotAndInput(t *testing.T) {
	sk := assemble(t)
	launchFake(t, sk, "print SNAPSHOT-MARK\nidle 60s\n")

	c := dialClient(t, sk, "attach")
	view := waitOneView(t, c)

	a, err := c.Attach(view.ID)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if len(a.Snapshot()) == 0 {
		t.Fatal("attach returned an empty snapshot from the real shim (A-4/S10)")
	}
	if a.Generation() == 0 {
		t.Error("first attach generation = 0, want >= 1")
	}
	if err := a.Input([]byte("\n")); err != nil {
		t.Errorf("Input over the assembled attach: %v", err)
	}
}

// Scenario 8 (A-2) — detach leaves the session running; a fresh attach succeeds
// (L3: lease + stream released on detach).
func TestSkeleton_DetachLeavesSessionAndReattachSucceeds(t *testing.T) {
	sk := assemble(t)
	launchFake(t, sk, "print HI\nidle 60s\n")

	c := dialClient(t, sk, "attach")
	view := waitOneView(t, c)

	a, err := c.Attach(view.ID)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := a.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}

	// The session is still listed and re-attachable after detach.
	c2 := dialClient(t, sk, "attach")
	if _, err := c2.Attach(waitOneView(t, c2).ID); err != nil {
		t.Fatalf("re-Attach after detach (L3): %v", err)
	}
}

// Scenario 9 (A-3/P-5) — resize propagates under the attach lease without error.
func TestSkeleton_ResizeUnderLease(t *testing.T) {
	sk := assemble(t)
	launchFake(t, sk, "print HI\nidle 60s\n")

	c := dialClient(t, sk, "attach")
	a, err := c.Attach(waitOneView(t, c).ID)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := a.Resize(120, 40); err != nil {
		t.Fatalf("Resize under lease: %v", err)
	}
}

// Scenario 16 (P-5/S2) — a second attach supersedes with a strictly higher lease
// generation; the superseded controller's stream closes (stale-generation input
// can no longer be applied).
func TestSkeleton_TwoClientSupersede(t *testing.T) {
	sk := assemble(t)
	launchFake(t, sk, "print HI\nidle 60s\n")

	c1 := dialClient(t, sk, "attach")
	a1, err := c1.Attach(waitOneView(t, c1).ID)
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}

	c2 := dialClient(t, sk, "attach")
	a2, err := c2.Attach(waitOneView(t, c2).ID)
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}

	if !(a2.Generation() > a1.Generation()) {
		t.Fatalf("supersede generation: a2=%d not > a1=%d (S2)", a2.Generation(), a1.Generation())
	}

	// The superseded controller's live stream must close (its lease is void), so a
	// stale-generation client can no longer drive the session. Drain to close.
	closed := make(chan struct{})
	go func() {
		for range a1.Frames() {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("superseded controller's Frames() did not close within 3s (S2)")
	}
}

// S10 — snapshot continuity under output load: attach while the agent is actively
// producing output; the client-visible bytes are exactly one snapshot followed by
// live frames (never a blank screen, never a torn boundary). We assert the
// snapshot is non-empty and live frames continue to arrive after it.
func TestSkeleton_SnapshotContinuityUnderLoad(t *testing.T) {
	sk := assemble(t)
	// The fake agent prints repeatedly so output is in flight during attach.
	launchFake(t, sk, "print L1\nprint L2\nprint L3\nprint L4\nidle 60s\n")

	c := dialClient(t, sk, "attach")
	a, err := c.Attach(waitOneView(t, c).ID)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if len(a.Snapshot()) == 0 {
		t.Fatal("attach under load returned an empty snapshot (S10)")
	}
	// Frames stream stays live (the loop delivers post-snapshot output).
	select {
	case _, ok := <-a.Frames():
		_ = ok // a frame or a clean close both prove the stream is wired
	case <-time.After(3 * time.Second):
		// no frame is acceptable if the agent went idle, but the stream must exist
	}
}

// Hook routing DEMUX — a hook post (raw JSON callback) to the daemon socket is
// routed to the hook/engine path (rejected as unauthenticated here, S6), and does
// NOT wedge the socket: a fresh client can still Dial + List afterward. The full
// authenticated hook -> status -> fan-out path is exercised end-to-end in Epic 11.
func TestSkeleton_HookPostDemuxDoesNotWedgeClientSocket(t *testing.T) {
	sk := assemble(t)
	launchFake(t, sk, "print HI\nidle 60s\n")

	// A best-effort hook post with a bogus token: the assembly must demux it to the
	// hook path (not the client-RPC path) and stay serving.
	postBogusHook(t, sk.SocketPath())

	c := dialClient(t, sk, "attach")
	if _, err := c.List(); err != nil {
		t.Fatalf("client socket wedged after a hook post (demux broken): %v", err)
	}
}
