package daemon

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

// TestVersionSkew_SmokeReconnectRealShim asserts E5.10: an (old-shim × new-daemon)
// pair from the current build interoperates — the daemon reconnects and lists a
// real shim it did not itself launch, over the G2 wire (the shim answers the
// daemon's hello with shimwire.Version). The full adjacent-build compat matrix is
// E14.3; this is the interop smoke.
func TestVersionSkew_SmokeReconnectRealShim(t *testing.T) {
	cfg := daemonConfig(t)
	id := "smoke01"

	shimPID, agentPID := spawnRealShim(t, cfg.StateDir, id)
	start, err := processStartTime(shimPID)
	if err != nil {
		t.Fatalf("processStartTime(shim %d): %v", shimPID, err)
	}
	writeRunningMeta(t, cfg.StateDir, id, shimPID, start)

	d := openDaemon(t, cfg)
	got := waitStatus(t, d, id, status.ProcessRunning, pollTimeout)
	if got.Status.Process != status.ProcessRunning {
		t.Fatalf("smoke reconnect process = %q; want running", got.Status.Process)
	}
	if !processAlive(agentPID) {
		t.Fatalf("agent %d not alive after smoke reconnect", agentPID)
	}
}

// TestVersionSkew_DialDetectsAndNamesFix asserts E5.11/D-8: an incompatible
// client version is detected at the handshake and the resulting error names the
// fix, `swarm daemon restart`. A compatible dial succeeds.
func TestVersionSkew_DialDetectsAndNamesFix(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	_ = d

	// Compatible: succeeds.
	conn, err := Dial(cfg.SocketPath, ProtocolVersion)
	if err != nil {
		t.Fatalf("Dial at ProtocolVersion: %v; want success", err)
	}
	_ = conn.Close()

	// Incompatible: ErrVersionSkew, message names the fix.
	_, err = Dial(cfg.SocketPath, ProtocolVersion+1)
	if !errors.Is(err, ErrVersionSkew) {
		t.Fatalf("Dial at incompatible version error = %v; want ErrVersionSkew", err)
	}
	if !strings.Contains(err.Error(), "swarm daemon restart") {
		t.Fatalf("skew error %q does not name `swarm daemon restart` (D-8 UX)", err.Error())
	}
}

// TestVersionSkew_RestartIsSafe asserts the D-5/D-8 safety half of E5.11: a
// daemon restart is safe — running sessions continue under their shims and are
// reconnected by the replacement daemon with no data loss. The restart is
// modeled as the crash-safe abandon+reopen the D-8 message promises.
func TestVersionSkew_RestartIsSafe(t *testing.T) {
	cfg := daemonConfig(t)
	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	m, agentPID := launchAnnounce(t, d1)

	d1.abandon() // the daemon is replaced

	d2 := openDaemon(t, cfg)
	got := waitStatus(t, d2, m.ID, status.ProcessRunning, pollTimeout)
	if got.Status.Process != status.ProcessRunning {
		t.Fatalf("session %s not reconnected after restart: process = %q", m.ID, got.Status.Process)
	}
	if !processAlive(agentPID) {
		t.Fatalf("agent %d died across a restart; restart must be safe (D-5)", agentPID)
	}
}
