package daemon

// Daemon restart/upgrade SOAK (Epic 14 scenarios 10/11, E14.3 T5): N real sessions
// must endure 50 daemon kill -9 + restart cycles with ZERO session loss.
//
// Each cycle re-Opens a daemon over the same state dir — a real restart that
// rebuilds the registry purely from the on-disk meta scan and reconnects the live
// shims by (PID, process-start-time) identity (D-4/S3/L2) — asserts every session
// reconnected as the SAME shim with a byte-intact transcript, then abandon()s it.
// abandon() is the sanctioned kill -9 model (daemon.go): it drops the lock + socket
// fds with NO cleanup and NO shim signalling, exactly as the OS does when the daemon
// is SIGKILLed, so the shims are neither told to stop nor waited on. The shims are
// REAL detached `swarm shim` subprocesses, so their survival across all 50 cycles is
// real process survival; the loop fails on the FIRST lost session, naming the cycle.
//
// Real-PROCESS daemon death with shim reparenting-to-init is separately proven once
// by TestSurvival_RealKillNineReconnectsAll (a real `swarm daemon` subprocess
// SIGKILLed for real); this soak's distinct job is the 50-cycle endurance and
// transcript integrity, so each cycle is kept spawn-free (in-process Open/abandon)
// per the "keep per-cycle work tight" guidance rather than churning 50 subprocesses.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// soakSession is one session's captured identity + transcript baseline: the truth
// every restart must reproduce, or a session was lost.
type soakSession struct {
	id         string
	shimPID    int
	shimStart  int64
	marker     string
	transcript []byte
}

// launchFakePrint launches a real detached fake-agent session that prints marker to
// its PTY then idles long enough to outlast the whole soak, returning its confirmed
// running meta. The shim is a real subprocess that survives the daemon (S1), so it
// is reconnectable across every cycle; the printed marker gives the transcript real
// content whose integrity the soak asserts.
func launchFakePrint(t *testing.T, d *Daemon, marker string) persist.Meta {
	t.Helper()
	scriptDir := t.TempDir()
	script := filepath.Join(scriptDir, "script.txt")
	if err := os.WriteFile(script, []byte("print "+marker+"\nidle 600s\n"), 0o644); err != nil {
		t.Fatalf("write fake-agent script: %v", err)
	}
	m, err := d.Launch(LaunchSpec{
		AgentType: "fake",
		Argv:      []string{fakeAgentBin, script},
		Cwd:       scriptDir,
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      80,
		Rows:      24,
	})
	if err != nil {
		t.Fatalf("Launch fake %q: %v", marker, err)
	}
	t.Cleanup(func() { killTree(m.ShimPID) })
	return m
}

// waitTranscript polls a session's transcript.log (the shim's raw, append-only PTY
// capture) until it contains want, returning its full bytes as the stable baseline
// the soak later asserts stays byte-identical across every restart.
func waitTranscript(t *testing.T, sessionDir, want string, timeout time.Duration) []byte {
	t.Helper()
	path := filepath.Join(sessionDir, "transcript.log")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), want) {
			return b
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("transcript %s never contained %q within %s", path, want, timeout)
	return nil
}

func TestSoak_RestartUpgrade_50CyclesZeroLoss(t *testing.T) {
	const n = 3
	cycles := 50
	if testing.Short() {
		cycles = 10 // still RUNS under -short (never skipped); fewer cycles only
	}
	cfg := daemonConfig(t)

	// Launch N real sessions through the first daemon incarnation, capture each
	// session's identity + transcript baseline, then abandon it (cycle-0 kill -9):
	// the detached shims survive and are the population every later cycle reconnects.
	d0, err := Open(cfg)
	if err != nil {
		t.Fatalf("initial Open: %v", err)
	}
	sessions := make([]soakSession, 0, n)
	for i := 0; i < n; i++ {
		marker := fmt.Sprintf("SOAK-LIVES-%d", i)
		m := launchFakePrint(t, d0, marker)
		tr := waitTranscript(t, d0.sessionDir(m.ID), marker, pollTimeout)
		sessions = append(sessions, soakSession{
			id: m.ID, shimPID: m.ShimPID, shimStart: m.ShimStartTime,
			marker: marker, transcript: tr,
		})
	}
	if got := len(d0.List()); got != n {
		t.Fatalf("pre-soak registry size = %d; want %d", got, n)
	}
	d0.abandon()

	for cycle := 1; cycle <= cycles; cycle++ {
		// S1: every shim survived the previous kill -9 with its identity intact.
		for _, s := range sessions {
			if !processAlive(s.shimPID) {
				t.Fatalf("cycle %d: shim %d (session %s) died — session lost (violates S1 zero-loss)",
					cycle, s.shimPID, s.id)
			}
			if st, err := processStartTime(s.shimPID); err != nil || st != s.shimStart {
				t.Fatalf("cycle %d: shim %d (session %s) identity changed (start=%d want %d, err=%v) — session lost",
					cycle, s.shimPID, s.id, st, s.shimStart, err)
			}
		}

		// Restart: a fresh daemon rebuilds from disk and reconnects the live shims.
		d, err := Open(cfg)
		if err != nil {
			t.Fatalf("cycle %d: restart Open: %v", cycle, err)
		}
		if got := len(d.List()); got != n {
			d.abandon()
			t.Fatalf("cycle %d: restarted registry size = %d; want %d (a session was lost)", cycle, got, n)
		}
		for _, s := range sessions {
			m := waitStatus(t, d, s.id, status.ProcessRunning, pollTimeout)
			if m.ShimPID != s.shimPID || m.ShimStartTime != s.shimStart {
				d.abandon()
				t.Fatalf("cycle %d: session %s reconnected as a DIFFERENT shim (pid %d/start %d, want %d/%d) — relaunched, not reconnected",
					cycle, s.id, m.ShimPID, m.ShimStartTime, s.shimPID, s.shimStart)
			}
			// Transcript intact: byte-identical to the pre-soak baseline, so the kill
			// -9 + restart churn never lost or truncated the agent's captured output.
			tr, err := os.ReadFile(filepath.Join(d.sessionDir(s.id), "transcript.log"))
			if err != nil {
				d.abandon()
				t.Fatalf("cycle %d: session %s transcript unreadable: %v", cycle, s.id, err)
			}
			if !bytes.Equal(tr, s.transcript) {
				d.abandon()
				t.Fatalf("cycle %d: session %s transcript changed across restart (%dB want %dB) — lost/truncated",
					cycle, s.id, len(tr), len(s.transcript))
			}
		}

		if cycle < cycles {
			d.abandon() // kill -9 for the next cycle
		} else {
			_ = d.Close() // final incarnation: clean shutdown releases the singleton
		}
	}
}
