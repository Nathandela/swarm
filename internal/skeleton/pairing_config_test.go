package skeleton

// FAILING-FIRST tests for slice A4-1b: loading the machine's pairing identity
// (internal/remote/machineid, just landed) into coreAPI.pairing. Today
// pairingConfig (pairing.go) is wired NOWHERE in production — d.api.pairing is
// always nil, so BeginPairing always fails closed with "pairing not configured".
//
// INTENDED PRODUCTION (RED — none of this exists yet; GREEN implements it):
//
//	// loadPairingConfig reads the machine identity from
//	// <stateDir>/remote/machine.key (the same path cmd/swarm's `remote init`
//	// writes, see cmd/swarm/remote_test.go) and maps it onto a *pairingConfig.
//	// TRI-STATE, mirroring loadRemoteLaunchPolicy's fail-closed posture but for a
//	// case where "unconfigured" and "corrupt" must be told apart (unlike the
//	// launch policy's uniform deny-all):
//	//   - identity file MISSING       -> (nil, nil)   pairing unsupported, daemon starts fine
//	//   - identity file present, OK   -> (*pairingConfig, nil), fields mapped from the
//	//                                    machineid.Identity accessors
//	//   - identity file CORRUPT       -> (nil, non-nil error) — fail closed; Serve()
//	//                                    must refuse to start the daemon, mirroring how a
//	//                                    corrupt device registry fails assembly (serve.go's
//	//                                    device.Open error check).
//	// NewRendezvous is left nil in this slice (the real relay adapter is A3.3-e).
//	func loadPairingConfig(stateDir string) (*pairingConfig, error)
//
//	// serve.go calls loadPairingConfig(cfg.StateDir) and, on a non-nil error,
//	// aborts assembly (return nil, err) exactly like the device.Open check above it;
//	// on success it sets d.api.pairing = cfg (nil or non-nil, either is a valid
//	// wire — a nil cfg simply leaves pairing unsupported).
//
// RED today: loadPairingConfig does not exist, so this file does not compile — an
// acceptable compile-fail RED for a new API, unambiguous by name.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/machineid"
)

// writeTestIdentity generates a machine identity and saves it at the
// <stateDir>/remote/machine.key path loadPairingConfig must read, returning the
// identity so the test can compare loaded fields against its accessors.
func writeTestIdentity(t *testing.T, stateDir, hostname string) *machineid.Identity {
	t.Helper()
	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", remoteDir, err)
	}
	id, err := machineid.Generate(hostname)
	if err != nil {
		t.Fatalf("machineid.Generate: %v", err)
	}
	if err := id.Save(filepath.Join(remoteDir, "machine.key")); err != nil {
		t.Fatalf("Save identity: %v", err)
	}
	return id
}

