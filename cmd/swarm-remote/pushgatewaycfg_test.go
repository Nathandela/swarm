package main

// Tests for ADR-015 P9/P12 runtime configuration: only the atomic registry PushBinding
// supplies gateway authority; obsolete split files are ignored and untouched.

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func writePushGatewayFile(t *testing.T, stateDir string, data []byte) string {
	t.Helper()
	dir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "push-gateway.json"), data, 0o600); err != nil {
		t.Fatalf("write push-gateway.json: %v", err)
	}
	return filepath.Join(dir, "push-gateway.json")
}

func TestService_RedriveForegroundOnlyRetainsPendingObligation(t *testing.T) {
	requests := make(chan []byte, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
		if err != nil {
			t.Errorf("read wake: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"provider_accepted"}`)
	}))
	t.Cleanup(server.Close)
	// Trust only this test server's certificate; exercise the production HTTPS submitter.
	previousTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	rec := addPairedDevice(t, stateDir)
	capability := func(fill byte) string {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
	}
	rec.Push = &device.PushBinding{
		GatewayURL: server.URL, Address: bytes.Repeat([]byte{0x22}, 16),
		SubmitCapability: capability(0x31), MachineRevokeCapability: capability(0x32),
		WakeKey: bytes.Repeat([]byte{0x41}, 32), CapabilityRecordVersion: schema.CurrentCapabilityRecordVersion,
		Transport: device.PushTransportGateway,
	}
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.AddSole(rec); err != nil {
		t.Fatal(err)
	}
	bound, err := resolveGatewayParams(stateDir, "/tmp/remote.sock")
	if err != nil {
		t.Fatal(err)
	}
	machine := remotegw.NewWakeObligationMachine(remotegw.WakeObligationConfig{
		Store: bound.PushGateway.Obligations, WakeKey: bound.PushGateway.WakeKey,
		Address: bound.PushGateway.Address, Seq: bound.PushGateway.WakeSeq,
	})
	if err := machine.Trigger(); err != nil {
		t.Fatal(err)
	}
	if err := bound.PushPrefs.SavePrefs(remotegw.PushPrefs{Version: 1, NeedsInput: true, Finished: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "remote", "wake-obligations.json")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	binding := rec.Push
	rec.Push = nil
	if err := reg.AddSole(rec); err != nil {
		t.Fatal(err)
	}
	foreground, err := resolveGatewayParams(stateDir, "/tmp/remote.sock")
	if err != nil {
		t.Fatal(err)
	}
	if foreground.PushGateway != nil {
		t.Fatalf("foreground config has push gateway: %+v", foreground.PushGateway)
	}
	service := remotegw.NewService(serviceConfigFromParams(foreground, noopMailbox{}))
	if err := service.RedrivePendingWakeObligations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatal("foreground-only restart submitted a wake")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("foreground redrive changed pending obligation: bytes equal=%v err=%v", bytes.Equal(got, want), err)
	}
	store, err := remotegw.OpenObligationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after foreground restart = %d, err=%v", len(pending), err)
	}
	rec.Push = binding
	if err := reg.AddSole(rec); err != nil {
		t.Fatal(err)
	}
	restored, err := resolveGatewayParams(stateDir, "/tmp/remote.sock")
	if err != nil {
		t.Fatal(err)
	}
	restoredService := remotegw.NewService(serviceConfigFromParams(restored, noopMailbox{}))
	if err := restoredService.RedrivePendingWakeObligations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("restored binding submitted %d wakes, want 1", len(requests))
	}
	if body := <-requests; !bytes.Equal(body, pending[0].Envelope) {
		t.Fatal("restored binding did not retry the original sealed wake bytes")
	}
}

// A registry row without a push binding is foreground-only.
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

// Only a registry binding may supply push authority; a valid obsolete sidecar is inert.
func TestResolveGatewayParams_ValidObsoleteSidecarIsIgnored(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)

	sidecar := []byte(`{"gateway_url":"https://push.example.com","submit_capability":"old","push_address":"0102030405060708090a0b0c0d0e0f10"}`)
	path := writePushGatewayFile(t, stateDir, sidecar)

	p, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock")
	if err != nil {
		t.Fatalf("resolveGatewayParams: %v", err)
	}
	if p.PushGateway != nil {
		t.Fatalf("obsolete sidecar supplied gateway authority: %+v", p.PushGateway)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, sidecar) {
		t.Fatalf("obsolete sidecar was modified: bytes=%q err=%v", got, err)
	}
}

