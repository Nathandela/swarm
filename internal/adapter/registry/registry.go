// Package registry is the Epic 11 adapter registry: the single table mapping a
// stable agent name ("claude" / "codex" / "reference") to the constructor that
// builds that adapter.Adapter. It is the ONE place the daemon's DetectFunc (which
// greys the launch-form picker per L-2) and `swarm-char --adapter` resolve an
// adapter by name, so adding a v1.1 CLI is a single entry here (T-5, T-7).
package registry

import (
	"sort"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/adapter/refadapter"
)

// constructors is the registry table: every registered name maps to its adapter
// builder. The reference adapter is fixture-driven but needs no fixture identity
// here — its empty-fixture fallback yields the "reference-cli" binary the picker
// probes (E9.5).
var constructors = map[string]func() adapter.Adapter{
	"claude":    claude.New,
	"codex":     codex.New,
	"reference": func() adapter.Adapter { return refadapter.New(adapter.Fixture{}) },
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
