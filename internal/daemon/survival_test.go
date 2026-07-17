package daemon

import (
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

// TestSurvival_KillDashNineReconnectsAll is THE headline S1/L2 test (E5.8): the
// daemon is the lifecycle authority that can die and come back without losing
// anyone. Start a daemon, launch N real sessions, model a kill -9 of the daemon
// (drop all its fds with no cleanup — the shims are detached and independent),
// confirm every agent PID is still alive, then start a fresh daemon and confirm
// it lists and reconnects all N sessions, none lost.
func TestSurvival_KillDashNineReconnectsAll(t *testing.T) {
	const n = 3
	cfg := daemonConfig(t)

	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}

	ids := make([]string, 0, n)
	agentPIDs := make([]int, 0, n)
	for i := 0; i < n; i++ {
		m, agentPID := launchAnnounce(t, d1)
		ids = append(ids, m.ID)
		agentPIDs = append(agentPIDs, agentPID)
	}
	if len(d1.List()) != n {
		t.Fatalf("registry size before kill = %d; want %d", len(d1.List()), n)
	}

	// kill -9 the daemon: no graceful shutdown, no shim signalling.
	d1.abandon()

	// S1: every agent must still be alive with the daemon gone.
	for _, pid := range agentPIDs {
		if !processAlive(pid) {
			t.Fatalf("agent %d died when the daemon was killed; violates S1 survival", pid)
		}
	}

	// A fresh daemon reconnects every session (L2), none lost.
	d2 := openDaemon(t, cfg)
	for _, id := range ids {
		got := waitStatus(t, d2, id, status.ProcessRunning, pollTimeout)
		if got.Status.Process == status.ProcessLost {
			t.Fatalf("session %s marked lost after restart; want reconnected", id)
		}
	}
	if len(d2.List()) != n {
		t.Fatalf("restarted registry size = %d; want %d", len(d2.List()), n)
	}
	// All agents remain alive under the reconnected daemon.
	for _, pid := range agentPIDs {
		if !processAlive(pid) {
			t.Fatalf("agent %d not alive after reconnect", pid)
		}
	}

	// The reconnection is real, not assumed: the new daemon can drive each shim.
	for _, id := range ids {
		if err := d2.Kill(id); err != nil {
			t.Fatalf("Kill %s via reconnected daemon: %v", id, err)
		}
	}
	for _, pid := range agentPIDs {
		waitProcessGone(t, pid, pollTimeout)
	}
}
