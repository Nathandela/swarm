package adapter

// E9.1 / T-1 — the ONE Adapter interface, frozen. These tests pin the SHAPE of
// the anti-corruption boundary: the interface method set and the data types the
// launch form + engine consume. They are compile-anchored — if the frozen API
// drifts (a method signature or a struct field changes), this file stops
// compiling, which is the freeze doing its job.
//
// The behavioral freeze (what a conforming adapter must DO) lives in
// conformance_test.go; the reference adapter that proves T-5 lives in
// internal/adapter/refadapter.

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/vt"
)

// baseAdapter (stubs_test.go) must satisfy the frozen interface. A value
// receiver stub AND a pointer receiver stub both count as adapters — adapters
// are strategy objects that may be shared by value.
var (
	_ Adapter = baseAdapter{}
	_ Adapter = (*unstableName)(nil)
)

// TestAdapterInterfaceMethodSet exercises every method through the interface
// type only (never the concrete stub), proving the interface exposes exactly
// the frozen surface: detection, version range, argv composition, options
// schema, signal sources, resume, and conversation-id extraction.
func TestAdapterInterfaceMethodSet(t *testing.T) {
	var a Adapter = baseAdapter{}

	if a.Name() == "" {
		t.Error("Name() empty")
	}
	if a.Binary() == "" {
		t.Error("Binary() empty")
	}
	if a.VersionArgs() == nil {
		t.Error("VersionArgs() nil")
	}
	if _, ok := a.ParseVersion("stub-cli 1.2.0"); !ok {
		t.Error("ParseVersion() did not parse a valid version line")
	}
	_ = a.SupportedVersions()
	if _, err := a.Command(LaunchSpec{Cwd: "/w", Options: map[string]string{"model": "smart"}, InitialPrompt: "hi"}); err != nil {
		t.Errorf("Command() error: %v", err)
	}
	if len(a.Options()) == 0 {
		t.Error("Options() empty for the base stub")
	}
	if len(a.SignalSources()) == 0 {
		t.Error("SignalSources() empty for the base stub")
	}
	if _, err := a.Resume(ResumeSpec{Cwd: "/w", ConversationID: "abc"}); err != nil {
		t.Errorf("Resume() error: %v", err)
	}
	_, _ = a.ExtractConversationID(nil, []byte("conv-id=xyz"))
}

// TestFrozenTypeShape constructs each contract data type using EVERY field by
// name. A removed or renamed field breaks compilation — this is the type-level
// half of the freeze.
func TestFrozenTypeShape(t *testing.T) {
	_ = Detection{Found: true, Path: "/bin/x", Version: "1.0.0", InRange: true}
	_ = VersionConstraint{Min: "1.0.0", Max: "2.0.0"}
	_ = OptionSpec{Key: "k", Label: "L", Type: "choice", Default: "a", Choices: []string{"a", "b"}, Required: true}
	_ = SignalSource{Kind: "hook", Descriptor: map[string]string{"event": "Stop"}}
	_ = LaunchSpec{Cwd: "/w", Options: map[string]string{"k": "v"}, InitialPrompt: "p"}
	_ = ResumeSpec{Cwd: "/w", ConversationID: "id", Options: map[string]string{"k": "v"}}

	// ExtractConversationID's grid parameter is *vt.Snap — the emulator
	// projection, the ONLY core type the boundary shares (E9.5 boundary: the
	// adapter depends on the contract + vt, nothing else).
	var _ func(*vt.Snap, []byte) (string, bool) = baseAdapter{}.ExtractConversationID

	// The core Detect function takes an Adapter and a HostProber (the frozen
	// detection shape); a value receiver stub satisfies HostProber.
	var _ func(Adapter, HostProber) Detection = Detect
	var _ HostProber = fakeHostProber{}
}

// TestDetect_CoreDrivesDescriptorsThroughHostProber — Detect is now a CORE
// function: it owns the LookPath/exec through a HostProber and fills Detection
// from the adapter's pure descriptors. Driving a FAKE HostProber proves the
// three outcomes without touching PATH or exec: found-in-range, not-found, and
// found-out-of-range. baseAdapter supports [1.0.0, 2.0.0].
func TestDetect_CoreDrivesDescriptorsThroughHostProber(t *testing.T) {
	a := baseAdapter{}

	t.Run("found-in-range", func(t *testing.T) {
		got := Detect(a, fakeHostProber{path: "/opt/bin/stub-cli", runOut: "stub-cli 1.5.0"})
		if !got.Found || got.Path != "/opt/bin/stub-cli" {
			t.Errorf("Found/Path = %v/%q, want true//opt/bin/stub-cli", got.Found, got.Path)
		}
		if got.Version != "1.5.0" || !got.InRange {
			t.Errorf("Version/InRange = %q/%v, want 1.5.0/true", got.Version, got.InRange)
		}
	})

	t.Run("not-found", func(t *testing.T) {
		got := Detect(a, fakeHostProber{lookErr: errors.New("not on PATH")})
		if got.Found || got.Path != "" || got.Version != "" || got.InRange {
			t.Errorf("missing binary should yield the zero Detection, got %+v", got)
		}
	})

	t.Run("found-out-of-range", func(t *testing.T) {
		got := Detect(a, fakeHostProber{path: "/opt/bin/stub-cli", runOut: "stub-cli 3.0.0"})
		if !got.Found || got.Version != "3.0.0" {
			t.Errorf("Found/Version = %v/%q, want true/3.0.0", got.Found, got.Version)
		}
		if got.InRange {
			t.Error("InRange = true for 3.0.0 outside [1.0.0, 2.0.0]")
		}
	})

	t.Run("found-version-unparseable", func(t *testing.T) {
		got := Detect(a, fakeHostProber{path: "/opt/bin/stub-cli", runOut: "no version here"})
		if !got.Found || got.Version != "" || got.InRange {
			t.Errorf("unparseable version should be Found with empty Version, got %+v", got)
		}
	})
}
