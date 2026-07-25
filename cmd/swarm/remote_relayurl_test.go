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
	"strconv"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/pairing"
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

// TestRemoteInit_RejectsARelayURLTheQRCannotCarry is slice S3 review finding B1: the
// relay URL written here is the ONE free variable in the pairing QR's size budget
// (internal/remote/qrterm/qrterm_test.go's TestRender_FitsAStandard80x24TerminalByRelayURLLength
// derives the ceiling from the shipped codec and renderer), and today nothing bounds it.
// The overflow is silent AND misattributed: past the ceiling `swarm remote pair` draws no
// symbol at all and blames the terminal, sending the operator to resize an 80x24 window
// that was never the problem. The config file is where the cause is, so the config writer
// is where the refusal belongs — before the bad value is ever persisted.
//
// Fail fast and leave NOTHING behind: a rejected flag must write no relay.json AND no
// machine identity, so a corrected re-run starts from a clean state dir.
func TestRemoteInit_RejectsARelayURLTheQRCannotCarry(t *testing.T) {
	tooLong := "wss://" + strings.Repeat("a", pairing.MaxRelayURLLen+1-len("wss://"))
	cases := []struct {
		name string
		url  string
	}{
		// Real endpoints measured in the review: both draw no QR on a standard terminal.
		{"regional relay host", "wss://swarm-relay.us-east-1.example.com:8443"},
		{"per-tenant rendezvous path", "wss://relay.example.com:8443/tenants/acme/rendezvous"},
		// Exactly one character past the ceiling: the cliff is one character wide.
		{"one character over the ceiling", tooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.url) <= pairing.MaxRelayURLLen {
				t.Fatalf("test bug: %q is %d characters, within the %d-character ceiling",
					tc.url, len(tc.url), pairing.MaxRelayURLLen)
			}
			dir := t.TempDir()
			t.Setenv(daemon.EnvStateDir, dir)

			var stdout, stderr bytes.Buffer
			exit := runRemoteInit([]string{"--relay-url", tc.url}, &stdout, &stderr)
			if exit == 0 {
				t.Fatalf("runRemoteInit accepted a %d-character relay URL (exit 0); `swarm remote "+
					"pair` will then draw NO QR symbol and blame the terminal for it. The limit is "+
					"%d characters (PB-PAIR-1(b) on a standard terminal).", len(tc.url), pairing.MaxRelayURLLen)
			}
			if msg := stderr.String(); !strings.Contains(msg, strconv.Itoa(pairing.MaxRelayURLLen)) {
				t.Errorf("rejection message %q does not state the %d-character limit; an operator "+
					"cannot fix a bound they are not told", msg, pairing.MaxRelayURLLen)
			}
			if _, err := os.Stat(relayConfigPath(dir)); !os.IsNotExist(err) {
				t.Errorf("relay.json was written despite the refusal (err=%v); the bad endpoint "+
					"would be read back by the daemon on the next start", err)
			}
			if _, err := os.Stat(remoteIdentityPath(dir)); !os.IsNotExist(err) {
				t.Errorf("machine.key was provisioned despite the refusal (err=%v); a rejected "+
					"flag must leave the state dir untouched", err)
			}
		})
	}
}

// TestRemoteInit_RejectsAnUndialableRelayURL pins the rest of the write-time contract
// (S3 review B1/N3): --relay-url is carried VERBATIM into the QR as the only address a
// scanning phone will ever have, so a value no phone can dial must never reach the file.
// Whitespace-only is called out explicitly by N3 — it is non-empty, so today it is
// persisted and read back as a configured endpoint.
func TestRemoteInit_RejectsAnUndialableRelayURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"no scheme", "relay.example.com:8443"},
		{"wrong scheme", "https://relay.example.com"},
		{"no host", "wss://"},
		{"whitespace only", "   "},
		{"surrounding whitespace", " ws://127.0.0.1:9999 "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(daemon.EnvStateDir, dir)

			var stdout, stderr bytes.Buffer
			if exit := runRemoteInit([]string{"--relay-url", tc.url}, &stdout, &stderr); exit == 0 {
				t.Fatalf("runRemoteInit accepted --relay-url %q (exit 0); a phone that scans the "+
					"pairing QR has this string and nothing else to dial", tc.url)
			}
			if _, err := os.Stat(relayConfigPath(dir)); !os.IsNotExist(err) {
				t.Errorf("relay.json was written despite the refusal (err=%v)", err)
			}
		})
	}
}

// TestRemoteInit_AcceptsARelayURLAtTheCeiling is the other side of the bound: the limit
// must be the exact derived ceiling, so a URL of exactly that length is still written —
// and written VERBATIM, since the machine's own dial target is the one endpoint known
// reachable and a normalized URL is a different destination.
func TestRemoteInit_AcceptsARelayURLAtTheCeiling(t *testing.T) {
	base := "wss://relay.example.com:8443/"
	if len(base) > pairing.MaxRelayURLLen {
		t.Fatalf("test bug: the base URL is already %d characters, over the %d ceiling",
			len(base), pairing.MaxRelayURLLen)
	}
	atCeiling := base + strings.Repeat("r", pairing.MaxRelayURLLen-len(base))
	if len(atCeiling) != pairing.MaxRelayURLLen {
		t.Fatalf("test bug: built a %d-character URL, want exactly %d", len(atCeiling), pairing.MaxRelayURLLen)
	}

	dir := t.TempDir()
	t.Setenv(daemon.EnvStateDir, dir)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit([]string{"--relay-url", atCeiling}, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit refused a relay URL of exactly %d characters (exit %d, stderr=%q); "+
			"the ceiling must be the derived one, not a round number below it",
			len(atCeiling), exit, stderr.String())
	}
	b, err := os.ReadFile(relayConfigPath(dir))
	if err != nil {
		t.Fatalf("read relay.json: %v", err)
	}
	var rc relayConfigFile
	if err := json.Unmarshal(b, &rc); err != nil {
		t.Fatalf("relay.json is not valid JSON: %v (content: %s)", err, b)
	}
	if rc.RelayURL != atCeiling {
		t.Errorf("relay.json relay_url = %q, want %q verbatim", rc.RelayURL, atCeiling)
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
