package main

// FAILING-FIRST tests for slice A4-1b: the `swarm remote init` CLI verb and its
// dispatch wiring. Machine key custody: init must persist the machine identity at
// 0600, never print private material, and be IDEMPOTENT (a second run loads the
// existing identity rather than rotating keys).
//
// INTENDED PRODUCTION (RED — none of this exists yet; GREEN implements it):
//
//	// runRemoteInit is the `swarm remote init` verb. It resolves the state dir the
//	// same way dialClient does (SWARM_DAEMON_STATE / persist.DefaultDir), generates a
//	// machineid.Identity at <stateDir>/remote/machine.key if none exists yet, saves it
//	// 0600, and prints the PUBLIC fingerprint (identity.String()) to stdout. A second
//	// run loads the existing identity instead of rotating it (idempotent) and prints
//	// the same fingerprint. It never prints private material.
//	func runRemoteInit(args []string, stdout, stderr io.Writer) int
//
//	// dispatch gains a "remote" case: `swarm remote init` -> runRemoteInit;
//	// `swarm remote` (no verb) -> usage (mentions init/devices/revoke/pair/off/on/
//	// status), nonzero exit; `swarm remote <unknown verb>` -> unknown-verb error,
//	// nonzero exit.
//
// The identity path this test pins, <stateDir>/remote/machine.key, is also the path
// internal/skeleton.loadPairingConfig must read from (internal/skeleton/pairing_config_test.go)
// — the CLI and the daemon assembly must agree on it.
//
// RED today: runRemoteInit does not exist and dispatch has no "remote" case, so this
// file does not compile — an acceptable compile-fail RED for a new API, unambiguous
// by name.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/machineid"
)

// fingerprint mirrors machineid.Identity.String()'s own redaction fingerprint
// (internal/remote/machineid/machineid.go's keyFingerprint, pinned by
// TestMachineIdentity_StringRedactsPrivateKeys): the first 8 bytes of SHA-256 of a
// public key, hex-encoded. Used here only to recognize a safe, public identifier in
// `remote init`'s stdout, never a private one.
func fingerprint(pub []byte) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// remoteIdentityPath mirrors the path GREEN's runRemoteInit (and
// internal/skeleton.loadPairingConfig) must use: <stateDir>/remote/machine.key.
func remoteIdentityPath(stateDir string) string {
	return filepath.Join(stateDir, "remote", "machine.key")
}

// TestRemoteInit_CreatesIdentityFile pins machine key custody: `swarm remote init`
// persists a machineid identity at 0600 under the state dir, prints a recognizable
// PUBLIC fingerprint to stdout, and never prints the raw grant-signing private key
// (the one raw private machineid.Identity exposes an accessor for).
func TestRemoteInit_CreatesIdentityFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(daemon.EnvStateDir, dir)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, stderr.String())
	}

	path := remoteIdentityPath(dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat identity file at %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("identity file perms = %o, want 0600", perm)
	}

	id, err := machineid.Load(path)
	if err != nil {
		t.Fatalf("machineid.Load(%s): %v", path, err)
	}

	out := stdout.String()
	if strings.TrimSpace(out) == "" {
		t.Fatal("runRemoteInit printed nothing to stdout; want a public fingerprint")
	}

	// stdout must carry a recognizable PUBLIC identifier: the fingerprint algorithm
	// identity.String() uses (sha256(pub)[:8] hex) applied to the recipient public key.
	fp := fingerprint(id.RecipientPublic())
	if !strings.Contains(out, fp) {
		t.Errorf("stdout %q does not contain the recipient-pubkey fingerprint %q; want identity.String() printed", out, fp)
	}

	// NEVER print private material. The raw grant-signing private is the one private
	// key machineid.Identity exposes an accessor for (enroll.Enroll needs it); it must
	// not leak into stdout, raw or hex-encoded.
	priv := id.GrantSignPrivate()
	if bytes.Contains(stdout.Bytes(), priv) {
		t.Error("stdout leaks the raw grant-signing private key bytes")
	}
	if strings.Contains(out, hex.EncodeToString(priv)) {
		t.Error("stdout leaks the hex-encoded grant-signing private key")
	}
}

// TestRemoteInit_Idempotent pins that a second `swarm remote init` LOADS the
// existing identity rather than rotating it: the on-disk bytes are byte-for-byte
// unchanged, and the printed fingerprint is identical across both runs.
func TestRemoteInit_Idempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(daemon.EnvStateDir, dir)
	path := remoteIdentityPath(dir)

	var out1, errOut1 bytes.Buffer
	if exit := runRemoteInit(nil, &out1, &errOut1); exit != 0 {
		t.Fatalf("first runRemoteInit exit = %d, want 0; stderr=%q", exit, errOut1.String())
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read identity file after first run: %v", err)
	}

	var out2, errOut2 bytes.Buffer
	if exit := runRemoteInit(nil, &out2, &errOut2); exit != 0 {
		t.Fatalf("second runRemoteInit exit = %d, want 0; stderr=%q", exit, errOut2.String())
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read identity file after second run: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Error("identity file bytes changed between two `remote init` runs; init must be idempotent (load existing keys, never rotate)")
	}
	if out1.String() != out2.String() {
		t.Errorf("printed fingerprint changed between two runs: first=%q second=%q", out1.String(), out2.String())
	}
}

// TestDispatch_RemoteUsageAndUnknownVerb pins the `remote` subcommand's verb
// routing, in the same style as TestDispatch: `swarm remote` (no verb) prints usage
// mentioning every documented verb and exits nonzero; `swarm remote bogus` reports
// an unknown-verb error and exits nonzero.
func TestDispatch_RemoteUsageAndUnknownVerb(t *testing.T) {
	t.Run("no verb prints usage", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		exit := dispatch([]string{"remote"}, &stdout, &stderr)
		if exit == 0 {
			t.Fatal("dispatch([remote]) exit = 0, want nonzero")
		}
		combined := strings.ToLower(stdout.String() + stderr.String())
		for _, verb := range []string{"init", "devices", "revoke", "pair", "off", "on", "status"} {
			if !strings.Contains(combined, verb) {
				t.Errorf("remote usage missing verb %q; stdout=%q stderr=%q", verb, stdout.String(), stderr.String())
			}
		}
	})

	t.Run("unknown verb reports error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		exit := dispatch([]string{"remote", "bogus"}, &stdout, &stderr)
		if exit == 0 {
			t.Fatal("dispatch([remote bogus]) exit = 0, want nonzero")
		}
		combined := strings.ToLower(stdout.String() + stderr.String())
		if !strings.Contains(combined, "unknown") {
			t.Errorf("dispatch([remote bogus]) output = %q, want an unknown-verb substring", combined)
		}
	})
}
