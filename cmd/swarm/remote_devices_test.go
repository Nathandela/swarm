package main

// FAILING-FIRST tests for slice A4-cli: the `swarm remote devices` / `swarm remote
// revoke <device-id>` CLI subcommands, built on the protocol.Client.ListDevices/
// RevokeDevice methods pinned in internal/protocol/client_devices_test.go.
//
// INTENDED PRODUCTION (RED — none of this exists yet; GREEN implements it):
//
//	// runRemote gains two routed verbs alongside the existing "init":
//	//   "devices" -> runRemoteDevices
//	//   "revoke"  -> runRemoteRevoke
//
//	// runRemoteDevices dials the owner socket (dialClient, hello caps including
//	// "pairing"), calls ListDevices(), and prints a table (device id, name,
//	// capability, paired-at) to stdout. An empty registry is clean output, exit 0.
//	func runRemoteDevices(args []string, stdout, stderr io.Writer) int
//
//	// runRemoteRevoke requires exactly one positional arg (the device id); with zero
//	// or more than one, it prints a usage error to stderr and returns nonzero without
//	// dialing. With exactly one, it dials, calls RevokeDevice(arg), and prints a
//	// confirmation to stdout on success.
//	func runRemoteRevoke(args []string, stdout, stderr io.Writer) int
//
// A live in-process daemon IS feasible in this package without spawning a `swarm
// daemon` subprocess: internal/skeleton.Serve stands up the real assembled daemon
// (including its coreAPI's DeviceLister/DeviceRevoker, backed by a real
// internal/remote/device.Registry opened from <stateDir>/devices) as a plain Go value
// in-process; its main socket's demux already answers the daemon.EnsureDaemon
// version-probe handshake dialClient()'s auto-start relies on (see
// cmd/swarm/tui_smoke_test.go's PTY smoke, which spawns a real subprocess for a
// different reason — driving a live PTY — not because an in-process daemon can't
// serve the protocol). Seeding a device must happen BEFORE skeleton.Serve, since its
// device.Registry loads the on-disk file once at Open and does not hot-reload.
//
// RED today: runRemoteDevices/runRemoteRevoke (and the "devices"/"revoke" dispatch
// routes) do not exist, so this file does not compile — an acceptable compile-fail
// RED for a new API, unambiguous by name.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/skeleton"
)

// shortStateDir returns a short-pathed temp dir under /tmp — NOT t.TempDir(), whose
// path embeds the sanitized test name and, once daemon.sock is appended, overflows
// the ~104-byte UNIX socket path cap (mirrors internal/protocol/harness_test.go's
// tmpSock, which exists for the identical reason).
func shortStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swcli")
	if err != nil {
		t.Fatalf("mkdir short state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// TestRemoteRevoke_RequiresOneArg pins `swarm remote revoke`'s arg-parsing contract:
// zero or two-plus positional args is a usage error (nonzero exit), and NEVER dials a
// daemon to get there (no SWARM_DAEMON_* env is set in this test).
func TestRemoteRevoke_RequiresOneArg(t *testing.T) {
	t.Run("no device id", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		exit := runRemote([]string{"revoke"}, &stdout, &stderr)
		if exit == 0 {
			t.Fatal("runRemote([revoke]) exit = 0, want nonzero")
		}
		combined := strings.ToLower(stdout.String() + stderr.String())
		if !strings.Contains(combined, "usage") {
			t.Errorf("runRemote([revoke]) output = %q, want a usage substring", combined)
		}
	})

	t.Run("too many args", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		exit := runRemote([]string{"revoke", "a", "b"}, &stdout, &stderr)
		if exit == 0 {
			t.Fatal("runRemote([revoke a b]) exit = 0, want nonzero")
		}
		// Assert a USAGE refusal specifically (not merely nonzero): today "revoke" is
		// unrouted, so runRemote's unknown-verb fallback ALSO returns nonzero for the
		// wrong reason. Requiring "usage" keeps this red until real arg-count
		// validation exists, rather than passing on the unrelated fallback path.
		combined := strings.ToLower(stdout.String() + stderr.String())
		if !strings.Contains(combined, "usage") {
			t.Errorf("runRemote([revoke a b]) output = %q, want a usage substring", combined)
		}
	})
}

