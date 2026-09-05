// ADR-007 B34, machine half (FAILING FIRST): <stateDir>/remote/relay.json gains an
// optional SPKI pin, and ONE parser owns the file.
//
// Three separate readers used to parse this file -- cmd/swarm's readRelayURL,
// cmd/swarm-remote's loadRelayURL and internal/skeleton's loadRelayURL -- each with its
// own anonymous struct and its own copy of the JSON key. Two of them said so in comments
// ("the CLI writer and this reader must agree on this filename + shape"). Adding a field
// to two of three would leave a machine that reads as pinned on one dial path and is not
// on another, which is worse than no pin at all: it reads as covered.
package relaycfg_test

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

// samplePin is a well-formed pin: base64 of a 32-byte SHA-256, the exact form
// docs/operations/relay-runbook.md section 3 tells the operator to produce with
// `openssl dgst -sha256 -binary | openssl base64`.
func samplePin() (raw []byte, b64 string) {
	sum := sha256.Sum256([]byte("a relay public key"))
	return sum[:], base64.StdEncoding.EncodeToString(sum[:])
}

func TestRelayCfg_AbsentFileIsNotAnError(t *testing.T) {
	cfg, found, err := relaycfg.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load on an unprovisioned state dir: %v", err)
	}
	if found {
		t.Fatal("Load reported a config where no relay.json exists")
	}
	if cfg.RelayURL != "" || cfg.SPKIPin != "" {
		t.Fatalf("Load returned %+v for an absent file, want the zero value", cfg)
	}
}

