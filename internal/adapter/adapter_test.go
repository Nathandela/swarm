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
	if _, err := a.Detect(); err != nil {
		t.Errorf("Detect() error: %v", err)
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
}
