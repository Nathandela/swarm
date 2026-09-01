package device

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func pushTestCapability(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func TestRegistry_V1MigrationDropsUnversionedPushAuthorityAndWritesV2(t *testing.T) {
	dir := t.TempDir()
	rec := fullRecord(t, 0x31, CapFull, 4)
	rec.Push = &PushBinding{
		GatewayURL: "https://attacker.invalid", Address: bytes.Repeat([]byte{1}, 16),
		SubmitCapability: pushTestCapability(2), MachineRevokeCapability: pushTestCapability(3),
		WakeKey: bytes.Repeat([]byte{4}, 32), CapabilityRecordVersion: 1, Transport: PushTransportGateway,
	}
	data, err := json.Marshal(envelope{SchemaVersion: 1, Devices: []Record{rec}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, devicesFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reg.Get(rec.DeviceID)
	if !ok || got.Push != nil {
		t.Fatalf("v1 migration invented/trusted push authority: %+v", got.Push)
	}
	persisted, err := os.ReadFile(filepath.Join(dir, devicesFile))
	if err != nil {
		t.Fatal(err)
	}
	var migrated envelope
	if err := json.Unmarshal(persisted, &migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.SchemaVersion != 2 || migrated.Devices[0].Push != nil {
		t.Fatalf("migrated registry = %+v", migrated)
	}
}

func TestRegistry_PushBindingRoundTripsAsPartOfDeviceCommitAndIsDeepCopied(t *testing.T) {
	dir := t.TempDir()
	reg, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := fullRecord(t, 0x51, CapFull, 7)
	rec.Push = &PushBinding{
		GatewayURL:              "https://push-swarm.dsfactory.org",
		Address:                 bytes.Repeat([]byte{0x61}, 16),
		SubmitCapability:        pushTestCapability(0x63),
		MachineRevokeCapability: pushTestCapability(0x64),
		WakeKey:                 bytes.Repeat([]byte{0x62}, 32),
		CapabilityRecordVersion: 1,
		Transport:               PushTransportGateway,
	}
	if err := reg.AddSole(rec); err != nil {
		t.Fatal(err)
	}
	rec.Push.Address[0] = 0xff
	rec.Push.WakeKey[0] = 0xff

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(rec.DeviceID)
	if !ok || got.Push == nil {
		t.Fatal("persisted device lost push binding")
	}
	if got.Push.GatewayURL != "https://push-swarm.dsfactory.org" ||
		got.Push.Address[0] != 0x61 || got.Push.WakeKey[0] != 0x62 ||
		got.Push.Transport != PushTransportGateway {
		t.Fatalf("push binding round trip = %+v", got.Push)
	}
	got.Push.WakeKey[1] = 0xee
	again, _ := reopened.Get(rec.DeviceID)
	if again.Push.WakeKey[1] != 0x62 {
		t.Fatal("Get exposed registry-owned push wake key bytes")
	}
}

func TestRegistry_PushBindingValidationFailsClosed(t *testing.T) {
	valid := &PushBinding{
		GatewayURL:              "https://push-swarm.dsfactory.org",
		Address:                 bytes.Repeat([]byte{0x71}, 16),
		SubmitCapability:        pushTestCapability(0x73),
		MachineRevokeCapability: pushTestCapability(0x74),
		WakeKey:                 bytes.Repeat([]byte{0x72}, 32),
		CapabilityRecordVersion: 1,
		Transport:               PushTransportGateway,
	}
	tests := []struct {
		name string
		edit func(*PushBinding)
	}{
		{"cleartext gateway", func(p *PushBinding) { p.GatewayURL = "http://push.example.com" }},
		{"gateway path", func(p *PushBinding) { p.GatewayURL += "/v1" }},
		{"short address", func(p *PushBinding) { p.Address = p.Address[:15] }},
		{"short wake key", func(p *PushBinding) { p.WakeKey = p.WakeKey[:31] }},
		{"missing submit", func(p *PushBinding) { p.SubmitCapability = "" }},
		{"same capabilities", func(p *PushBinding) { p.MachineRevokeCapability = p.SubmitCapability }},
		{"unknown capability record", func(p *PushBinding) { p.CapabilityRecordVersion = 0 }},
		{"non-gateway transport", func(p *PushBinding) { p.Transport = "legacy_relay" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			rec := fullRecord(t, 0x73, CapFull, 7)
			cp := *valid
			cp.Address = append([]byte(nil), valid.Address...)
			cp.WakeKey = append([]byte(nil), valid.WakeKey...)
			tc.edit(&cp)
			rec.Push = &cp
			if err := reg.AddSole(rec); err == nil {
				t.Fatal("invalid push binding was admitted")
			}
			if reg.Count() != 0 {
				t.Fatal("invalid push binding changed registry")
			}
		})
	}
}
