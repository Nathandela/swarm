// Package registry is the Epic 11 adapter registry: the single table mapping a
// stable agent name ("agy" / "claude" / "codex" / "opencode" / "reference") to
// the constructor that builds that adapter.Adapter. It is the ONE place the
// daemon's DetectFunc (which greys the launch-form picker per L-2) and
// `swarm-char --adapter` resolve an adapter by name, so adding a v1.1 CLI is a
// single entry here (T-5, T-7).
package registry

import (
	"sort"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/agy"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/adapter/opencode"
	"github.com/Nathandela/swarm/internal/adapter/refadapter"
)

// constructors is the registry table: every registered name maps to its adapter
// builder. The reference adapter is fixture-driven but needs no fixture identity
// here — its empty-fixture fallback yields the "reference-cli" binary the picker
// probes (E9.5).
var constructors = map[string]func() adapter.Adapter{
	"agy":       agy.New,
	"claude":    claude.New,
	"codex":     codex.New,
	"opencode":  opencode.New,
	"reference": func() adapter.Adapter { return refadapter.New(adapter.Fixture{}) },
}

// production is the subset of registered adapters that are REAL providers a client
// may launch in production. The registry also holds the fixture-only "reference"
// adapter (constructed above for the E9.5 characterization harness and the
// launch-picker probe), but it is NOT a production provider: a launch RPC naming it
// must be refused at the launch boundary (GG-6 scope), even though it stays
// registered here. A new production adapter must be added to BOTH constructors and
// this set — the fail-closed default (absent ⇒ not launchable) is deliberate.
var production = map[string]bool{
	"agy":      true,
	"claude":   true,
	"codex":    true,
	"opencode": true,
}

// New constructs the adapter registered under name. ok is false for an unknown
// name (e.g. a v1.1 CLI not yet registered).
func New(name string) (adapter.Adapter, bool) {
	build, ok := constructors[name]
	if !ok {
		return nil, false
	}
	return build(), true
}

// IsProduction reports whether name is a registered REAL provider that may be
// launched in production (the GG-6 scope gate), as opposed to a fixture-only
// adapter such as "reference". Unknown names return false.
func IsProduction(name string) bool {
	return production[name]
}

// Names returns the registered adapter names, sorted, so a caller (DetectFunc, the
// launch picker) enumerates them deterministically.
func Names() []string {
	names := make([]string, 0, len(constructors))
	for name := range constructors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
