package main

// ADR-016 W1 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): the `swarm remote
// init` CLI half of the policy/pin independence ruling.
//
//	--relay-tls-policy {webpki|pinned_spki} selects the policy, and nothing else.
//	  Omitted, it is webpki.
//	--relay-pin keeps its EXACT current meaning: mandatory under pinned_spki, refused
//	  under webpki.
//	--relay-pin-compat supplies the W9 compatibility pin, legal ONLY under webpki.
//	One legacy inference survives, over the FLAG only, never over stored state:
//	  --relay-pin with no --relay-tls-policy selects pinned_spki.
//
// Every refusal happens BEFORE any filesystem write (validateRelayPin's existing
// contract), so a rejected run provisions nothing.

import (
	"bytes"
	"os"
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
