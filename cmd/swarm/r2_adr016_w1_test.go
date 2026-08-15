package main

// ADR-016 W1 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): the `swarm remote
// init` CLI half of the policy/pin independence ruling.
//
//	--relay-tls-policy {webpki|pinned_spki} selects the policy, and nothing else.
//	  Omitted, it is webpki.
//	--relay-pin keeps its EXACT current meaning: mandatory under pinned_spki, refused
//	  under webpki.
//	--relay-pin-compat supplies the W9 compatibility pin, legal ONLY under webpki.
//	One legacy inference survives, over the FLAG only, never over stored state AS A
//	  SELECTION -- stored state IS read by the demotion guard below, but only to REFUSE a
//	  run, never to choose a policy the operator did not type:
//	  --relay-pin with no --relay-tls-policy selects pinned_spki.
//
// Every refusal happens BEFORE any filesystem write (validateRelayPin's existing
// contract), so a rejected run provisions nothing.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

func r2w1RemoteInitStateDir(t *testing.T) (stateDir string, restore func()) {
	t.Helper()
	stateDir = t.TempDir()
	old := os.Getenv("SWARM_DAEMON_STATE")
	_ = os.Setenv("SWARM_DAEMON_STATE", stateDir)
	return stateDir, func() { _ = os.Setenv("SWARM_DAEMON_STATE", old) }
}

