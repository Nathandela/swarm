package daemon

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// errInjectedCrash is returned by a launchProbe to model a daemon crash at a
// two-phase-launch boundary: the launch aborts with NO cleanup, exactly as a
// kill -9 would leave things.
var errInjectedCrash = errors.New("injected daemon crash")

// TestLaunch_TwoPhaseHappy asserts E5.4: a normal launch persists a running meta
// with the shim's identity filled in, the shim+agent are alive, and the session
// is on disk and in the registry.
func TestLaunch_TwoPhaseHappy(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	m, agentPID := launchAnnounce(t, d)

	if m.Status.Process != status.ProcessRunning {
		t.Fatalf("launched meta process = %q; want running", m.Status.Process)
	}
	if m.ShimPID <= 0 {
		t.Fatalf("launched meta ShimPID = %d; want a real PID", m.ShimPID)
	}
	if m.ShimStartTime == 0 {
		t.Fatalf("launched meta ShimStartTime = 0; want a recorded start time")
	}
	if !processAlive(m.ShimPID) {
		t.Fatalf("shim %d not alive after Launch", m.ShimPID)
	}
	if !processAlive(agentPID) {
		t.Fatalf("agent %d not alive after Launch", agentPID)
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, m.ID, "meta.json")); err != nil {
		t.Fatalf("meta.json not persisted for %s: %v", m.ID, err)
	}
	if _, ok := d.Get(m.ID); !ok {
		t.Fatalf("session %s absent from registry", m.ID)
	}
}

// TestLaunch_CrashBeforeSpawn_NoPhantom asserts E5.4/S11: a crash after the
// reservation meta is persisted but before the shim is spawned leaves no phantom
// running session and no orphan shim. Reconciliation on the next Open resolves
// the reserved session (it is not left `running` with no shim).
func TestLaunch_CrashBeforeSpawn_NoPhantom(t *testing.T) {
	cfg := daemonConfig(t)
	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := announceSpec(t, pidFile)

	var reservedID string
	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseReserved {
			reservedID = m.ID
			return errInjectedCrash
		}
		return nil
	}
	if _, err := d1.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want injected crash", err)
	}
	if reservedID == "" {
		t.Fatalf("probe never observed phaseReserved")
	}
	// No shim/agent may have been spawned.
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatalf("agent was spawned despite crash before spawn")
	}

	d1.abandon()
	d2 := openDaemon(t, cfg)

	// The reserved session must not survive as a phantom running session.
	waitNotProcess(t, d2, reservedID, status.ProcessRunning, pollTimeout)
	assertNoOrphansNoPhantoms(t, d2, cfg.StateDir)
}

// TestLaunch_CrashBeforeConfirm_NoOrphan asserts E5.4/S11: a crash after the
// shim is spawned but before the launch is confirmed/finalized leaves a live
// shim. Reconciliation must resolve it — adopt it (running) or reap it — but
// never leave it orphaned (running-and-unowned) or the session phantom.
func TestLaunch_CrashBeforeConfirm_NoOrphan(t *testing.T) {
	cfg := daemonConfig(t)
	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := announceSpec(t, pidFile)

	var spawnedID string
	var shimPID int
	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseSpawned {
			spawnedID = m.ID
			shimPID = m.ShimPID
			return errInjectedCrash
		}
		return nil
	}
	if _, err := d1.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want injected crash", err)
	}
	if spawnedID == "" {
		t.Fatalf("probe never observed phaseSpawned")
	}
	// Crash semantics: the probe modelled a daemon that DIED after spawning the
	// shim, so the abort must NOT have cleaned up — the shim is still alive and
	// serving. (A launch that kills its shim on probe-error would never exercise
	// the S11 spawn window.)
	if shimPID <= 0 || !processAlive(shimPID) {
		t.Fatalf("shim PID %d not alive after crash-before-confirm; the crash must leave the shim running", shimPID)
	}
	if !shimServing(cfg.StateDir, spawnedID) {
		t.Fatalf("shim for %s not serving after crash-before-confirm; the crash must leave it running", spawnedID)
	}
	// The shim execs the agent; capture the agent PID so cleanup reaps it
	// regardless of how reconciliation resolves the shim.
	agentPID := readPIDFile(t, pidFile)
	t.Cleanup(func() { killTree(agentPID); killTree(shimPID) })

	d1.abandon()
	d2 := openDaemon(t, cfg)

	// Give reconciliation a moment to settle whichever resolution it chose.
	waitSettled(t, d2, spawnedID, cfg.StateDir, pollTimeout)
	assertNoOrphansNoPhantoms(t, d2, cfg.StateDir)
	assertSessionBijection(t, d2, cfg.StateDir, spawnedID)
}

