package skeleton

// Item 1.3 (agents-tracker-445), R1.3.7 / T1.3.f — the grid tap must SKIP a
// session that has a live controller lease. Under concurrent shim serving a tap
// attach would otherwise steal the controller's stream every poll. White-box: a
// real core with one running fake session, driven through tapOnce with the
// controller check and the per-session sample both faked so the decision is
// observed directly.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
)

func TestTapGrids_SkipsControlledSession(t *testing.T) {
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "swsktap")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	core, err := daemon.Open(daemon.Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "d.sock"),
		LockPath:    filepath.Join(dir, "d.lock"),
		LogPath:     filepath.Join(dir, "d.log"),
		ShimBinary:  swarmBin,
		MaxSessions: 8,
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

	var mu sync.Mutex
	sampled := map[string]int{}
	controlled := true

	d := &Daemon{
		core:     core,
		stateDir: dir,
		sampling: make(map[string]struct{}),
		sampleFn: func(id string) {
			mu.Lock()
			sampled[id]++
			mu.Unlock()
		},
		controlled: func(id string) bool {
			mu.Lock()
			defer mu.Unlock()
			return controlled && id == m.ID
		},
	}

	// Controlled: the tap must NOT sample it (no attach reaches the shim).
	d.tapOnce(context.Background())
	d.sampleWG.Wait()
	mu.Lock()
	n := sampled[m.ID]
	mu.Unlock()
	if n != 0 {
		t.Fatalf("tap sampled a controlled session %d times; want 0 (R1.3.7 stream-steal)", n)
	}

	// Not controlled (detached): the heuristic resumes; the tap samples it.
	mu.Lock()
	controlled = false
	mu.Unlock()
	d.tapOnce(context.Background())
	d.sampleWG.Wait()
	mu.Lock()
	n = sampled[m.ID]
	mu.Unlock()
	if n != 1 {
		t.Fatalf("tap sampled an uncontrolled running session %d times; want 1 (heuristic must resume after detach)", n)
	}
}

func mustScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return p
}
