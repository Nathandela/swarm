package skeleton

// agents-tracker-nx44.7 -- the easy-to-miss half of the roster badge. The roster
// poller is the SOLE snapshot producer, and it only queues a meta when its
// per-session diff key CHANGES. A remote take_control changes nothing the core
// persists (no status, no name), so unless the control state is IN the diff key the
// flip alone never fires an event: the badge would appear only when some unrelated
// status change happened to follow it, and ADR-008's 1s roster bound would silently
// fail for this field.
//
// White-box on purpose: this asserts the poller's own behaviour against a REAL core
// with one running fake session, with the control source faked so the flip is the
// only thing that moves.
//
// RED today: coreAPI has no controlled-lease source to register, so this file does
// not compile.

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestRosterPoll_ControlFlipAloneFiresAnEvent(t *testing.T) {
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "swskrc")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	core, err := daemon.Open(daemon.Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "d.sock"),
		LockPath:    filepath.Join(dir, "d.lock"),
		LogPath:     filepath.Join(dir, "d.log"),
		ShimBinary:  swarmBin,
		MaxSessions: 4,
	})
	if err != nil {
		t.Fatalf("daemon.Open: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	m, err := core.Launch(daemon.LaunchSpec{
		AgentType: "fake",
		Argv:      []string{fakeAgentBin, mustScript(t, "print HI\nidle 60s\n")},
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

	var controlled atomic.Bool
	api := newCoreAPI(core, fakeAgentBin, endpointID(dir))
	t.Cleanup(api.close)
	api.SetRemoteControlledFunc(func(local string) bool { return controlled.Load() && local == m.ID })

	// Drain everything the poller queues while the roster settles, so the only
	// change left to observe is the control flip. A quiet window (no event for two
	// poll intervals) means the poller has caught up with the session's own churn.
	drain := func() {
		quiet := time.NewTimer(3 * eventPoll)
		defer quiet.Stop()
		for {
			select {
			case <-api.events:
				if !quiet.Stop() {
					<-quiet.C
				}
				quiet.Reset(3 * eventPoll)
			case <-quiet.C:
				return
			}
		}
	}
	drain()

	controlled.Store(true)
	// ADR-008 bounds a roster change at 1s; the poller ticks well inside that.
	var got persist.Meta
	select {
	case got = <-api.events:
	case <-time.After(2 * time.Second):
		t.Fatal("a remote take_control fired NO roster event within 2s: the poller's diff key ignores the control state (ADR-008's roster bound fails for the badge)")
	}
	if got.ID != m.ID {
		t.Fatalf("roster event after the control flip named %q; want the controlled session %q", got.ID, m.ID)
	}

	drain()
	controlled.Store(false)
	select {
	case got = <-api.events:
		if got.ID != m.ID {
			t.Fatalf("roster event after the control release named %q; want %q", got.ID, m.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("releasing remote control fired NO roster event within 2s: the badge would never clear")
	}
}
