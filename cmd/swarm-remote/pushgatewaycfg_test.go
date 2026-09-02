package main

// Tests for ADR-015 P9/P12 runtime configuration: legacy records may consume the optional
// push-gateway.json compatibility file, while negotiated records consume and supersede it
// from their atomic registry PushBinding.

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func writePushGatewayFile(t *testing.T, stateDir string, f pushGatewayFile) {
	t.Helper()
	dir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal push-gateway.json fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "push-gateway.json"), data, 0o600); err != nil {
		t.Fatalf("write push-gateway.json: %v", err)
	}
}

// TestResolveGatewayParams_NoPushGatewayFileLeavesPushGatewayNil pins the default: a
// pairing that has never migrated off legacy_relay (no push-gateway.json provisioned)
// gets a nil PushGateway, and NewService reads that as "wire the push path exactly as it
// is today" (internal/remotegw/service_test.go's TestNewService_NoPushGatewayLeaves...).
func TestResolveGatewayParams_NoPushGatewayFileLeavesPushGatewayNil(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)

	p, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock")
	if err != nil {
		t.Fatalf("resolveGatewayParams: %v", err)
	}
	if p.PushGateway != nil {
		t.Fatalf("PushGateway = %+v, want nil with no push-gateway.json provisioned", p.PushGateway)
	}
}

// TestResolveGatewayParams_PushGatewayFileWiresConfig is the positive half: a provisioned
// push-gateway.json resolves into a fully-populated remotegw.PushGatewayConfig, with a
// wake_seq source DISTINCT from PushSeq (see PushGatewayConfig.WakeSeq's doc comment for
// why sharing PushSeq's file would stale-drop the two wire objects against each other).
func TestResolveGatewayParams_PushGatewayFileWiresConfig(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)

	addr := make([]byte, 16)
	for i := range addr {
		addr[i] = byte(i + 1)
	}
	writePushGatewayFile(t, stateDir, pushGatewayFile{
		GatewayURL: "https://push.example.com", SubmitCapability: "test-submit-cap",
		PushAddress: hex.EncodeToString(addr),
	})

	p, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock")
	if err != nil {
		t.Fatalf("resolveGatewayParams: %v", err)
	}
	if p.PushGateway == nil {
		t.Fatal("PushGateway is nil despite a provisioned push-gateway.json")
	}
	if p.PushGateway.GatewayURL != "https://push.example.com" {
		t.Fatalf("GatewayURL = %q, want the provisioned URL", p.PushGateway.GatewayURL)
	}
	if p.PushGateway.SubmitCapability != "test-submit-cap" {
		t.Fatalf("SubmitCapability = %q, want the provisioned capability", p.PushGateway.SubmitCapability)
	}
	var wantAddr remotegw.PushAddress
	copy(wantAddr[:], addr)
	if p.PushGateway.Address != wantAddr {
		t.Fatalf("Address = %x, want %x", p.PushGateway.Address, wantAddr)
	}
	if p.PushGateway.Transport == nil || p.PushGateway.Obligations == nil || p.PushGateway.WakeSeq == nil {
		t.Fatalf("PushGateway has an unopened durable store: %+v", p.PushGateway)
	}
	if p.PushGateway.WakeSeq == p.PushSeq {
		t.Fatal("PushGateway.WakeSeq is the SAME SeqSource as PushSeq -- the legacy wake and " +
			"WakeV1 must not share a durable coordinate")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "remote", "outbound-wake.seq")); err == nil {
		t.Fatal("outbound-wake.seq exists before the first Next() call -- OpenSeqSource must not " +
			"write eagerly")
	}
	if _, err := p.PushGateway.WakeSeq.Next(); err != nil {
		t.Fatalf("WakeSeq.Next: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "remote", "outbound-wake.seq")); err != nil {
		t.Fatalf("outbound-wake.seq was not created by Next(): %v", err)
	}
}

