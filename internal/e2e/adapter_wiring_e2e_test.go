// Epic 11 FIX 4 end-to-end: the adapters are wired into launch. Launching
// agent="claude" resolves the claude adapter through the registry, composes its
// REAL argv (claude + the inline --settings hook injection) via adapter.Command,
// resolves the bare "claude" binary against the agent PATH, registers the session
// with claude's SignalSources, and a real `swarm hook` invocation drives status
// through the full path (hook stdin -> engine normalize via SignalSources -> persist
// + fan out). COST: the resolved "claude" binary is a FAKE stub script that only
// posts a hook and idles — no billable real-CLI run (the real-CLI smoke is Epic 14).
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// fakeClaudeBinDir writes a stub executable named "claude" into a fresh dir and
// returns the dir. The stub ignores the claude argv (it is not the real CLI); it
// posts a Stop hook through the swarm binary — exactly as a real Claude Stop hook
// would — then idles so the session stays running and observable. The swarm binary
// is referenced by its absolute build path, so only "claude" needs to be on PATH.
func fakeClaudeBinDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swfakecli")
	if err != nil {
		t.Fatalf("bin dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	script := "#!/bin/sh\n" +
		"# Fake claude (Epic 11 FIX 4 wiring test): drive status via the real swarm hook\n" +
		"# path, then idle so the session stays running.\n" +
		"\"" + swarmBin + "\" hook Stop </dev/null\n" +
		"sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return dir
}

// TestE2E_AdapterWiring_ClaudeLaunchAndHook launches agent="claude" through the
// assembled daemon and asserts (1) the composed argv is claude's real argv (the
// resolved binary + the --settings hook injection), and (2) a real Stop hook drives
// the session to turn=idle via the claude SignalSources mapping — the whole FIX 3 +
// FIX 4 chain, with no billable CLI run.
func TestE2E_AdapterWiring_ClaudeLaunchAndHook(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)
	startDaemon(t, env)
	c := dial(t, env.sock)

	binDir := fakeClaudeBinDir(t)
	cwd := t.TempDir()
	id, _, err := c.Launch(protocol.LaunchReq{
		Agent:   "claude",
		Cwd:     cwd,
		Options: map[string]string{},
		Env:     []string{"PATH=" + binDir + ":" + os.Getenv("PATH")},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatalf("client Launch(claude): %v", err)
	}
	// Clean up the surviving shim/agent while the daemon is still alive (LIFO before
	// the daemon-kill cleanup).
	t.Cleanup(func() { _ = c.Delete(id) })

	waitOneView(t, c)
	local := localOf(t, id)

	// (1) The composed argv is claude's real argv: argv[0] resolved to the fake
	// "claude" on the agent PATH, and the inline --settings hook injection present.
	argv := readShimArgv(t, env.stateDir, local)
	wantBin := filepath.Join(binDir, "claude")
	if len(argv) == 0 || argv[0] != wantBin {
		t.Fatalf("shim argv[0] = %v; want the resolved claude binary %q", argv, wantBin)
	}
	if !argvHas(argv, "--settings") {
		t.Fatalf("shim argv omits claude's --settings hook injection (adapter.Command not used): %v", argv)
	}

	// (2) The real Stop hook drives the session to turn=idle via claude's SignalSources
	// (Stop -> idle). Without the mapping bridge + sources registration, the hook is
	// rejected or unmapped and the turn never settles.
	st, ok := waitForStatus(t, c, id, l1Bound, func(s status.Status) bool {
		return s.Turn == status.TurnIdle
	})
	if !ok {
		t.Fatalf("claude session never settled to turn=idle from its real Stop hook (last=%+v); the "+
			"adapter->launch wiring or the mapping bridge is not driving status", st)
	}
}

// readShimArgv reads a launched session's composed agent argv out of the
// daemon-written shim-launch.json.
func readShimArgv(t *testing.T, stateDir, local string) []string {
	t.Helper()
	path := filepath.Join(stateDir, local, "shim-launch.json")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			var cfg struct {
				Argv []string `json:"argv"`
			}
			if json.Unmarshal(data, &cfg) == nil && len(cfg.Argv) > 0 {
				return cfg.Argv
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("shim argv never appeared in %s", path)
	return nil
}

// argvHas reports whether argv contains an element exactly equal to s.
func argvHas(argv []string, s string) bool {
	for _, a := range argv {
		if a == s {
			return true
		}
	}
	return false
}