// TestLoadPairingConfig_MapsIdentity pins the field-by-field mapping from a
// present, valid machine identity onto pairingConfig.
func TestLoadPairingConfig_MapsIdentity(t *testing.T) {
	dir := t.TempDir()
	id := writeTestIdentity(t, dir, "pairing-test-host")

	cfg, err := loadPairingConfig(dir)
	if err != nil {
		t.Fatalf("loadPairingConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("loadPairingConfig returned a nil config for a present, valid identity")
	}

	if cfg.Static == nil {
		t.Error("cfg.Static is nil; want the identity's Noise-static handshake handle")
	}
	if !bytes.Equal(cfg.RecipientPub, id.RecipientPublic()) {
		t.Errorf("cfg.RecipientPub = %x, want %x", cfg.RecipientPub, id.RecipientPublic())
	}
	if !bytes.Equal(cfg.SignPub, id.GrantSignPublic()) {
		t.Errorf("cfg.SignPub = %x, want %x", cfg.SignPub, id.GrantSignPublic())
	}
	if !bytes.Equal(cfg.SignPriv, id.GrantSignPrivate()) {
		t.Error("cfg.SignPriv does not match id.GrantSignPrivate()")
	}
	if cfg.EpochID != id.EpochID() {
		t.Errorf("cfg.EpochID = %d, want %d", cfg.EpochID, id.EpochID())
	}
	if cfg.GrantSeq != id.GrantSeq() {
		t.Errorf("cfg.GrantSeq = %d, want %d", cfg.GrantSeq, id.GrantSeq())
	}
	if cfg.EpochKeys != id.EpochKeys() {
		t.Error("cfg.EpochKeys does not match id.EpochKeys()")
	}
	if cfg.Hostname != id.Hostname() {
		t.Errorf("cfg.Hostname = %q, want %q", cfg.Hostname, id.Hostname())
	}
	if !bytes.Equal(cfg.RoutingID, id.RoutingID()) {
		t.Errorf("cfg.RoutingID = %x, want %x", cfg.RoutingID, id.RoutingID())
	}
	if !bytes.Equal(cfg.RelayAuthPub, id.RelayAuthPublic()) {
		t.Errorf("cfg.RelayAuthPub = %x, want %x", cfg.RelayAuthPub, id.RelayAuthPublic())
	}
}

// TestLoadPairingConfig_MissingIsNilNoError pins the tri-state's first arm: with no
// identity file at all, pairing is simply unsupported — (nil, nil), not an error —
// so a daemon with no provisioned machine identity still starts fine.
func TestLoadPairingConfig_MissingIsNilNoError(t *testing.T) {
	dir := t.TempDir() // empty: no <dir>/remote/machine.key

	cfg, err := loadPairingConfig(dir)
	if err != nil {
		t.Fatalf("loadPairingConfig on a missing identity file returned an error: %v; want (nil, nil)", err)
	}
	if cfg != nil {
		t.Error("loadPairingConfig on a missing identity file returned a non-nil config; want nil (pairing unsupported until `swarm remote init`)")
	}
}

// TestLoadPairingConfig_CorruptFailsClosed pins the tri-state's third arm: a
// present but corrupt/malformed identity file is a HARD failure (non-nil error,
// nil config) — never a silently-empty or partially-populated pairingConfig. This
// is machine key custody's fail-closed posture, mirroring the device-registry
// check in serve.go.
func TestLoadPairingConfig_CorruptFailsClosed(t *testing.T) {
	dir := t.TempDir()
	remoteDir := filepath.Join(dir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", remoteDir, err)
	}
	if err := os.WriteFile(filepath.Join(remoteDir, "machine.key"), []byte("not a machine identity file"), 0o600); err != nil {
		t.Fatalf("write corrupt identity file: %v", err)
	}

	cfg, err := loadPairingConfig(dir)
	if err == nil {
		t.Fatal("loadPairingConfig on a corrupt identity file returned a nil error; must fail closed")
	}
	if cfg != nil {
		t.Error("loadPairingConfig on a corrupt identity file returned a non-nil config despite the error")
	}
}

// assembleWithMachineIdentity stands up the full in-process assembly over a
// short-pathed state dir, optionally seeding a valid or corrupt machine identity
// first, so Serve()'s own wiring/fail-closed behavior (not just the loader in
// isolation) is pinned. ShimBinary is a placeholder: these tests never launch a
// session, and daemon.Open only touches ShimBinary when reconnecting an existing
// one (there is none in a fresh state dir), so no real shim binary is required.
func assembleWithMachineIdentity(t *testing.T, seed func(stateDir string)) (*Daemon, error) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swpc")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	if seed != nil {
		seed(dir)
	}

	sk, serveErr := Serve(Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "d.sock"),
		LockPath:    filepath.Join(dir, "d.lock"),
		LogPath:     filepath.Join(dir, "d.log"),
		ShimBinary:  "/bin/true",
		MaxSessions: 4,
	})
	if sk != nil {
		t.Cleanup(func() { _ = sk.Close() })
	}
	return sk, serveErr
}

// TestServe_WiresPairingConfigWhenIdentityPresent pins that Serve() actually calls
// loadPairingConfig and sets d.api.pairing when a valid machine identity is present
// — not just that the loader function exists in isolation.
func TestServe_WiresPairingConfigWhenIdentityPresent(t *testing.T) {
	sk, err := assembleWithMachineIdentity(t, func(stateDir string) {
		writeTestIdentity(t, stateDir, "assemble-host")
	})
	if err != nil {
		t.Fatalf("Serve with a present, valid machine identity: %v", err)
	}
	if sk.api.pairing == nil {
		t.Error("sk.api.pairing is nil after Serve with a present, valid machine identity; want it wired from loadPairingConfig")
	}
}

// TestServe_CorruptIdentityFailsAssembly pins the fail-closed contract at the
// Serve() level: a corrupt machine identity file must abort assembly entirely
// (non-nil error, nil *Daemon) — mirroring how a corrupt device registry fails
// assembly in serve.go — rather than starting a daemon with pairing silently
// broken or half-configured.
func TestServe_CorruptIdentityFailsAssembly(t *testing.T) {
	sk, err := assembleWithMachineIdentity(t, func(stateDir string) {
		remoteDir := filepath.Join(stateDir, "remote")
		if mkErr := os.MkdirAll(remoteDir, 0o700); mkErr != nil {
			t.Fatalf("mkdir %s: %v", remoteDir, mkErr)
		}
		if wErr := os.WriteFile(filepath.Join(remoteDir, "machine.key"), []byte("garbage"), 0o600); wErr != nil {
			t.Fatalf("write corrupt identity: %v", wErr)
		}
	})
	if err == nil {
		t.Fatal("Serve with a corrupt machine identity file returned a nil error; assembly must fail closed")
	}
	if sk != nil {
		t.Error("Serve with a corrupt machine identity file returned a non-nil *Daemon")
	}
}
