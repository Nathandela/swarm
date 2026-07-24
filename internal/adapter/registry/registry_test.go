// Package registry is the Epic 11 adapter REGISTRY: the single table mapping a
// stable agent name ("agy" / "claude" / "codex" / "opencode" / "reference") to
// the constructor that builds that Adapter. It is the ONE place the daemon's
// DetectFunc (which greys the launch-form picker per L-2) and `swarm-char
// --adapter` resolve an adapter by name (T-5, T-7).
//
// R-F1 (.claude/tmp/cli-duo-implementation-plan.md Phase F) extends this
// originally v1.0 (claude/codex) suite to the v1.1 CLI duo: agy + opencode
// join as both constructors and production entries, FAILING-FIRST — this file
// pins the expanded surface before registry.go registers them, so the RED
// reason is "New(\"agy\"/\"opencode\") = (nil, false)" and a truncated
// Names() until they land.
package registry

import (
	"sort"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

// wantNames are the v1.1 registered adapters (T-7): the four real CLIs plus the
// fixture-only reference adapter that proves the boundary (E9.5).
var wantNames = []string{"agy", "claude", "codex", "opencode", "reference"}

// wantBinary is the real PATH binary each named adapter detects.
var wantBinary = map[string]string{
	"agy":       "agy",
	"claude":    "claude",
	"codex":     "codex",
	"opencode":  "opencode",
	"reference": "reference-cli",
}

// TestNew_KnownAdapters — each v1.1 name constructs a non-nil adapter whose
// Name and Binary match the registration; an unknown name returns (nil, false).
func TestNew_KnownAdapters(t *testing.T) {
	for _, name := range wantNames {
		a, ok := New(name)
		if !ok || a == nil {
			t.Errorf("New(%q) = (%v, %v); want a constructed adapter", name, a, ok)
			continue
		}
		if got := a.Binary(); got != wantBinary[name] {
			t.Errorf("New(%q).Binary() = %q; want %q", name, got, wantBinary[name])
		}
	}

	if a, ok := New("gemini"); ok || a != nil {
		t.Errorf("New(\"gemini\") = (%v, %v); want (nil, false) — unregistered (agy, not a bare \"gemini\" name, is the Gemini-CLI-successor adapter)", a, ok)
	}
}

// wantProduction is the T-7/GG-6 production set: every real CLI adapter, minus
// the fixture-only "reference" adapter.
var wantProduction = []string{"agy", "claude", "codex", "opencode"}

// TestIsProduction — R-F1: every real CLI adapter is a launchable production
// provider; "reference" and an unregistered name are not (the fail-closed
// default a launch RPC's scope gate relies on).
func TestIsProduction(t *testing.T) {
	for _, name := range wantProduction {
		if !IsProduction(name) {
			t.Errorf("IsProduction(%q) = false; want true (v1.1 real CLI)", name)
		}
	}
	if IsProduction("reference") {
		t.Errorf("IsProduction(\"reference\") = true; want false (fixture-only, not launchable)")
	}
	if IsProduction("gemini") {
		t.Errorf("IsProduction(\"gemini\") = true; want false (unregistered name)")
	}
}

// TestNames_SortedAndComplete — Names lists exactly the registered adapters,
// sorted, so a caller (DetectFunc, picker) enumerates them deterministically.
func TestNames_SortedAndComplete(t *testing.T) {
	got := Names()
	if !sort.StringsAreSorted(got) {
		t.Errorf("Names() = %v; want sorted", got)
	}
	set := map[string]bool{}
	for _, n := range got {
		set[n] = true
	}
	for _, want := range wantNames {
		if !set[want] {
			t.Errorf("Names() = %v; missing %q", got, want)
		}
	}
}

// TestRegisteredAdapters_Conformant — every registered adapter passes the frozen
// T-1 conformance suite. The registry cannot smuggle in a non-conforming adapter:
// this is the registry's tie to the anti-corruption boundary.
func TestRegisteredAdapters_Conformant(t *testing.T) {
	for _, name := range Names() {
		a, ok := New(name)
		if !ok {
			t.Fatalf("Names() listed %q but New(%q) failed", name, name)
		}
		if errs := adapter.CheckConformance(a); len(errs) != 0 {
			t.Errorf("registered adapter %q is not conformant: %v", name, errs)
		}
	}
}

// stubProber is an adapter.HostProber with canned results.
type stubProber struct {
	path string
	out  string
}

func (s stubProber) LookPath(string) (string, error)      { return s.path, nil }
func (s stubProber) Run(string, []string) (string, error) { return s.out, nil }

// TestRegistryDrivesDetection — the registry is the source a daemon DetectFunc
// consumes: enumerate Names(), construct each, and probe it through the CORE
// adapter.Detect. Here the claude adapter, built via the registry, detects its real
// version as InRange — the picker's usable/greyed signal (L-2). This proves the
// registry + contract compose into detection without the registry itself doing any
// I/O.
func TestRegistryDrivesDetection(t *testing.T) {
	a, ok := New("claude")
	if !ok {
		t.Fatal("New(\"claude\") failed")
	}
	det := adapter.Detect(a, stubProber{path: "/usr/local/bin/claude", out: "2.1.212 (Claude Code)"})
	if !det.Found || !det.InRange {
		t.Errorf("Detect(registry claude, real version) = %+v; want Found + InRange (picker offers it)", det)
	}
}
