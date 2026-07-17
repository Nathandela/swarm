// Package fixtureio is the DISK side of the fixture corpus (E9.2 / E9.4). It
// holds the ONE function that reads a fixture off disk — LoadFixture — so the
// pure adapter contract package (internal/adapter) contains NO os.ReadFile/Open
// and stays genuinely I/O-free. The Fixture type, its Validate rules, and the
// FixtureSchemaVersion constant remain in internal/adapter (they are pure data +
// pure validation); only the file read lives here, on the harness side.
package fixtureio

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Nathandela/swarm/internal/adapter"
)

// LoadFixture reads and validates a fixture from path. It rejects a missing
// file, non-JSON garbage, a schema version newer than this build understands,
// and any fixture that fails adapter.Fixture.Validate — the same drift
// discipline as persist's meta.json. The version/validation semantics are
// identical to the original contract-package loader; only the file read moved.
func LoadFixture(path string) (adapter.Fixture, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return adapter.Fixture{}, fmt.Errorf("adapter: read fixture %s: %w", path, err)
	}
	var fx adapter.Fixture
	if err := json.Unmarshal(b, &fx); err != nil {
		return adapter.Fixture{}, fmt.Errorf("adapter: parse fixture %s: %w", path, err)
	}
	if fx.SchemaVersion > adapter.FixtureSchemaVersion {
		return adapter.Fixture{}, fmt.Errorf("adapter: fixture %s has a newer schema version %d (this build reads version %d)", path, fx.SchemaVersion, adapter.FixtureSchemaVersion)
	}
	if err := fx.Validate(); err != nil {
		return adapter.Fixture{}, fmt.Errorf("adapter: invalid fixture %s: %w", path, err)
	}
	return fx, nil
}
