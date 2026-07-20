package device

// Hardening regression tests from the R-DEV.1 fable review. Each pins a fail-closed
// admission/load guard for the registry, the daemon's remote-authorization authority:
//   - a future/unstamped on-disk schema version is rejected, not silently loaded
//     (review DEFECT 1);
//   - a record whose DeviceID is not the canonical DeviceIDFor(CommandSignPub) is
//     rejected, so the id is self-authenticating and cannot shadow another device
//     (review RISK 2);
//   - the three 32-byte identity keys (Noise-static, relay-auth, recipient) are
//     length-checked at admission, like the command-signing key (review RISK 3);
//   - a zero/unknown Capability is rejected by Add (review RISK 4);
//   - a corrupt or hostile devices.json fails Open loudly (review RISK 5).
// These fail to compile/pass until the guards are implemented; no existing test is
// edited to go green.

import (
	"os"
	"path/filepath"
	"testing"
)

// writeDevicesFile writes raw bytes as the registry's devices.json in dir.
func writeDevicesFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, devicesFile), []byte(content), 0o600); err != nil {
		t.Fatalf("write devices.json: %v", err)
	}
}

// TestRegistry_OpenRejectsFutureSchema pins that a devices.json stamped with a
// schema version this build does not understand (or an unstamped version 0) fails
// Open, rather than loading records whose semantics the writer may have changed
// (review DEFECT 1 — mirrors internal/persist's future-version refusal).
func TestRegistry_OpenRejectsFutureSchema(t *testing.T) {
	valid := `{"device_id":"x","name":"n","command_sign_pub":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","capability":"full"}`
	for _, sv := range []string{"99", "0"} {
		t.Run("schema_"+sv, func(t *testing.T) {
			dir := t.TempDir()
			writeDevicesFile(t, dir, `{"schema_version":`+sv+`,"devices":[`+valid+`]}`)
			if _, err := Open(dir); err == nil {
				t.Fatalf("Open with schema_version %s = nil error; want rejection (fail-closed)", sv)
			}
		})
	}
}

// TestRegistry_AddRejectsMismatchedDeviceID pins that Add refuses a record whose
// DeviceID is not DeviceIDFor(CommandSignPub): the id must be self-authenticating so
// a device cannot be registered (or shadow another) under an id unrelated to the key
// R-POL.9 verifies against (review RISK 2).
func TestRegistry_AddRejectsMismatchedDeviceID(t *testing.T) {
	reg, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	rec := fullRecord(t, 0x66, CapFull, 1)
	rec.DeviceID = "not-the-derived-id"
	if err := reg.Add(rec); err == nil {
		t.Fatalf("Add with mismatched DeviceID = nil error; want rejection")
	}
	if reg.Count() != 0 {
		t.Fatalf("Count() = %d after rejected Add, want 0", reg.Count())
	}
}

// TestRegistry_AddRejectsNon32Keys pins that each of the three 32-byte identity keys
// is length-validated at admission (review RISK 3). A nil RecipientPub, for instance,
// would leave EpochGrant sealing (R-CRY.10) with no target key.
func TestRegistry_AddRejectsNon32Keys(t *testing.T) {
	reg, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	for _, field := range []string{"noise", "relay", "recipient"} {
		t.Run(field, func(t *testing.T) {
			rec := fullRecord(t, 0x77, CapFull, 1)
			switch field {
			case "noise":
				rec.NoiseStaticPub = make([]byte, 31)
			case "relay":
				rec.RelayAuthPub = make([]byte, 33)
			case "recipient":
				rec.RecipientPub = nil
			}
			if err := reg.Add(rec); err == nil {
				t.Fatalf("Add with bad %s key = nil error; want rejection", field)
			}
			if reg.Count() != 0 {
				t.Fatalf("Count() = %d after rejected Add, want 0", reg.Count())
			}
		})
	}
}

// TestRegistry_AddRejectsInvalidCapability pins that Add refuses a zero-value or
// unknown-high Capability (review RISK 4) — a corrupted tier must never be admitted.
func TestRegistry_AddRejectsInvalidCapability(t *testing.T) {
	reg, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	for _, c := range []Capability{Capability(0), Capability(0xFF)} {
		rec := fullRecord(t, 0x88, c, 1)
		if err := reg.Add(rec); err == nil {
			t.Fatalf("Add with capability %d = nil error; want rejection", uint8(c))
		}
	}
	if reg.Count() != 0 {
		t.Fatalf("Count() = %d after rejected Adds, want 0", reg.Count())
	}
}

// TestRegistry_OpenRejectsCorruptFile pins that a truncated file, an unknown
// capability string, and a wrong-length persisted key each fail Open loudly rather
// than admitting a mis-decoded or malformed record (review RISK 5).
func TestRegistry_OpenRejectsCorruptFile(t *testing.T) {
	full32 := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" // 32 zero bytes, base64
	cases := map[string]string{
		"truncated_json":    `{"schema_version":1,"devices":[{`,
		"unknown_capability": `{"schema_version":1,"devices":[{"device_id":"x","command_sign_pub":"` + full32 + `","capability":"superuser"}]}`,
		"short_key":         `{"schema_version":1,"devices":[{"device_id":"x","command_sign_pub":"AAAA","capability":"full"}]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeDevicesFile(t, dir, content)
			if _, err := Open(dir); err == nil {
				t.Fatalf("Open of %s devices.json = nil error; want rejection", name)
			}
		})
	}
}
