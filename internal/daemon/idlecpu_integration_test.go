//go:build integration

package daemon

// E14.4 (T9, N-3, scenario 15 idle half): a REAL daemon process plus its N shims,
// left fully idle for >= 60s, must consume near-zero CPU — the executable proof
// that the design has NO busy-poll. It is build-tagged `integration` so it runs in
// CI's integration lane (the same `-tags integration` step the engine's real-CPU
// sampler runs behind, cpu_integration_test.go), never in the default `go test`.
//
// FIDELITY. The daemon is a genuine subprocess (the daemon-host re-exec used by the
// real kill-9 test) that opens a real daemon, launches N long-lived idle announce
// sessions as its own child shims, and then serves (select{}) — real Open, real
// accept loop, real per-shim liveness monitors, real detached shim processes. We
// sample the daemon's own PID and each shim's PID (never the test process), so the
// measurement is of the daemon + shims, isolated from the harness. NO billable
// agent runs: the agents are this test binary re-exec'd as blocked announce
// processes (main_test.go).
//
// MEASUREMENT. engine.SampleCPU integrates a process's CPU time over a short window
// and returns its utilization as a percentage of one core (the production sampler,
// E10.6). We sum daemon + all shims and sample repeatedly across the whole >= 60s
// idle window: a busy-poll anywhere keeps at least one process's utilization high on
// EVERY sample, while the mean is robust to a lone scheduling blip on a shared CI
// runner. The gate is fail-closed on the sustained (mean) summed idle CPU.
//
// THRESHOLD. The daemon's only periodic idle work is a per-shim liveness poll at
// monitorPoll (100ms) doing a couple of syscalls (pidAlive + processStartTime); the
// shims block on IO (PTY read, socket accept) and the announce agents block on
// stdin. Steady-state summed idle CPU is therefore a small fraction of one percent.
// The gate is 5% summed (matching the engine integration test's per-process idle<5%
// precedent, applied here to the whole system): comfortably above real idle noise,
// far below the ~100%-of-a-core a busy-poll would show.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// waitShimPIDs reads the persisted roster (the daemon-host holds the singleton, so
// we scan the state dir rather than dial an in-process daemon) until n running
// sessions each expose a shim PID, and returns those PIDs.
func waitShimPIDs(t *testing.T, stateDir string, n int, timeout time.Duration) []int {
	t.Helper()
	store, err := persist.NewStore(stateDir)
	if err != nil {
		t.Fatalf("NewStore(%s): %v", stateDir, err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		metas, serr := store.Scan()
		if serr == nil {
			pids := make([]int, 0, n)
			for _, m := range metas {
				if m.Status.Process == status.ProcessRunning && m.ShimPID > 0 {
					pids = append(pids, m.ShimPID)
				}
			}
			if len(pids) == n {
				return pids
			}
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("did not observe %d running shims in %s within %s", n, stateDir, timeout)
	return nil
}

func TestDaemonIdleCPUNearZero(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("engine.SampleCPU unsupported on %s", runtime.GOOS)
	}

	const (
		n            = 3
		idleWindow   = 60 * time.Second // >= 60s of genuine idle (scenario 15)
		settle       = 2 * time.Second  // let the launch-time CPU spike decay first
		sampleGap    = 4 * time.Second  // cadence of summed-CPU samples across the window
		cpuThreshold = 5.0              // percent of one core, SUMMED over daemon + all shims
	)

	dir := shortStateDir(t)
	sock := filepath.Join(dir, "daemon.sock")
	lock := filepath.Join(dir, "daemon.lock")
	logp := filepath.Join(dir, "daemon.log")

	// Spawn the real daemon process (opens a daemon, launches N idle shims, serves).
	host := exec.Command(selfExe(t), "-test.run", "^TestDaemonHostSubprocess$")
	host.Env = append(os.Environ(),
		envHostActive+"=1",
		envHostN+"="+strconv.Itoa(n),
		envDaemonState+"="+dir,
		envDaemonSock+"="+sock,
		envDaemonLock+"="+lock,
		envDaemonLog+"="+logp,
	)
	host.Stdout, host.Stderr = os.Stderr, os.Stderr
	if err := host.Start(); err != nil {
		t.Fatalf("start daemon host: %v", err)
	}
	daemonPID := host.Process.Pid
	hostDone := make(chan struct{})
	go func() { _, _ = host.Process.Wait(); close(hostDone) }()
	t.Cleanup(func() { killTree(daemonPID) })

	waitDial(t, sock, launchTimeout) // the daemon is serving: all N launched

	shimPIDs := waitShimPIDs(t, dir, n, launchTimeout)
	t.Cleanup(func() {
		for _, p := range shimPIDs {
			killTree(p)
		}
	})
	pids := append([]int{daemonPID}, shimPIDs...)
	t.Logf("measuring idle CPU of daemon pid=%d + %d shim pids=%v", daemonPID, len(shimPIDs), shimPIDs)

	time.Sleep(settle)

	start := time.Now()
	var (
		sumOfSums float64
		maxSum    float64
		samples   int
	)
	for time.Since(start) < idleWindow {
		total := 0.0
		for _, pid := range pids {
			c, err := engine.SampleCPU(pid)
			if err != nil {
				// Nothing should exit during a pure-idle window; a vanished process is
				// itself a failure (a lost session), not a measurement to skip.
				t.Fatalf("SampleCPU(pid=%d) at idle+%.0fs: %v (process died during idle?)",
					pid, time.Since(start).Seconds(), err)
			}
			total += c
		}
		if total > maxSum {
			maxSum = total
		}
		sumOfSums += total
		samples++
		t.Logf("idle+%4.0fs: summed daemon+shim CPU = %.3f%% of one core", time.Since(start).Seconds(), total)

		select {
		case <-hostDone:
			t.Fatalf("daemon host exited during the idle window — a session was lost")
		default:
		}
		time.Sleep(sampleGap)
	}
	elapsed := time.Since(start)

	// Guard the >= 60s requirement explicitly: the measurement window must really
	// have spanned a minute of idle, not a truncated loop.
	if elapsed < idleWindow {
		t.Fatalf("idle window was only %s; scenario 15 requires >= %s", elapsed, idleWindow)
	}
	if samples == 0 {
		t.Fatalf("no CPU samples taken across the idle window")
	}
	meanSum := sumOfSums / float64(samples)
	t.Logf("IDLE CPU over %s (%d samples, daemon + %d shims): mean summed = %.3f%%, max summed = %.3f%%, threshold = %.1f%%",
		elapsed.Round(time.Second), samples, len(shimPIDs), meanSum, maxSum, cpuThreshold)

	// Fail closed: sustained (mean) summed idle CPU must stay below the near-zero
	// threshold. A busy-poll would push a process toward ~100% of a core and blow
	// past this by more than an order of magnitude.
	if meanSum > cpuThreshold {
		t.Fatalf("mean summed idle CPU = %.3f%% over %s exceeds %.1f%% — a busy-poll is likely present (N-3 violation)",
			meanSum, elapsed.Round(time.Second), cpuThreshold)
	}
}
