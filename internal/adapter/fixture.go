package adapter

// Fixture is the versioned, data-only characterization record (E9.4 / T-6): a
// snapshot of one real CLI's behavior — its identity, the raw PTY bytes it
// produced in a named scenario, and any hook callbacks it emitted. It is the
// adapter's acceptance baseline; on CLI drift it is re-recorded.
//
// This file is PURE: the Fixture type, its Validate rules, and the schema
// constant only. Reading a fixture off disk (LoadFixture) lives in the
// harness-side package internal/adapter/fixtureio so the contract package opens
// no files (E9.2 — no file-read syscalls here). The loader there rejects a
// fixture from a FUTURE schema (this build cannot faithfully read it) and
// rejects garbage, loudly — the same discipline as persist's meta.json.

import (
	"encoding/json"
	"fmt"
)

// FixtureSchemaVersion is the current fixture schema version. Bump it whenever
// the Fixture/HookPayload wire format changes; fixtureio.LoadFixture rejects any
// newer version and Validate rejects any other version.
const FixtureSchemaVersion = 1

// Fixture is one recorded characterization scenario.
type Fixture struct {
	SchemaVersion int           `json:"schema_version"`
	CLI           string        `json:"cli"`
	Version       string        `json:"version"`
	Scenario      string        `json:"scenario"`
	PTYCapture    []byte        `json:"pty_capture"` // base64 on the wire
	HookPayloads  []HookPayload `json:"hook_payloads,omitempty"`
}

// HookPayload is one hook callback the CLI emitted during characterization.
type HookPayload struct {
	Event        string          `json:"event"`
	Raw          json.RawMessage `json:"raw"`
	ReceivedAtMs int64           `json:"received_at_ms"`
}

// Validate reports whether the fixture is well-formed: an exact schema-version
// match, non-empty identity fields, a non-empty PTY capture, and — for every
// hook — a non-empty event and a syntactically valid JSON raw body. A CLI with
// no hooks (empty HookPayloads) is legitimate.
func (f Fixture) Validate() error {
	if f.SchemaVersion != FixtureSchemaVersion {
		return fmt.Errorf("fixture schema version %d != %d", f.SchemaVersion, FixtureSchemaVersion)
	}
	if f.CLI == "" {
		return fmt.Errorf("fixture cli is empty")
	}
	if f.Version == "" {
		return fmt.Errorf("fixture version is empty")
	}
	if f.Scenario == "" {
		return fmt.Errorf("fixture scenario is empty")
	}
	if len(f.PTYCapture) == 0 {
		return fmt.Errorf("fixture pty_capture is empty")
	}
	for i, hp := range f.HookPayloads {
		if hp.Event == "" {
			return fmt.Errorf("fixture hook_payloads[%d] has an empty event", i)
		}
		if !json.Valid(hp.Raw) {
			return fmt.Errorf("fixture hook_payloads[%d] raw is not valid JSON", i)
		}
	}
	return nil
}
