package main

// FAILING-FIRST tests for slice A4-relayurl: `swarm remote init --relay-url <url>`
// (the WRITER half of A3.3-e's relay-URL config contract). The READER already
// exists: internal/skeleton/pairing_config.go's loadRelayURL reads
// <stateDir>/remote/relay.json, expecting the EXACT shape {"relay_url":"..."} (see
// pairing_config.go's remoteRelayFile const and loadRelayURL doc comment, and
// internal/skeleton/pairing_relay_test.go's writeRelayURL helper, which pins the same
// path/shape/perms: json.Marshal(map[string]string{"relay_url": url}) written 0600
// under <stateDir>/remote/, itself mkdir'd 0700).
//
// INTENDED PRODUCTION (RED — none of this exists yet; GREEN implements EXACTLY this):
//
//	// runRemoteInit gains a --relay-url flag, parsed via flag.NewFlagSet (the same
//	// convention runShim uses in main.go for --config: fs.String, fs.Parse(args)).
//	// When provided, AFTER writing the machine identity, it writes
//	// <stateDir>/remote/relay.json as {"relay_url":"<url>"} at 0600 — byte-for-byte
//	// the shape loadRelayURL reads. When the flag is absent, no relay.json is written
//	// at all (pairing stays relay-unconfigured; loadPairingConfig's NewRendezvous
//	// stays nil).
//	func runRemoteInit(args []string, stdout, stderr io.Writer) int
//
// RED today: runRemoteInit's current signature discards args entirely
// (`func runRemoteInit(_ []string, stdout, stderr io.Writer) int`) and never writes
// relay.json under any circumstance, so TestRemoteInit_RelayURLWritesConfig fails
// (relay.json never materializes). TestRemoteInit_NoRelayURLWritesNoConfig happens to
// already pass today (init never writes relay.json in ANY case yet) — it is included
// as the paired regression guard the writer must preserve once it lands, not as new
// failing evidence. TestRemoteInit_RelayURLWritesConfig is the behavioral RED to
// inspect.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
)

// relayConfigPath mirrors remoteRelayFile in internal/skeleton/pairing_config.go:
// <stateDir>/remote/relay.json is the exact path the reader (loadRelayURL) uses, and
// the path GREEN's writer must agree on.
func relayConfigPath(stateDir string) string {
	return filepath.Join(stateDir, "remote", "relay.json")
}

// relayConfigFile is the JSON shape loadRelayURL unmarshals relay.json into
// (pairing_config.go: `var rc struct { RelayURL string `json:"relay_url"` }`).
// Duplicated here (rather than imported) because relayConfigFile's fields are
// unexported in package skeleton and cmd/swarm is package main.
type relayConfigFile struct {
	RelayURL string `json:"relay_url"`
}

// TestRemoteInit_RelayURLWritesConfig pins the headline writer behavior: `swarm
// remote init --relay-url <url>` must persist <stateDir>/remote/relay.json at 0600,
// containing {"relay_url":"<url>"} — the exact shape and perms
// internal/skeleton.loadRelayURL reads back (pairing_relay_test.go's writeRelayURL
// helper pins the same contract on the reader side).
//
// RED today: runRemoteInit ignores its args parameter entirely, so no relay.json is
// ever written; os.Stat on the expected path fails with ErrNotExist.
func TestRemoteInit_RelayURLWritesConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(daemon.EnvStateDir, dir)

	const wantURL = "ws://127.0.0.1:9999"

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit([]string{"--relay-url", wantURL}, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit([--relay-url %s]) exit = %d, want 0; stderr=%q", wantURL, exit, stderr.String())
	}

	path := relayConfigPath(dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat relay.json at %s: %v; want it written when --relay-url is provided", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("relay.json perms = %o, want 0600 (matching machine.key custody)", perm)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read relay.json: %v", err)
	}
	var rc relayConfigFile
	if err := json.Unmarshal(b, &rc); err != nil {
		t.Fatalf("relay.json is not valid JSON in the {\"relay_url\":...} shape loadRelayURL expects: %v (content: %s)", err, b)
	}
	if rc.RelayURL != wantURL {
		t.Errorf("relay.json relay_url = %q, want %q", rc.RelayURL, wantURL)
	}
}

// TestRemoteInit_NoRelayURLWritesNoConfig pins the fail-closed default: `swarm remote
// init` WITHOUT --relay-url must never write relay.json, so
// internal/skeleton.loadPairingConfig continues to see relay.json absent and leaves
// pairingConfig.NewRendezvous nil (TestLoadPairingConfig_NoRelayURLLeavesRendezvousNil
// in pairing_relay_test.go pins that reader-side contract).
func TestRemoteInit_NoRelayURLWritesNoConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(daemon.EnvStateDir, dir)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit(nil) exit = %d, want 0; stderr=%q", exit, stderr.String())
	}

	path := relayConfigPath(dir)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("relay.json exists at %s despite no --relay-url flag; want it ABSENT (err=%v)", path, err)
	}
}

// TestRemoteInit_RelayURLStillCreatesIdentity guards against the --relay-url flag
// breaking machine key custody: providing it must still create
// <stateDir>/remote/machine.key at 0600, exactly as a plain `swarm remote init` does
// (TestRemoteInit_CreatesIdentityFile in remote_test.go pins the flag-less case).
func TestRemoteInit_RelayURLStillCreatesIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(daemon.EnvStateDir, dir)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit([]string{"--relay-url", "ws://127.0.0.1:9999"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit([--relay-url ...]) exit = %d, want 0; stderr=%q", exit, stderr.String())
	}

	idPath := remoteIdentityPath(dir)
	info, err := os.Stat(idPath)
	if err != nil {
		t.Fatalf("stat identity file at %s: %v; --relay-url must not skip identity provisioning", idPath, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("identity file perms = %o, want 0600", perm)
	}
}