// seedDevice pairs one device directly into the registry on disk at
// <stateDir>/devices, BEFORE any daemon opens it, so a from-scratch skeleton.Serve
// reads it back as an already-paired device. The device id is self-authenticating
// (device.DeviceIDFor of the command-signing key), so a random 32-byte key is
// generated and the id derived from it, exactly as production pairing would.
func seedDevice(t *testing.T, stateDir, name string, cap device.Capability) string {
	t.Helper()
	pub := make([]byte, ed25519.PublicKeySize)
	if _, err := rand.Read(pub); err != nil {
		t.Fatalf("generate command-signing pubkey: %v", err)
	}
	id := device.DeviceIDFor(pub)

	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatalf("device.Open: %v", err)
	}
	rec := device.Record{
		DeviceID:       id,
		Name:           name,
		NoiseStaticPub: make([]byte, 32),
		RelayAuthPub:   make([]byte, 32),
		CommandSignPub: pub,
		RecipientPub:   make([]byte, 32),
		RoutingID:      []byte{1, 2, 3, 4},
		Capability:     cap,
		PairedAt:       time.Now().Truncate(time.Second),
		GrantedEpoch:   1,
	}
	if err := reg.Add(rec); err != nil {
		t.Fatalf("device registry Add: %v", err)
	}
	return id
}

// startCLIDaemon stands up a REAL in-process daemon (internal/skeleton.Serve, no
// subprocess) rooted at stateDir, and points the SWARM_DAEMON_* environment
// dialClient()/daemon.EnsureDaemon read at its socket/lock/log paths — so
// runRemoteDevices/runRemoteRevoke's dial finds it already live (EnsureDaemon's first
// Dial attempt succeeds; it never spawns).
func startCLIDaemon(t *testing.T, stateDir string) {
	t.Helper()
	sock := filepath.Join(stateDir, "daemon.sock")
	lock := filepath.Join(stateDir, "daemon.lock")
	logPath := filepath.Join(stateDir, "daemon.log")
	t.Setenv(daemon.EnvStateDir, stateDir)
	t.Setenv(daemon.EnvSocket, sock)
	t.Setenv(daemon.EnvLock, lock)
	t.Setenv(daemon.EnvLog, logPath)

	sk, err := skeleton.Serve(skeleton.Config{
		StateDir:    stateDir,
		SocketPath:  sock,
		LockPath:    lock,
		LogPath:     logPath,
		MaxSessions: 4,
	})
	if err != nil {
		t.Fatalf("skeleton.Serve: %v", err)
	}
	t.Cleanup(func() { _ = sk.Close() })
}

// TestRemoteDevices_PrintsTable drives `swarm remote devices` against a REAL
// in-process daemon with one seeded paired device, and asserts the printed table
// names it (device id + name).
func TestRemoteDevices_PrintsTable(t *testing.T) {
	dir := shortStateDir(t)
	id := seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
	startCLIDaemon(t, dir)

	var stdout, stderr bytes.Buffer
	exit := runRemote([]string{"devices"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("runRemote([devices]) exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, id) {
		t.Errorf("devices table missing seeded device id %q; got:\n%s", id, out)
	}
	if !strings.Contains(out, "Nathan's iPhone") {
		t.Errorf("devices table missing seeded device name; got:\n%s", out)
	}
}

// TestRemoteDevices_EmptyRegistry drives `swarm remote devices` against a REAL
// in-process daemon with NO paired devices: clean output, exit 0 (no crash on an
// empty roster).
func TestRemoteDevices_EmptyRegistry(t *testing.T) {
	dir := shortStateDir(t)
	startCLIDaemon(t, dir)

	var stdout, stderr bytes.Buffer
	exit := runRemote([]string{"devices"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("runRemote([devices]) exit = %d, want 0 on an empty registry; stderr=%q", exit, stderr.String())
	}
}

// TestRemoteRevoke_Removes drives `swarm remote revoke <id>` against a REAL
// in-process daemon with two seeded devices, and proves the targeted device is gone
// from a subsequent `swarm remote devices` while the other survives.
func TestRemoteRevoke_Removes(t *testing.T) {
	dir := shortStateDir(t)
	targetID := seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
	keepID := seedDevice(t, dir, "Nathan's iPad", device.CapReadOnly)
	startCLIDaemon(t, dir)

	var revokeOut, revokeErr bytes.Buffer
	if exit := runRemote([]string{"revoke", targetID}, &revokeOut, &revokeErr); exit != 0 {
		t.Fatalf("runRemote([revoke %s]) exit = %d, want 0; stderr=%q", targetID, exit, revokeErr.String())
	}

	var listOut, listErr bytes.Buffer
	if exit := runRemote([]string{"devices"}, &listOut, &listErr); exit != 0 {
		t.Fatalf("runRemote([devices]) after revoke exit = %d, want 0; stderr=%q", exit, listErr.String())
	}
	out := listOut.String()
	if strings.Contains(out, targetID) {
		t.Errorf("revoked device %q still present after revoke; devices table:\n%s", targetID, out)
	}
	if !strings.Contains(out, keepID) {
		t.Errorf("un-revoked device %q missing after revoking a DIFFERENT device; devices table:\n%s", keepID, out)
	}
}
