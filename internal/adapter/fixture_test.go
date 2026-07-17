package adapter

// E9.4 / T-6 — the fixture-corpus schema is DATA-ONLY and VERSIONED. A fixture
// records a real CLI's behavior ({cli, version, scenario, pty_capture,
// hook_payloads[]}) and is the adapter's acceptance baseline; on CLI drift it
// is re-recorded (T-6), so the loader must reject a fixture from a FUTURE schema
// (this build cannot faithfully read it) and reject garbage, loudly — the same
// discipline as persist's meta.json (persist.go decodeMeta).
//
// FROZEN SCHEMA (pinned) — snake_case JSON tags, matching the brief's
// {cli, version, scenario, pty_capture, hook_payloads[]} and persist's
// schema_version convention; testdata/fixtures/*.json use these keys:
//
//	const FixtureSchemaVersion = 1
//	type Fixture struct {
//	    SchemaVersion int           `json:"schema_version"`
//	    CLI           string        `json:"cli"`
//	    Version       string        `json:"version"`
//	    Scenario      string        `json:"scenario"`
//	    PTYCapture    []byte        `json:"pty_capture"`     // base64 on the wire
//	    HookPayloads  []HookPayload `json:"hook_payloads,omitempty"`
//	}
//	type HookPayload struct {
//	    Event        string          `json:"event"`
//	    Raw          json.RawMessage `json:"raw"`
//	    ReceivedAtMs int64           `json:"received_at_ms"`
//	}
//	func (f Fixture) Validate() error
//
// Validate rules (pinned): SchemaVersion == FixtureSchemaVersion; CLI, Version,
// Scenario non-empty; PTYCapture non-empty; every HookPayload has a non-empty
// Event and a syntactically valid JSON Raw body. The disk loader LoadFixture
// now lives in internal/adapter/fixtureio (E9.2 — the contract package opens no
// files); its tests moved there with it.

import (
	"encoding/json"
	"testing"
)

// sampleFixture is a valid, fully populated in-memory fixture.
func sampleFixture() Fixture {
	return Fixture{
		SchemaVersion: FixtureSchemaVersion,
		CLI:           "reference-cli",
		Version:       "1.0.0",
		Scenario:      "idle-after-greeting",
		PTYCapture:    []byte("Welcome\r\nconv-id=abc123\r\n> \r\n"),
		HookPayloads: []HookPayload{
			{Event: "Stop", Raw: json.RawMessage(`{"reason":"done"}`), ReceivedAtMs: 1710000000000},
		},
	}
}

// TestFixtureSchemaVersionConstant pins the current schema version.
func TestFixtureSchemaVersionConstant(t *testing.T) {
	if FixtureSchemaVersion != 1 {
		t.Fatalf("FixtureSchemaVersion = %d, want 1", FixtureSchemaVersion)
	}
}

// TestFixtureValidate exercises every Validate rule with a single-defect table.
func TestFixtureValidate(t *testing.T) {
	valid := sampleFixture()
	if err := valid.Validate(); err != nil {
		t.Fatalf("baseline fixture invalid: %v", err)
	}

	mut := func(f func(*Fixture)) Fixture {
		fx := sampleFixture()
		f(&fx)
		return fx
	}
	cases := []struct {
		name string
		fx   Fixture
	}{
		{"wrong-schema-version", mut(func(f *Fixture) { f.SchemaVersion = FixtureSchemaVersion + 1 })},
		{"zero-schema-version", mut(func(f *Fixture) { f.SchemaVersion = 0 })},
		{"empty-cli", mut(func(f *Fixture) { f.CLI = "" })},
		{"empty-version", mut(func(f *Fixture) { f.Version = "" })},
		{"empty-scenario", mut(func(f *Fixture) { f.Scenario = "" })},
		{"empty-capture", mut(func(f *Fixture) { f.PTYCapture = nil })},
		{"hook-empty-event", mut(func(f *Fixture) { f.HookPayloads = []HookPayload{{Event: "", Raw: json.RawMessage(`{}`)}} })},
		{"hook-invalid-json", mut(func(f *Fixture) { f.HookPayloads = []HookPayload{{Event: "Stop", Raw: json.RawMessage(`{not json`)}} })},
		{"hook-empty-raw", mut(func(f *Fixture) { f.HookPayloads = []HookPayload{{Event: "Stop", Raw: json.RawMessage(``)}} })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fx.Validate(); err == nil {
				t.Errorf("Validate accepted invalid fixture (%s)", tc.name)
			}
		})
	}
}

// TestFixtureValidate_EmptyHooksAllowed — a CLI with no hooks is legitimate; an
// empty HookPayloads slice must NOT be a validation error.
func TestFixtureValidate_EmptyHooksAllowed(t *testing.T) {
	fx := sampleFixture()
	fx.HookPayloads = nil
	if err := fx.Validate(); err != nil {
		t.Errorf("fixture with no hooks rejected: %v", err)
	}
}