// TestADR016W1_RelayTLSPolicyOmittedDefaultsToWebPKI: "Omitted, it is webpki."
func TestADR016W1_RelayTLSPolicyOmittedDefaultsToWebPKI(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit([]string{"--relay-url", "wss://swarm-relay.example.com:8443"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("remote init exit=%d stderr=%s", exit, stderr.String())
	}
	cfg, found, err := relaycfg.Load(stateDir)
	if err != nil || !found {
		t.Fatalf("relaycfg.Load: found=%v err=%v", found, err)
	}
	if cfg.TLSPolicy != relaycfg.PolicyWebPKI {
		t.Errorf("TLSPolicy = %q, want %q (the default)", cfg.TLSPolicy, relaycfg.PolicyWebPKI)
	}
}

// TestADR016W1_RelayPinWithNoPolicyFlagInfersPinnedSPKI: the one legacy inference W1
// keeps, "so an operator's existing invocation keeps its exact present meaning".
func TestADR016W1_RelayPinWithNoPolicyFlagInfersPinnedSPKI(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	var stdout, stderr bytes.Buffer
	args := []string{"--relay-url", "wss://swarm-relay.example.com:8443", "--relay-pin", pin}
	if exit := runRemoteInit(args, &stdout, &stderr); exit != 0 {
		t.Fatalf("remote init exit=%d stderr=%s", exit, stderr.String())
	}
	cfg, _, err := relaycfg.Load(stateDir)
	if err != nil {
		t.Fatalf("relaycfg.Load: %v", err)
	}
	if cfg.TLSPolicy != relaycfg.PolicyPinnedSPKI {
		t.Errorf("TLSPolicy = %q, want %q inferred from --relay-pin alone", cfg.TLSPolicy, relaycfg.PolicyPinnedSPKI)
	}
	if cfg.SPKIPin != pin {
		t.Errorf("SPKIPin = %q, want %q", cfg.SPKIPin, pin)
	}
}

// TestADR016W1_RelayPinUnderWebPKIIsRefusedNamingCompat: "--relay-pin together with
// --relay-tls-policy webpki is a pre-write refusal that names --relay-pin-compat."
// Nothing is written: the second load below must find the SAME state the first left
// (in this case: nothing at all).
func TestADR016W1_RelayPinUnderWebPKIIsRefusedNamingCompat(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	var stdout, stderr bytes.Buffer
	args := []string{"--relay-url", "wss://swarm-relay.example.com:8443", "--relay-tls-policy", "webpki", "--relay-pin", pin}
	if exit := runRemoteInit(args, &stdout, &stderr); exit == 0 {
		t.Fatalf("remote init accepted --relay-pin with --relay-tls-policy webpki; W1 refuses this before any write")
	}
	if !strings.Contains(stderr.String(), "--relay-pin-compat") {
		t.Errorf("refusal %q does not name --relay-pin-compat as the supported route", stderr.String())
	}
	if _, found, _ := relaycfg.Load(stateDir); found {
		t.Errorf("a rejected `remote init` wrote relay.json; W1's pre-write refusal must provision nothing")
	}
}

// TestADR016W1_RelayPinCompatUnderPinnedSPKIIsRefused: "--relay-pin-compat supplies the
// W9 compatibility pin and is legal only under webpki."
func TestADR016W1_RelayPinCompatUnderPinnedSPKIIsRefused(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	var stdout, stderr bytes.Buffer
	args := []string{"--relay-url", "wss://swarm-relay.example.com:8443", "--relay-tls-policy", "pinned_spki",
		"--relay-pin", pin, "--relay-pin-compat", pin}
	if exit := runRemoteInit(args, &stdout, &stderr); exit == 0 {
		t.Fatalf("remote init accepted --relay-pin-compat under pinned_spki; W1 makes it webpki-only")
	}
	if _, found, _ := relaycfg.Load(stateDir); found {
		t.Errorf("a rejected `remote init` wrote relay.json")
	}
}

// TestADR016W1_PinnedSPKIWithNoPinIsRefused: "--relay-pin ... is MANDATORY under
// pinned_spki".
func TestADR016W1_PinnedSPKIWithNoPinIsRefused(t *testing.T) {
	_, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	var stdout, stderr bytes.Buffer
	args := []string{"--relay-url", "wss://swarm-relay.example.com:8443", "--relay-tls-policy", "pinned_spki"}
	if exit := runRemoteInit(args, &stdout, &stderr); exit == 0 {
		t.Fatalf("remote init accepted --relay-tls-policy pinned_spki with no --relay-pin; W1 makes the pin mandatory in this policy")
	}
}

// TestADR016W1_WebPKIWithCompatPinPublishesBothFieldsIndependently is the Conformance
// table's independence row, exercised end to end through the CLI: a webpki machine
// carrying a compatibility pin (W9's first rung) round-trips both values.
func TestADR016W1_WebPKIWithCompatPinPublishesBothFieldsIndependently(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	var stdout, stderr bytes.Buffer
	args := []string{"--relay-url", "wss://swarm-relay.example.com:8443", "--relay-tls-policy", "webpki", "--relay-pin-compat", pin}
	if exit := runRemoteInit(args, &stdout, &stderr); exit != 0 {
		t.Fatalf("remote init exit=%d stderr=%s", exit, stderr.String())
	}
	cfg, _, err := relaycfg.Load(stateDir)
	if err != nil {
		t.Fatalf("relaycfg.Load: %v", err)
	}
	if cfg.TLSPolicy != relaycfg.PolicyWebPKI {
		t.Errorf("TLSPolicy = %q, want %q", cfg.TLSPolicy, relaycfg.PolicyWebPKI)
	}
	if cfg.SPKIPin != pin {
		t.Errorf("SPKIPin = %q, want %q (the compatibility pin, published even under webpki)", cfg.SPKIPin, pin)
	}
}

// TestADR016W1_ReRunWithoutPolicyFlagRefusesToDemoteAnAlreadyPinnedMachine is the webpki
// punch-list's HIGH finding, reproduced exactly as the reviewer found it: re-running the
// documented, idempotent provisioning command on an already-pinned machine, with
// --relay-tls-policy simply OMITTED rather than mistyped, used to silently rewrite
// relay.json from pinned_spki to webpki with no warning and exit 0 -- W1's own rule
// ("a single flag would let one mistyped invocation move a machine between trust models in
// silence") and W6's ("Nothing demotes a policy silently") reached through an omitted flag
// instead of a mistyped one, because resolveRelayTLSPolicy never looked at what was already
// on disk before choosing the default.
func TestADR016W1_ReRunWithoutPolicyFlagRefusesToDemoteAnAlreadyPinnedMachine(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	var stdout, stderr bytes.Buffer
	first := []string{"--relay-url", "wss://swarm-relay.example.com:8443", "--relay-pin", pin}
	if exit := runRemoteInit(first, &stdout, &stderr); exit != 0 {
		t.Fatalf("first remote init exit=%d stderr=%s", exit, stderr.String())
	}
	cfg, _, err := relaycfg.Load(stateDir)
	if err != nil || cfg.TLSPolicy != relaycfg.PolicyPinnedSPKI {
		t.Fatalf("setup: TLSPolicy = %q err=%v, want %q pinned by the first run",
			cfg.TLSPolicy, err, relaycfg.PolicyPinnedSPKI)
	}

	stdout.Reset()
	stderr.Reset()
	second := []string{"--relay-url", "wss://swarm-relay.example.com:8443"}
	if exit := runRemoteInit(second, &stdout, &stderr); exit == 0 {
		t.Fatalf("re-running remote init with no --relay-tls-policy silently demoted an "+
			"already-pinned machine to webpki (stdout=%q); it must refuse, not rewrite", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--relay-tls-policy webpki") ||
		!strings.Contains(stderr.String(), "--relay-pin-compat") {
		t.Errorf("refusal %q does not name --relay-tls-policy webpki --relay-pin-compat as "+
			"the deliberate route", stderr.String())
	}
	cfg2, _, err := relaycfg.Load(stateDir)
	if err != nil {
		t.Fatalf("relaycfg.Load after the refused re-run: %v", err)
	}
	if cfg2.TLSPolicy != relaycfg.PolicyPinnedSPKI || cfg2.SPKIPin != pin {
		t.Fatalf("state after the refused re-run = policy %q pin %q; want the original "+
			"pinned_spki/%q UNCHANGED", cfg2.TLSPolicy, cfg2.SPKIPin, pin)
	}
}

// TestADR016W1_ReRunWithoutPolicyFlagRefusesToDemoteALegacyPinnedMachine is the
// re-verification pass's HIGH finding: the guard added above keys on exact string equality
// against relaycfg.PolicyPinnedSPKI, but the population that actually exists on disk before
// anyone has ever typed --relay-tls-policy is the LEGACY shape -- relay_spki_pin set, no
// relay_tls_policy key at all -- which relaycfg.Config.Security itself reads as pinned
// ("TLSPolicy == "" is the legacy shape ... and is read as pinned_spki here ... Only an
// EXPLICIT webpki policy withdraws consultation"). The guard must fire for that shape too, or
// the very first re-run of `remote init` on the only population that predates this ADR still
// silently un-pins it.
func TestADR016W1_ReRunWithoutPolicyFlagRefusesToDemoteALegacyPinnedMachine(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	// Seed the LEGACY shape directly (not through the CLI, which always writes an explicit
	// policy): a relay.json as it existed before relay_tls_policy was ever introduced.
	if err := relaycfg.Save(stateDir, relaycfg.Config{
		RelayURL: "wss://swarm-relay.example.com:8443",
		SPKIPin:  pin,
	}); err != nil {
		t.Fatalf("seed relaycfg.Save: %v", err)
	}

	var stdout, stderr bytes.Buffer
	args := []string{"--relay-url", "wss://swarm-relay.example.com:8443"}
	if exit := runRemoteInit(args, &stdout, &stderr); exit == 0 {
		t.Fatalf("re-running remote init with no --relay-tls-policy silently demoted a LEGACY "+
			"pinned machine (relay_spki_pin set, no relay_tls_policy field) to webpki "+
			"(stdout=%q); it must refuse, not rewrite", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--relay-tls-policy webpki") ||
		!strings.Contains(stderr.String(), "--relay-pin-compat") {
		t.Errorf("refusal %q does not name --relay-tls-policy webpki --relay-pin-compat as "+
			"the deliberate route", stderr.String())
	}
	cfg2, _, err := relaycfg.Load(stateDir)
	if err != nil {
		t.Fatalf("relaycfg.Load after the refused re-run: %v", err)
	}
	if cfg2.TLSPolicy != "" || cfg2.SPKIPin != pin {
		t.Fatalf("state after the refused re-run = policy %q pin %q; want the original legacy "+
			"shape (empty policy, pin %q) UNCHANGED", cfg2.TLSPolicy, cfg2.SPKIPin, pin)
	}
}

// TestADR016W1_BareReRunOnAPinnedMachineStillProvisionsTheIdentity guards against the defect
// the legacy-shape fix could introduce by running the disk-check guard unconditionally: a
// bare `swarm remote init` (no flags at all) never reaches relaycfg.Save (runRemoteInit only
// writes relay.json when --relay-url is non-empty), so there is nothing on THAT invocation
// that could demote a pin. It must stay the always-available identity/gateway repair path
// runRemoteInit's own comment names -- refusing it would leave an owner whose gateway is down
// with no supported way to start it, on every pinned_spki machine.
func TestADR016W1_BareReRunOnAPinnedMachineStillProvisionsTheIdentity(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	var stdout, stderr bytes.Buffer
	first := []string{"--relay-url", "wss://swarm-relay.example.com:8443", "--relay-pin", pin}
	if exit := runRemoteInit(first, &stdout, &stderr); exit != 0 {
		t.Fatalf("first remote init exit=%d stderr=%s", exit, stderr.String())
	}
	firstIdentity := strings.TrimSpace(stdout.String())

	stdout.Reset()
	stderr.Reset()
	if exit := runRemoteInit(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("bare `remote init` (no flags) on an already-pinned machine was refused: "+
			"exit=%d stderr=%s -- it writes nothing to relay.json and must remain available",
			exit, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != firstIdentity {
		t.Errorf("bare re-run printed identity %q, want the original %q unchanged", got, firstIdentity)
	}
	cfg, _, err := relaycfg.Load(stateDir)
	if err != nil {
		t.Fatalf("relaycfg.Load after the bare re-run: %v", err)
	}
	if cfg.TLSPolicy != relaycfg.PolicyPinnedSPKI || cfg.SPKIPin != pin {
		t.Fatalf("relay.json changed by a URL-less re-run: policy %q pin %q, want the original "+
			"pinned_spki/%q UNCHANGED", cfg.TLSPolicy, cfg.SPKIPin, pin)
	}
}

// TestADR016W1_ReRunWithCorruptRelayJSONRefusesRatherThanSilentlyOverwriting: the guard's
// disk read discarded relaycfg.Load's error, so a relay.json that EXISTS but fails to parse
// (a truncated write from an interrupted provisioning, a disk-full save) was treated the same
// as "nothing on disk" and silently overwritten with a fresh webpki config at exit 0.
// relaycfg.Load's own contract is the opposite: "a corrupt provisioning fails closed rather
// than silently reverting to unconfigured."
func TestADR016W1_ReRunWithCorruptRelayJSONRefusesRatherThanSilentlyOverwriting(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	remoteDir := filepath.Join(stateDir, relaycfg.Dir)
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	corrupt := []byte(`{"relay_url": `) // exists, does not parse
	relayJSON := filepath.Join(remoteDir, relaycfg.FileName)
	if err := os.WriteFile(relayJSON, corrupt, 0o600); err != nil {
		t.Fatalf("seed corrupt relay.json: %v", err)
	}

	var stdout, stderr bytes.Buffer
	args := []string{"--relay-url", "wss://swarm-relay.example.com:8443"}
	if exit := runRemoteInit(args, &stdout, &stderr); exit == 0 {
		t.Fatalf("remote init silently overwrote a CORRUPT relay.json (stdout=%q); "+
			"relaycfg.Load's parse error must fail the run closed, not be discarded", stdout.String())
	}
	after, err := os.ReadFile(relayJSON)
	if err != nil {
		t.Fatalf("ReadFile after the refused re-run: %v", err)
	}
	if string(after) != string(corrupt) {
		t.Errorf("corrupt relay.json was overwritten: got %q, want the original bytes untouched", after)
	}
}

// TestADR016W1_WebPKIRefusesAnIPLiteralHost is W6's pre-write refusal, tested here
// because it shares the same CLI validation pass as the rest of this file's cases and a
// GG-4 build must exercise it before any write reaches relay.json (a webpki policy that
// cannot succeed must not be written).
func TestADR016W1_WebPKIRefusesAnIPLiteralHost(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	var stdout, stderr bytes.Buffer
	args := []string{"--relay-url", "wss://203.0.113.7:8443", "--relay-tls-policy", "webpki"}
	if exit := runRemoteInit(args, &stdout, &stderr); exit == 0 {
		t.Fatalf("remote init accepted an IP-literal relay under webpki; W6 refuses it before any write")
	}
	if _, found, _ := relaycfg.Load(stateDir); found {
		t.Errorf("a rejected `remote init` wrote relay.json")
	}
}

// TestADR016W1_WebPKIWarnsButAdmitsAnObviouslyPrivateDNSSuffix is the webpki punch-list's
// MEDIUM B45-scope finding, the CLI half: unlike an IP literal, a name such as
// relay.local COULD in principle be served by webpki (an operator's own internal CA), so
// it is WARNED about rather than refused -- the warning names the residual ADR-016 W3's
// amendment records (a webpki machine on this suffix still reaches the pairing dial's
// unverified fallback), and the write still succeeds.
func TestADR016W1_WebPKIWarnsButAdmitsAnObviouslyPrivateDNSSuffix(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	var stdout, stderr bytes.Buffer
	args := []string{"--relay-url", "wss://swarm-relay.local:8443", "--relay-tls-policy", "webpki"}
	if exit := runRemoteInit(args, &stdout, &stderr); exit != 0 {
		t.Fatalf("remote init refused a private-suffix host under webpki (should warn, not "+
			"refuse): exit=%d stderr=%s", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "warning") || !strings.Contains(stderr.String(), "swarm-relay.local") {
		t.Errorf("stderr = %q, want a warning naming the private-suffix host", stderr.String())
	}
	cfg, found, err := relaycfg.Load(stateDir)
	if err != nil || !found {
		t.Fatalf("relaycfg.Load: found=%v err=%v", found, err)
	}
	if cfg.TLSPolicy != relaycfg.PolicyWebPKI {
		t.Errorf("TLSPolicy = %q, want %q (a warning must not block the write)", cfg.TLSPolicy, relaycfg.PolicyWebPKI)
	}
}

// TestADR016W1_ReRunWithoutPolicyFlagRefusesToDemoteAnUnrecognisedPinnedPolicy is the
// re-verification pass's BLOCKING finding: the guard above keyed on two exact-equality
// shapes (TLSPolicy == "pinned_spki", or the legacy TLSPolicy == "" with a pin), but
// relaycfg.Config.Security's own predicate is broader than either -- "if c.TLSPolicy !=
// PolicyWebPKI { sec.PinnedSPKISHA256 = pin }" pins the machine's REAL dials for ANY value
// that is not the literal "webpki", including an unrecognised or wrong-cased one (a
// hand-edited relay.json -- operator-runbook.md's own documented `printf` repair path --
// or an N/N-1 downgrade reading a newer build's policy string). The guard read that shape
// as unpinned and demoted it silently; it must fire on the same condition Security() acts
// on, not on a narrower enumeration of it.
func TestADR016W1_ReRunWithoutPolicyFlagRefusesToDemoteAnUnrecognisedPinnedPolicy(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	// A relay.json carrying an unrecognised policy string alongside a real pin -- neither
	// "pinned_spki" nor the legacy empty shape, but SPKIPin is set, so Security() pins it.
	if err := relaycfg.Save(stateDir, relaycfg.Config{
		RelayURL:  "wss://swarm-relay.example.com:8443",
		TLSPolicy: "Pinned_SPKI",
		SPKIPin:   pin,
	}); err != nil {
		t.Fatalf("seed relaycfg.Save: %v", err)
	}

	var stdout, stderr bytes.Buffer
	args := []string{"--relay-url", "wss://swarm-relay.example.com:8443"}
	if exit := runRemoteInit(args, &stdout, &stderr); exit == 0 {
		t.Fatalf("re-running remote init with no --relay-tls-policy silently demoted a "+
			"machine carrying an UNRECOGNISED pinned policy (stdout=%q); relaycfg.Config.Security "+
			"reads anything other than %q as pinned, so the guard must too", stdout.String(), relaycfg.PolicyWebPKI)
	}
	if !strings.Contains(stderr.String(), "--relay-tls-policy webpki") ||
		!strings.Contains(stderr.String(), "--relay-pin-compat") {
		t.Errorf("refusal %q does not name --relay-tls-policy webpki --relay-pin-compat as "+
			"the deliberate route", stderr.String())
	}
	cfg2, _, err := relaycfg.Load(stateDir)
	if err != nil {
		t.Fatalf("relaycfg.Load after the refused re-run: %v", err)
	}
	if cfg2.TLSPolicy != "Pinned_SPKI" || cfg2.SPKIPin != pin {
		t.Fatalf("state after the refused re-run = policy %q pin %q; want the original "+
			"unrecognised-policy shape UNCHANGED", cfg2.TLSPolicy, cfg2.SPKIPin)
	}
}

// TestADR016W1_ReRunWithoutPolicyFlagCarriesForwardTheExistingCompatibilityPin is the
// re-verification pass's W9-compat-pin MEDIUM: a flagless re-run against the SAME relay
// used to drop an already-published W9 compatibility pin by OMISSION rather than by the
// deliberate act ADR-016:179 step 6 defines (typing --relay-tls-policy webpki with no
// --relay-pin-compat). That is an availability break reached by typing nothing -- an
// un-migrated Android build then refuses every dial (B58) -- so the flagless re-run must
// carry the existing compatibility pin forward instead.
func TestADR016W1_ReRunWithoutPolicyFlagCarriesForwardTheExistingCompatibilityPin(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	url := "wss://swarm-relay.example.com:8443"
	var stdout, stderr bytes.Buffer
	first := []string{"--relay-url", url, "--relay-tls-policy", "webpki", "--relay-pin-compat", pin}
	if exit := runRemoteInit(first, &stdout, &stderr); exit != 0 {
		t.Fatalf("first remote init exit=%d stderr=%s", exit, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	second := []string{"--relay-url", url} // no --relay-tls-policy, no --relay-pin-compat
	if exit := runRemoteInit(second, &stdout, &stderr); exit != 0 {
		t.Fatalf("flagless re-run over an unchanged relay was refused: exit=%d stderr=%s",
			exit, stderr.String())
	}
	cfg, _, err := relaycfg.Load(stateDir)
	if err != nil {
		t.Fatalf("relaycfg.Load after the flagless re-run: %v", err)
	}
	if cfg.TLSPolicy != relaycfg.PolicyWebPKI {
		t.Errorf("TLSPolicy = %q, want %q unchanged", cfg.TLSPolicy, relaycfg.PolicyWebPKI)
	}
	if cfg.SPKIPin != pin {
		t.Errorf("SPKIPin = %q, want the compatibility pin %q carried forward, not dropped", cfg.SPKIPin, pin)
	}
}

// TestADR016W1_ExplicitWebPKIWithNoCompatPinWithdrawsAnExistingCompatibilityPin proves the
// carry-forward fix above did not also break W9 step 6's actual deliberate route: typing
// --relay-tls-policy webpki explicitly, with no --relay-pin-compat, still withdraws an
// existing compatibility pin -- carry-forward applies only to the OMITTED-flag path.
func TestADR016W1_ExplicitWebPKIWithNoCompatPinWithdrawsAnExistingCompatibilityPin(t *testing.T) {
	stateDir, restore := r2w1RemoteInitStateDir(t)
	defer restore()

	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	url := "wss://swarm-relay.example.com:8443"
	var stdout, stderr bytes.Buffer
	first := []string{"--relay-url", url, "--relay-tls-policy", "webpki", "--relay-pin-compat", pin}
	if exit := runRemoteInit(first, &stdout, &stderr); exit != 0 {
		t.Fatalf("first remote init exit=%d stderr=%s", exit, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	second := []string{"--relay-url", url, "--relay-tls-policy", "webpki"} // deliberate, no compat pin
	if exit := runRemoteInit(second, &stdout, &stderr); exit != 0 {
		t.Fatalf("explicit --relay-tls-policy webpki with no compat pin was refused: exit=%d stderr=%s",
			exit, stderr.String())
	}
	cfg, _, err := relaycfg.Load(stateDir)
	if err != nil {
		t.Fatalf("relaycfg.Load after the deliberate withdrawal: %v", err)
	}
	if cfg.SPKIPin != "" {
		t.Errorf("SPKIPin = %q, want empty: an EXPLICIT --relay-tls-policy webpki with no "+
			"--relay-pin-compat is the deliberate withdrawal ADR-016 W9 step 6 names", cfg.SPKIPin)
	}
}

// TestADR016W1_ReRunRefusesToDemoteAPolicyStoredWithoutAPin is the third-round finding:
// the guard's earlier predicate required a pin ALONGSIDE the policy, so a stored
// pinned_spki (or wrong-cased, or hand-edited) policy with no pin was rewritten to webpki
// by a flagless re-run -- and downstream, applyRelayTLSPolicy refuses a pinned-without-pin
// profile, so a migrated handset kept its pin exactly until the clean webpki profile from
// the demoted machine un-pinned it. W6 governs the PUBLISHED POLICY axis, pin or no pin.
func TestADR016W1_ReRunRefusesToDemoteAPolicyStoredWithoutAPin(t *testing.T) {
	for _, stored := range []string{"pinned_spki", "Pinned_SPKI", "anything-else"} {
		t.Run(stored, func(t *testing.T) {
			stateDir, restore := r2w1RemoteInitStateDir(t)
			defer restore()
			seed := []byte(`{"relay_url":"wss://a.example.com","relay_tls_policy":"` + stored + `"}`)
			dir := filepath.Join(stateDir, relaycfg.Dir)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, relaycfg.FileName), seed, 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			args := []string{"--relay-url", "wss://a.example.com"}
			if exit := runRemoteInit(args, &stdout, &stderr); exit == 0 {
				cfg, _, _ := relaycfg.Load(stateDir)
				t.Fatalf("flagless re-run over stored policy %q exited 0 and left policy %q; "+
					"a policy demotion must refuse whether or not a pin is present", stored, cfg.TLSPolicy)
			}
		})
	}
}
