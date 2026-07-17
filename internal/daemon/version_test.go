package daemon

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
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

// TestVersionSkew_DetectsLegacyUntaggedDaemon asserts the D-8 promise across the
// F2 wire change: the 'V' version-probe tag is an INCOMPATIBLE change, so a current
// client dialing an already-running PRE-change daemon must detect it as skew (and
// name the restart fix) rather than mistaking it for compatible. A legacy daemon
// spoke ProtocolVersion 1 and read a BARE 4-byte version (no tag); this stands one
// up and confirms the current Dial classifies it as skew. It is RED until
// ProtocolVersion is bumped past the legacy value 1 (at v1 the legacy reply matches
// the client and the wire change goes undetected).
func TestVersionSkew_DetectsLegacyUntaggedDaemon(t *testing.T) {
	dir, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "legacy.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	// A legacy (pre-'V') daemon: read exactly 4 untagged bytes, reply version 1.
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer conn.Close()
				var hdr [4]byte
				if _, rerr := io.ReadFull(conn, hdr[:]); rerr != nil {
					return
				}
				var out [4]byte
				binary.BigEndian.PutUint32(out[:], 1) // legacy ProtocolVersion
				_, _ = conn.Write(out[:])
			}()
		}
	}()

	_, err = Dial(sock, ProtocolVersion)
	if !errors.Is(err, ErrVersionSkew) {
		t.Fatalf("Dial against a legacy untagged daemon = %v; want ErrVersionSkew "+
			"(the 'V' wire change must be a version bump so D-8 detects it)", err)
	}
	if !strings.Contains(err.Error(), "swarm daemon restart") {
		t.Fatalf("skew error must name the fix `swarm daemon restart`; got %q", err.Error())
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
