package skeleton

// ADR-016 W1 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): the machine-side
// PROPAGATION half. relaycfg.Config.TLSPolicy must reach pairing.MachinePayload through
// exactly the path RelaySPKIPin already takes (loadPairingConfig -> pairingConfig ->
// BeginPairing's Payload literal), and RelayHost -- "the hostname the machine itself
// dials" -- is DERIVED from the configured relay.json RelayURL, the same way PB-PAIR-7
// already carries RelayURL verbatim onto pairingConfig for the QR.
//
// pairingConfig gains RelayTLSPolicy and RelayHost fields; loadPairingConfig populates
// both from relaycfg.Config; BeginPairing's `Payload: pairing.MachinePayload{...}` literal
// (pairing.go) carries them through, unchanged in shape from how RelaySPKIPin does it
// today.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

// r2w1ProvisionMachine writes a machine identity + relay.json carrying a TLS policy, the
// shape `swarm remote init --relay-url --relay-tls-policy` leaves behind.
func r2w1ProvisionMachine(t *testing.T, relayURL, policy, pin string) string {
	t.Helper()
	stateDir := t.TempDir()
	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	id, err := machineid.Generate("w1.local")
	if err != nil {
		t.Fatalf("machineid.Generate: %v", err)
	}
	if err := id.Save(filepath.Join(remoteDir, remoteIdentityFile)); err != nil {
		t.Fatalf("save identity: %v", err)
	}
	if err := relaycfg.Save(stateDir, relaycfg.Config{RelayURL: relayURL, OperatorNamespace: "owner", TLSPolicy: policy, SPKIPin: pin}); err != nil {
		t.Fatalf("relaycfg.Save: %v", err)
	}
	return stateDir
}

// TestADR016W1_LoadPairingConfigCarriesTheTLSPolicy pins the propagation from relay.json
// into pairingConfig: the SAME value, and a RelayHost derived from the relay URL's own
// hostname -- never from a separate config field, since W1 names only relay_tls_policy and
// relay_host as new payload fields, and the host is already implicit in RelayURL.
func TestADR016W1_LoadPairingConfigCarriesTheTLSPolicy(t *testing.T) {
	stateDir := r2w1ProvisionMachine(t, "wss://swarm-relay.example.com:8443", "webpki", "")

	cfg, err := loadPairingConfig(stateDir)
	if err != nil {
		t.Fatalf("loadPairingConfig: %v", err)
	}
	if cfg == nil {
		t.Fatalf("loadPairingConfig returned nil for a provisioned machine")
	}
	if cfg.RelayTLSPolicy != "webpki" {
		t.Errorf("pairingConfig.RelayTLSPolicy = %q, want %q", cfg.RelayTLSPolicy, "webpki")
	}
	if cfg.RelayHost != "swarm-relay.example.com" {
		t.Errorf("pairingConfig.RelayHost = %q, want %q (the relay URL's hostname)", cfg.RelayHost, "swarm-relay.example.com")
	}
}

// TestADR016W1_LoadPairingConfigPolicyIndependentOfPin: a pinned_spki machine WITH a pin
// carries both, independently -- the same mutation-control case relaycfg's own test pins,
// exercised through the full daemon-assembly read path.
func TestADR016W1_LoadPairingConfigPolicyIndependentOfPin(t *testing.T) {
	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	stateDir := r2w1ProvisionMachine(t, "wss://swarm-relay.example.com:8443", "pinned_spki", pin)

	cfg, err := loadPairingConfig(stateDir)
	if err != nil {
		t.Fatalf("loadPairingConfig: %v", err)
	}
	if cfg.RelayTLSPolicy != "pinned_spki" {
		t.Errorf("RelayTLSPolicy = %q, want %q", cfg.RelayTLSPolicy, "pinned_spki")
	}
	if len(cfg.RelaySPKIPin) == 0 {
		t.Errorf("RelaySPKIPin is empty; the pin must still flow independently of the policy field")
	}
}

// TestADR016W1_LoadPairingConfigNoRelayLeavesPolicyEmpty: an unprovisioned relay (no
// relay.json) must not manufacture a default policy at the DAEMON layer -- W1 gives the
// default to the CLI ("Omitted, it is webpki"), not to every reader of an absent file.
func TestADR016W1_LoadPairingConfigNoRelayLeavesPolicyEmpty(t *testing.T) {
	stateDir := t.TempDir()
	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	id, err := machineid.Generate("w1.local")
	if err != nil {
		t.Fatalf("machineid.Generate: %v", err)
	}
	if err := id.Save(filepath.Join(remoteDir, remoteIdentityFile)); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	cfg, err := loadPairingConfig(stateDir)
	if err != nil {
		t.Fatalf("loadPairingConfig: %v", err)
	}
	if cfg.RelayTLSPolicy != "" {
		t.Errorf("RelayTLSPolicy = %q for an unprovisioned relay, want empty", cfg.RelayTLSPolicy)
	}
}