func TestResolveGatewayParams_CommittedRegistryPushLeavesLegacySidecarUntouched(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)
	sidecar := []byte(`{"gateway_url":"https://old-push.example.com","submit_capability":"old"}`)
	path := writePushGatewayFile(t, stateDir, sidecar)
	obsolete := map[string][]byte{
		"outbound-push.seq":   []byte("corrupt-old-seq"),
		"push-transport.json": []byte("corrupt-old-transport"),
	}
	for name, data := range obsolete {
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

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
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, sidecar) {
		t.Fatalf("obsolete sidecar was modified: bytes=%q err=%v", got, err)
	}
	for name, want := range obsolete {
		got, err := os.ReadFile(filepath.Join(filepath.Dir(path), name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("obsolete %s was modified: bytes=%q err=%v", name, got, err)
		}
	}
	first, err := params.PushGateway.WakeSeq.Next()
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := resolveGatewayParams(stateDir, "/tmp/remote.sock")
	if err != nil {
		t.Fatal(err)
	}
	next, err := restarted.PushGateway.WakeSeq.Next()
	if err != nil || next <= first {
		t.Fatalf("per-address wake seq after restart = (%d,%v), want > %d", next, err, first)
	}
}

// A corrupt obsolete sidecar cannot affect foreground-only startup.
func TestResolveGatewayParams_CorruptObsoleteSidecarIsIgnored(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)
	sidecar := []byte(`{not json`)
	path := writePushGatewayFile(t, stateDir, sidecar)

	params, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock")
	if err != nil || params.PushGateway != nil {
		t.Fatalf("corrupt obsolete sidecar affected foreground-only startup: params=%+v err=%v", params.PushGateway, err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, sidecar) {
		t.Fatalf("obsolete sidecar was modified: bytes=%q err=%v", got, err)
	}
}

// Obsolete sidecar fields are not parsed, including malformed addresses.
func TestResolveGatewayParams_ObsoleteSidecarBadAddressIsIgnored(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)
	writePushGatewayFile(t, stateDir, []byte(`{"push_address":"not-hex"}`))

	if p, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock"); err != nil || p.PushGateway != nil {
		t.Fatalf("obsolete sidecar affected foreground-only startup: params=%+v err=%v", p.PushGateway, err)
	}
}

// Obsolete sidecar URLs cannot select a transport.
func TestResolveGatewayParams_ObsoleteSidecarURLIsIgnored(t *testing.T) {
	for _, gatewayURL := range []string{"http://push.example.com", "not a url at all"} {
		stateDir := t.TempDir()
		writeMachineIdentity(t, stateDir)
		writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
		addPairedDevice(t, stateDir)
		writePushGatewayFile(t, stateDir, []byte(`{"gateway_url":"`+gatewayURL+`"}`))

		if p, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock"); err != nil || p.PushGateway != nil {
			t.Fatalf("obsolete sidecar %q affected foreground-only startup: params=%+v err=%v", gatewayURL, p.PushGateway, err)
		}
	}
}

// Obsolete sidecar path variants are ignored as data, not interpreted as endpoints.
func TestResolveGatewayParams_ObsoleteSidecarPathIsIgnored(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)
	writePushGatewayFile(t, stateDir, []byte(`{"gateway_url":"https://push.example.com/v1"}`))

	if p, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock"); err != nil || p.PushGateway != nil {
		t.Fatalf("obsolete sidecar affected foreground-only startup: params=%+v err=%v", p.PushGateway, err)
	}
}

func TestResolveGatewayParams_ObsoleteRegistryTransportFailsClosed(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	rec := addPairedDevice(t, stateDir)
	capability := func(fill byte) string {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
	}
	rec.Push = &device.PushBinding{
		GatewayURL: "https://push.example", Address: bytes.Repeat([]byte{0x22}, 16),
		SubmitCapability: capability(0x31), MachineRevokeCapability: capability(0x32),
		WakeKey: bytes.Repeat([]byte{0x41}, 32), CapabilityRecordVersion: schema.CurrentCapabilityRecordVersion,
		Transport: device.PushTransportGateway,
	}
	if err := device.ValidatePushBinding(*rec.Push); err != nil {
		t.Fatalf("control binding is invalid before transport mutation: %v", err)
	}
	rec.Push.Transport = "legacy_relay"
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.AddSole(rec); err == nil {
		t.Fatal("obsolete registry transport was persisted")
	}
}