func TestResolveGatewayParams_CommittedRegistryPushSupersedesAndRetiresLegacySidecar(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)
	remoteDir := filepath.Join(stateDir, "remote")
	writePushGatewayFile(t, stateDir, pushGatewayFile{
		GatewayURL: "https://old-push.example.com", SubmitCapability: "old-submit",
		MachineRevokeCapability: "old-revoke", PushAddress: hex.EncodeToString(bytes.Repeat([]byte{0x11}, 16)),
	})

	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	recs := reg.List()
	if len(recs) != 1 {
		t.Fatalf("registry count=%d", len(recs))
	}
	capability := func(fill byte) string {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
	}
	recs[0].Push = &device.PushBinding{
		GatewayURL: "https://push-swarm.dsfactory.org", Address: bytes.Repeat([]byte{0x22}, 16),
		SubmitCapability: capability(0x31), MachineRevokeCapability: capability(0x32),
		WakeKey: bytes.Repeat([]byte{0x41}, 32), CapabilityRecordVersion: schema.CurrentCapabilityRecordVersion,
		Transport: device.PushTransportGateway,
	}
	if err := reg.AddSole(recs[0]); err != nil {
		t.Fatal(err)
	}

	params, err := resolveGatewayParams(stateDir, "/tmp/remote.sock")
	if err != nil {
		t.Fatalf("committed registry authority was bricked by stale legacy sidecar: %v", err)
	}
	if params.PushGateway == nil || params.PushGateway.GatewayURL != recs[0].Push.GatewayURL ||
		params.PushGateway.SubmitCapability != recs[0].Push.SubmitCapability {
		t.Fatalf("runtime did not use committed registry authority: %+v", params.PushGateway)
	}
	if _, err := os.Stat(filepath.Join(remoteDir, "push-gateway.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy sidecar survived committed migration: %v", err)
	}
}

// TestResolveGatewayParams_PushGatewayFileMissingFieldFailsClosed pins that a present but
// incomplete push-gateway.json is a fail-closed provisioning error, not a silent partial
// PushGatewayConfig that would panic or misbehave downstream.
func TestResolveGatewayParams_PushGatewayFileMissingFieldFailsClosed(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)
	writePushGatewayFile(t, stateDir, pushGatewayFile{GatewayURL: "https://push.example.com"}) // no capability, no address

	if _, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock"); err == nil {
		t.Fatal("resolveGatewayParams with an incomplete push-gateway.json returned nil error, want a fail-closed refusal")
	}
}

// TestResolveGatewayParams_PushGatewayFileBadAddressFailsClosed pins the same fail-closed
// discipline for a push_address that is not exactly 16 hex-encoded bytes.
func TestResolveGatewayParams_PushGatewayFileBadAddressFailsClosed(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)
	writePushGatewayFile(t, stateDir, pushGatewayFile{
		GatewayURL: "https://push.example.com", SubmitCapability: "cap", PushAddress: "not-hex",
	})

	if _, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock"); err == nil {
		t.Fatal("resolveGatewayParams with a malformed push_address returned nil error, want a fail-closed refusal")
	}
}

// TestResolveGatewayParams_PushGatewayFileNonHTTPSURLFailsClosed pins that a non-https
// gateway_url is refused AT LOAD, not left to reach HTTPWakeSubmitter.SubmitWake's own
// PG-TR-1 check -- where the refusal is a plain (therefore unconditionally retryable)
// error, so an http:// or unparseable URL would otherwise become a push path that
// silently retries forever without ever delivering, rather than a startup refusal.
func TestResolveGatewayParams_PushGatewayFileNonHTTPSURLFailsClosed(t *testing.T) {
	for _, gatewayURL := range []string{"http://push.example.com", "not a url at all"} {
		stateDir := t.TempDir()
		writeMachineIdentity(t, stateDir)
		writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
		addPairedDevice(t, stateDir)
		writePushGatewayFile(t, stateDir, pushGatewayFile{
			GatewayURL: gatewayURL, SubmitCapability: "cap", PushAddress: "0102030405060708090a0b0c0d0e0f10",
		})

		if _, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock"); err == nil {
			t.Fatalf("resolveGatewayParams with gateway_url %q returned nil error, want a fail-closed refusal (PG-TR-1)", gatewayURL)
		}
	}
}

// TestResolveGatewayParams_PushGatewayFileURLWithPathFailsClosed pins the second half of
// the same fix: SubmitWake builds the request as TrimRight(BaseURL, "/") + "/v1/wakes", so
// an operator who already includes the spec's /v1 prefix in gateway_url would silently get
// /v1/v1/wakes with nothing failing anywhere. A gateway_url carrying a path, query or
// fragment is refused at load instead.
func TestResolveGatewayParams_PushGatewayFileURLWithPathFailsClosed(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)
	writePushGatewayFile(t, stateDir, pushGatewayFile{
		GatewayURL: "https://push.example.com/v1", SubmitCapability: "cap", PushAddress: "0102030405060708090a0b0c0d0e0f10",
	})

	if _, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock"); err == nil {
		t.Fatal("resolveGatewayParams with a gateway_url carrying a /v1 path returned nil error, want a fail-closed refusal")
	}
}