// TestLaunch_FiltersClientEnv asserts the Epic 1 carry-forward: Launch passes
// persist.FilterEnv(ClientEnv) to the shim, so a non-allowlisted variable in
// ClientEnv never reaches the agent while an allowlisted one does. The agent
// writes exactly the env it received to a file — the ground truth.
func TestLaunch_FiltersClientEnv(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const secret = "SWARMLEAKCANARY=leak-3f9a2c"       // not on the allowlist
	const allowed = "CONDA_PREFIX=/canary/conda-7b2ff" // on the allowlist (persist/env.go)
	envFile := filepath.Join(t.TempDir(), "agent-env.txt")

	spec := LaunchSpec{
		AgentType: "fake",
		Argv:      []string{selfExe(t), markerEnvDump, envFile},
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=" + os.Getenv("PATH"), secret, allowed},
		Cols:      80,
		Rows:      24,
	}
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = d.Kill(m.ID) })

	waitFile(t, envFile, pollTimeout)
	agentEnv, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read agent env: %v", err)
	}
	env := string(agentEnv)
	if !strings.Contains(env, allowed) {
		t.Fatalf("allowlisted var did not reach the agent; env=\n%s", env)
	}
	if strings.Contains(env, "SWARMLEAKCANARY") {
		t.Fatalf("non-allowlisted var LEAKED to the agent; env=\n%s", env)
	}
}

// ---------------------------------------------------------------------------
// S11 assertion helpers (shim<->meta bijection after reconciliation)
// ---------------------------------------------------------------------------

// shimServing reports whether a live listener answers at the session's
// deterministic shim socket. A dial succeeds only if a shim process is bound
// there; a stale socket file with no listener yields a connection error.
func shimServing(stateDir, id string) bool {
	conn, err := net.Dial("unix", shimSocketPath(stateDir, id))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// assertNoOrphansNoPhantoms enforces S11 across the whole registry: every
// running session has a live, serving shim (no phantom), and no non-running
// session has a shim still serving its socket (no orphan).
func assertNoOrphansNoPhantoms(t *testing.T, d *Daemon, stateDir string) {
	t.Helper()
	for _, m := range d.List() {
		assertSessionBijection(t, d, stateDir, m.ID)
	}
}

// assertSessionBijection enforces the S11 pair for a single session id.
func assertSessionBijection(t *testing.T, d *Daemon, stateDir, id string) {
	t.Helper()
	m, ok := d.Get(id)
	if !ok {
		if shimServing(stateDir, id) {
			t.Fatalf("orphan shim: session %s absent from registry but its socket is still serving", id)
		}
		return
	}
	if m.Status.Process == status.ProcessRunning {
		if !shimServing(stateDir, id) {
			t.Fatalf("phantom session: %s is running but no shim is serving its socket", id)
		}
		if !processAlive(m.ShimPID) {
			t.Fatalf("phantom session: %s is running but shim PID %d is dead", id, m.ShimPID)
		}
		return
	}
	if shimServing(stateDir, id) {
		t.Fatalf("orphan shim: session %s is %q but its shim is still serving", id, m.Status.Process)
	}
}

// waitNotProcess waits until the session's process dimension is anything other
// than avoid (or the session is gone), failing on timeout.
func waitNotProcess(t *testing.T, d *Daemon, id string, avoid status.Process, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m, ok := d.Get(id)
		if !ok || m.Status.Process != avoid {
			return
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("session %s never left process=%q within %s", id, avoid, timeout)
}

// waitSettled waits until a session's shim<->meta relationship is internally
// consistent (running⇒serving, non-running⇒not serving), giving async
// reconciliation time to complete before the strict assertion.
func waitSettled(t *testing.T, d *Daemon, id, stateDir string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m, ok := d.Get(id)
		serving := shimServing(stateDir, id)
		if !ok {
			if !serving {
				return
			}
		} else if m.Status.Process == status.ProcessRunning {
			if serving && processAlive(m.ShimPID) {
				return
			}
		} else if !serving {
			return
		}
		time.Sleep(pollStep)
	}
}