func TestRelayCfg_RoundTripsThePin(t *testing.T) {
	_, b64 := samplePin()
	dir := t.TempDir()
	want := relaycfg.Config{RelayURL: "wss://relay.example.com:8443", OperatorNamespace: "owner", SPKIPin: b64}
	if err := relaycfg.Save(dir, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, found, err := relaycfg.Load(dir)
	if err != nil || !found {
		t.Fatalf("Load after Save: %+v found=%v err=%v", got, found, err)
	}
	if got != want {
		t.Fatalf("round trip: got %+v, want %+v", got, want)
	}

	// The on-disk KEY NAMES are the contract with the operator's editor and with the
	// runbook, so they are asserted literally rather than via the round trip.
	raw, err := os.ReadFile(filepath.Join(dir, "remote", "relay.json"))
	if err != nil {
		t.Fatalf("read relay.json: %v", err)
	}
	for _, key := range []string{`"relay_url"`, `"operator_namespace"`, `"relay_spki_pin"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("relay.json does not carry %s; on disk it is:\n%s", key, raw)
		}
	}
}

// TestRelayCfg_NoPinIsOmittedFromTheFile keeps an unpinned machine's file exactly what it
// is today, so a machine provisioned before this field existed is not rewritten into a
// shape that reads as "pinned with an empty pin".
func TestRelayCfg_NoPinIsOmittedFromTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := relaycfg.Save(dir, relaycfg.Config{RelayURL: "wss://relay.example.com", OperatorNamespace: "owner"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "remote", "relay.json"))
	if err != nil {
		t.Fatalf("read relay.json: %v", err)
	}
	if strings.Contains(string(raw), "relay_spki_pin") {
		t.Fatalf("an unpinned machine's relay.json mentions the pin key:\n%s", raw)
	}
}

// TestRelayCfg_SecurityCarriesTheConfiguredPin is the whole point of the field: the
// policy the machine dials under must actually contain the operator's pin.
func TestRelayCfg_SecurityCarriesTheConfiguredPin(t *testing.T) {
	raw, b64 := samplePin()
	sec, err := relaycfg.Config{RelayURL: "wss://relay.example.com", SPKIPin: b64}.Security()
	if err != nil {
		t.Fatalf("Security: %v", err)
	}
	if string(sec.PinnedSPKISHA256) != string(raw) {
		t.Fatalf("Security().PinnedSPKISHA256 = %x, want %x", sec.PinnedSPKISHA256, raw)
	}
}

// TestRelayCfg_SecurityWithoutAPinIsTheMachinePolicy pins the "optional in the file"
// half: a machine with no pin keeps working exactly as it does today, loopback carve-out
// included, which is what local development and the S19 exit demonstration depend on.
func TestRelayCfg_SecurityWithoutAPinIsTheMachinePolicy(t *testing.T) {
	sec, err := relaycfg.Config{RelayURL: "ws://127.0.0.1:9440"}.Security()
	if err != nil {
		t.Fatalf("Security with no pin: %v", err)
	}
	if len(sec.PinnedSPKISHA256) != 0 || len(sec.PinnedCert) != 0 {
		t.Fatalf("an unpinned config produced a pinned policy: %+v", sec)
	}
	if _, err := relay.DialRawSecure(t.Context(), "ws://127.0.0.1:1/", sec); errors.Is(err, relay.ErrCleartextRefused) {
		t.Fatalf("an unpinned machine config refused a loopback relay: %v", err)
	}
}

// TestRelayCfg_MalformedPinIsRefused covers every way the operator can get the value
// wrong. Each must be ErrPinMalformed and must be reported when the config is READ --
// before any dial -- for B33's reason: a truncated pin that only ever surfaced as a
// handshake failure would read as "the relay is down", and one silently zero-padded to
// length would weaken the check.
func TestRelayCfg_MalformedPinIsRefused(t *testing.T) {
	raw, _ := samplePin()
	for name, pin := range map[string]string{
		"not base64":     "this is not base64 at all!!",
		"too short":      base64.StdEncoding.EncodeToString(raw[:16]),
		"too long":       base64.StdEncoding.EncodeToString(append(append([]byte(nil), raw...), 0x00)),
		"empty-ish":      "   ",
		"hex not base64": "6e5f8a2c",
	} {
		_, err := relaycfg.Config{RelayURL: "wss://relay.example.com", SPKIPin: pin}.Security()
		if !errors.Is(err, relay.ErrPinMalformed) {
			t.Errorf("%s (%q): got %v, want relay.ErrPinMalformed", name, pin, err)
		}
	}
}

// TestRelayCfg_PinnedConfigAcceptsTheRunbooksExactOutput closes the gap between the
// documented command and the parser. `openssl base64` emits standard base64 WITH padding
// and a trailing newline, and an operator pasting it into JSON may leave the whitespace.
func TestRelayCfg_PinnedConfigAcceptsTheRunbooksExactOutput(t *testing.T) {
	raw, b64 := samplePin()
	sec, err := relaycfg.Config{RelayURL: "wss://relay.example.com", SPKIPin: b64 + "\n"}.Security()
	if err != nil {
		t.Fatalf("Security with the runbook's trailing newline: %v", err)
	}
	if string(sec.PinnedSPKISHA256) != string(raw) {
		t.Fatalf("pin mismatch after trimming whitespace")
	}
}

// TestRelayCfg_APinnedMachineRefusesCleartext is the "mandatory in effect once set" half.
// An operator who configured a pin has stated they want a verified peer; a ws:// URL
// cannot carry one, so the dial fails closed instead of silently running unpinned. The
// loopback carve-out does NOT rescue it -- that carve-out exists for a machine with no
// pin to apply.
func TestRelayCfg_APinnedMachineRefusesCleartext(t *testing.T) {
	_, b64 := samplePin()
	sec, err := relaycfg.Config{RelayURL: "ws://127.0.0.1:9440", SPKIPin: b64}.Security()
	if err != nil {
		t.Fatalf("Security: %v", err)
	}
	if _, err := relay.DialRawSecure(t.Context(), "ws://127.0.0.1:1/", sec); !errors.Is(err, relay.ErrCleartextRefused) {
		t.Fatalf("a pinned machine dialed cleartext: got %v, want ErrCleartextRefused", err)
	}
}
