package main

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 deliverable 4's machine-side revoke
// producer, config half (bead agents-tracker-u37c). The producer presents the
// MACHINE-REVOKE capability (push-gateway-api.md 2.2/3.4: distinct from submit,
// PG-AUTH-9), so the machine must HOLD it durably -- and today neither
// push-gateway.json nor remotegw.PushGatewayConfig has anywhere to put it: the pairing
// allocates {push_address, submit_capability, machine_revoke_capability} and the
// machine-side plumbing carries only the first two (config.go's pushGatewayFile).
//
// THE CONTRACT UNDER TEST (undefined today -- compile-level RED):
//
//   - pushGatewayFile.MachineRevokeCapability, JSON "machine_revoke_capability".
//   - remotegw.PushGatewayConfig.MachineRevokeCapability, carried verbatim.
//   - A file WITHOUT the field stays valid and resolves with the capability empty:
//     every already-provisioned push-gateway.json in the field predates the producer,
//     and refusing it would take down the working wake path to add a revoke path.
//     Empty means the producer cannot run and must say so -- degraded and disclosed,
//     never silently required.

import (
	"path/filepath"
	"testing"
)

// remoteDirOf mirrors resolveGatewayParams's own layout: push-gateway.json lives under
// <StateDir>/remote, which is what writePushGatewayFile provisions.
func remoteDirOf(stateDir string) string {
	return filepath.Join(stateDir, "remote")
}

func TestR4_PushGatewayConfig_CarriesTheMachineRevokeCapability(t *testing.T) {
	stateDir := t.TempDir()
	writePushGatewayFile(t, stateDir, pushGatewayFile{
		GatewayURL:              "https://push.example.com",
		SubmitCapability:        "cap-submit-000000000000000000000",
		MachineRevokeCapability: "cap-machine-revoke-00000000000000",
		PushAddress:             "4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e",
	})

	cfg, err := resolvePushGatewayConfig(remoteDirOf(stateDir))
	if err != nil {
		t.Fatalf("resolvePushGatewayConfig: %v", err)
	}
	if cfg == nil {
		t.Fatalf("resolvePushGatewayConfig returned nil for a present, valid file")
		return // unreachable; spelled out for staticcheck SA5011 (2026-09-01 lint drift)
	}
	if cfg.MachineRevokeCapability != "cap-machine-revoke-00000000000000" {
		t.Errorf("MachineRevokeCapability %q, want the file's value verbatim -- the producer "+
			"presents these exact bytes as Swarm-Revoke", cfg.MachineRevokeCapability)
	}
}

func TestR4_PushGatewayConfig_FileWithoutTheCapabilityStaysValid(t *testing.T) {
	stateDir := t.TempDir()
	writePushGatewayFile(t, stateDir, pushGatewayFile{
		GatewayURL:       "https://push.example.com",
		SubmitCapability: "cap-submit-000000000000000000000",
		PushAddress:      "4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e",
	})

	cfg, err := resolvePushGatewayConfig(remoteDirOf(stateDir))
	if err != nil {
		t.Fatalf("a pre-producer push-gateway.json was refused: %v -- the wake path must not "+
			"break to add the revoke path", err)
	}
	if cfg == nil {
		t.Fatalf("resolvePushGatewayConfig returned nil for a present, valid pre-producer file")
	}
	if cfg.MachineRevokeCapability != "" {
		t.Errorf("MachineRevokeCapability %q for a file that carries none; empty is the honest "+
			"answer, and it is the producer's job to disclose it cannot run", cfg.MachineRevokeCapability)
	}
}
