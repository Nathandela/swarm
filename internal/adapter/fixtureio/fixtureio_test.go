package fixtureio

// E9.2 / E9.4 — LoadFixture reads a versioned fixture off disk and rejects a
// future schema or garbage, loudly. These tests moved here with the loader when
// disk I/O left the pure contract package: the boundary discipline (reject a
// FUTURE schema this build cannot faithfully read; reject non-JSON and missing
// files) is unchanged, only its home is now the harness-side package.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

// sampleFixture is a valid, fully populated in-memory fixture.
func sampleFixture() adapter.Fixture {
	return adapter.Fixture{
		SchemaVersion: adapter.FixtureSchemaVersion,
		CLI:           "reference-cli",
		Version:       "1.0.0",
		Scenario:      "idle-after-greeting",
		PTYCapture:    []byte("Welcome\r\nconv-id=abc123\r\n> \r\n"),
		HookPayloads: []adapter.HookPayload{
			{Event: "Stop", Raw: json.RawMessage(`{"reason":"done"}`), ReceivedAtMs: 1710000000000},
		},
	}
}

// TestFixtureRoundTrip — a fixture marshaled to JSON and reloaded via LoadFixture
// is byte-for-byte the same value, including the raw PTY capture and the hooks.
func TestFixtureRoundTrip(t *testing.T) {
	want := sampleFixture()
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "fx.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadFixture(path)
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// TestLoadFixture_Valid reads a real on-disk fixture from testdata (proving the
// loader parses the wire format, not just Go-marshaled bytes).
func TestLoadFixture_Valid(t *testing.T) {
	f, err := LoadFixture("testdata/fixtures/valid.json")
	if err != nil {
		t.Fatalf("LoadFixture(valid): %v", err)
	}
	if f.CLI == "" || f.Version == "" || f.Scenario == "" {
		t.Errorf("valid fixture has empty identity fields: %+v", f)
	}
	if len(f.PTYCapture) == 0 {
		t.Error("valid fixture decoded an empty PTYCapture")
	}
	if err := f.Validate(); err != nil {
		t.Errorf("valid fixture failed Validate: %v", err)
	}
}

// TestLoadFixture_RejectsFutureSchemaVersion — a fixture stamped newer than this
// build must be rejected loudly, never silently read (T-6 drift discipline).
func TestLoadFixture_RejectsFutureSchemaVersion(t *testing.T) {
	_, err := LoadFixture("testdata/fixtures/future_version.json")
	if err == nil {
		t.Fatal("LoadFixture accepted a future schema version")
	}
	if !strings.Contains(err.Error(), "newer") && !strings.Contains(err.Error(), "version") {
		t.Errorf("error %q should explain the version mismatch", err)
	}
}

// TestLoadFixture_RejectsGarbage — non-JSON and a nonexistent path are errors.
func TestLoadFixture_RejectsGarbage(t *testing.T) {
	if _, err := LoadFixture("testdata/fixtures/garbage.json"); err == nil {
		t.Error("LoadFixture accepted non-JSON garbage")
	}
	if _, err := LoadFixture("testdata/fixtures/does-not-exist.json"); err == nil {
		t.Error("LoadFixture accepted a missing file")
	}
}

// TestLoadFixture_RejectsZeroVersion — a schema version below the current one
// (here 0, the JSON zero value) is garbage this build will not read.
func TestLoadFixture_RejectsZeroVersion(t *testing.T) {
	if _, err := LoadFixture("testdata/fixtures/zero_version.json"); err == nil {
		t.Error("LoadFixture accepted schema version 0")
	}
}
