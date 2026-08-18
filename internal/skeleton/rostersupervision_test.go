package skeleton

// ADR-010 Amendment 3, slice 1: SessionView.SupervisionPending is live state (a
// pending supervision event, nothing the core persists), so unless it is in the
// roster poller's diff key a flip alone never fires an event and the board marker
// would appear only when some unrelated change happened to follow. Mirrors
// rostercontrol_test.go, which pins the same rule for the remote-control flag.
//
// FROZEN API: func (a *coreAPI) SetSupervisionPendingFunc(fn func(local string) bool)
// (nil clears), sampled into rosterSnap beside controlled.
//
// RED today: coreAPI has no SetSupervisionPendingFunc, so this file does not compile.

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

func TestRosterPoll_SupervisionPendingFlipAloneFiresAnEvent(t *testing.T) {
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "swsksp")
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

	var pending atomic.Bool
	api := newCoreAPI(core, fakeAgentBin, endpointID(dir))
	t.Cleanup(api.close)
	api.SetSupervisionPendingFunc(func(local string) bool { return pending.Load() && local == m.ID })

	// Drain the session's own churn until a quiet window, so the pending flip is the
	// only change left to observe.
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

	pending.Store(true)
	var got persist.Meta
	select {
	case got = <-api.events:
	case <-time.After(2 * time.Second):
		t.Fatal("a pending supervision event fired NO roster event within 2s: the poller's diff key ignores it (ADR-008's roster bound fails for the marker)")
	}
	if got.ID != m.ID {
		t.Fatalf("roster event after the pending flip named %q; want %q", got.ID, m.ID)
	}

	drain()
	pending.Store(false)
	select {
	case got = <-api.events:
		if got.ID != m.ID {
			t.Fatalf("roster event after the pending clear named %q; want %q", got.ID, m.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("clearing the pending event fired NO roster event within 2s: the marker would never clear")
	}
}
