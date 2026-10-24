package daemon

// The compatibility matrix checks adjacent old/new shim contracts against the
// current daemon. Skew is detected without signalling a foreign-version process;
// the matched cell reconnects and remains controllable.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/testbin"
)

// buildSwarmWithTag builds the swarm binary with an extra build tag and returns
// its path. Used to produce an adjacent-wire-version `swarm shim` for a compat
// cell; the default (untagged) swarmBin from TestMain is the matched v2 shim.
func buildSwarmWithTag(t *testing.T, tag string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "swarm-"+tag)
	if err := testbin.RegisterExecutable(bin); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-tags", tag, "-o", bin, "github.com/Nathandela/swarm/cmd/swarm")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build swarm -tags %s: %v", tag, err)
	}
	return bin
}

// spawnShimBin is spawnRealShim parameterized on the shim BINARY: it starts a real
// `<bin> shim --config` subprocess running a long-lived announce agent for session
// id under stateDir, at the deterministic socket path so daemon reconcile finds it.
// It returns the shim and agent PIDs and registers cleanup.
func spawnShimBin(t *testing.T, bin, stateDir, id string) (shimPID, agentPID int) {
	t.Helper()
	sessionDir := filepath.Join(stateDir, id)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), id+".pid")
	sock := shimSocketPath(stateDir, id)
	if len(sock) > 100 {
		t.Fatalf("shim socket path too long (%d): %s", len(sock), sock)
	}
	lc := shimLaunchConfig{
		SessionID:  id,
		Argv:       []string{selfExe(t), markerAnnounce, pidFile},
		Cwd:        t.TempDir(),
		Env:        []string{"PATH=" + os.Getenv("PATH")},
		SocketPath: sock,
		SessionDir: sessionDir,
		Cols:       80,
		Rows:       24,
		GraceMS:    2000,
	}
	cfgPath := filepath.Join(t.TempDir(), id+".json")
	writeJSON(t, cfgPath, lc)

	cmd := exec.Command(bin, "shim", "--config", cfgPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start shim %s: %v", bin, err)
	}
	shimPID = cmd.Process.Pid
	agentPID = readPIDFile(t, pidFile)
	waitFile(t, sock, pollTimeout) // the shim binds async; wait until it is servable
	t.Cleanup(func() {
		killTree(agentPID)
		killTree(shimPID)
		_, _ = cmd.Process.Wait()
	})
	return shimPID, agentPID
}

// TestCompatMatrix_OldShimNewDaemon runs the full {old, matched, new}-shim compat
// matrix against the current daemon: the matched cell reconnects and is drivable,
// and each skew cell is detected and marked lost with the agent left alive.
func TestCompatMatrix_OldShimNewDaemon(t *testing.T) {
	// Build the two adjacent-version shim binaries once; the matched cell reuses the
	// default v2 swarmBin from TestMain.
	oldShim := buildSwarmWithTag(t, "compat_shim_old")
	newShim := buildSwarmWithTag(t, "compat_shim_new")

	cells := []struct {
		name        string
		bin         string
		wantServing bool           // confirmShimServing: does the G2 hello gate accept it?
		wantProcess status.Process // reconcile outcome through a real daemon Open
	}{
		{"new-daemon(2)_x_old-shim(1)", oldShim, false, status.ProcessLost},
		{"matched(2)", swarmBin, true, status.ProcessRunning},
		{"old-daemon(2)_x_new-shim(3)", newShim, false, status.ProcessLost},
	}

	for _, c := range cells {
		c := c
		t.Run(c.name, func(t *testing.T) {
			cfg := daemonConfig(t) // fresh, isolated state dir per cell
			const id = "cmpt01"

			shimPID, agentPID := spawnShimBin(t, c.bin, cfg.StateDir, id)
			start, err := processStartTime(shimPID)
			if err != nil {
				t.Fatalf("processStartTime(shim %d): %v", shimPID, err)
			}
			// Persist a running meta whose (PID, start-time) MATCHES the live shim, so
			// the ONLY thing reconcile can reject on is the wire version — isolating the
			// version gate from the identity check.
			writeRunningMeta(t, cfg.StateDir, id, shimPID, start)

			// (1) The hello gate directly: confirmShimServing completes the G2 hello and
			// compares WireVersion, so it accepts ONLY a matched-version shim. A skew
			// shim answers with a mismatched version (or drops the connection) and reads
			// as not-serving — the exact signal reconcile uses to refuse adoption.
			sock := shimSocketPath(cfg.StateDir, id)
			if got := confirmShimServing(sock); got != c.wantServing {
				t.Fatalf("confirmShimServing = %v; want %v (wire-version hard gate)", got, c.wantServing)
			}

			// (2) End-to-end through a real daemon Open (reconcile runs synchronously
			// before serving, so Get is authoritative the moment Open returns).
			d := openDaemon(t, cfg)

			if c.wantProcess == status.ProcessRunning {
				got := waitStatus(t, d, id, status.ProcessRunning, pollTimeout)
				if got.Status.Process != status.ProcessRunning {
					t.Fatalf("matched cell: process = %q; want running", got.Status.Process)
				}
				if got.ShimPID != shimPID || got.ShimStartTime != start {
					t.Fatalf("matched cell: reconnected a DIFFERENT shim (pid %d/start %d, want %d/%d)",
						got.ShimPID, got.ShimStartTime, shimPID, start)
				}
				if !processAlive(agentPID) {
					t.Fatalf("matched cell: agent %d not alive after reconnect", agentPID)
				}
				// SERVE proof: the reconnected v2 shim is genuinely drivable — the daemon
				// kills it over the same matched wire and the agent dies.
				if err := d.Kill(id); err != nil {
					t.Fatalf("matched cell: Kill via reconnected shim: %v", err)
				}
				waitProcessGone(t, agentPID, pollTimeout)
				return
			}

			// Skew cell: the session must be LOST, not adopted over a mismatched
			// protocol (never a silent corrupt reconnect).
			m := getMeta(t, d, id)
			if m.Status.Process != status.ProcessLost {
				t.Fatalf("skew cell: process = %q; want lost (skew must be detected, not silently reconnected)", m.Status.Process)
			}
			// S3: marking lost sends ZERO signals — the live shim and its agent are
			// untouched (the daemon only reclassifies its own record). The shim closed
			// just the one skewed connection and keeps serving.
			if !processAlive(agentPID) {
				t.Fatalf("skew cell: agent %d died; marking lost must send no signal (S3)", agentPID)
			}
			if !processAlive(shimPID) {
				t.Fatalf("skew cell: shim %d died; a wire-version mismatch must drop only the connection, not the shim", shimPID)
			}
		})
	}
}
