// Package adapter is the FROZEN anti-corruption boundary (Epic 9, T-1/T-5/T-6)
// between swarm's core (daemon/shim/wire/TUI) and the concrete agent CLIs it
// drives. An Adapter is a stateless, goroutine-safe STRATEGY OBJECT: it answers
// questions about a CLI (how to detect it, which versions it supports, how to
// compose its launch/resume argv, which launch options and signal sources it
// exposes, how to read a conversation id out of its output) but owns NO
// lifecycle — no process, no fd, no socket, no disk. Core owns all of that.
//
// The boundary depends only on this package plus internal/vt (the Snap
// projection ExtractConversationID reads); adding a real adapter later touches
// only that adapter's own package. This file pins the interface method set and
// every data type; conformance.go pins the behavior a conforming adapter must
// exhibit; fixture.go/capability.go pin the characterization corpus and its
// derived capability matrix.
package adapter

import "github.com/Nathandela/swarm/internal/vt"

// Adapter is the ONE interface every agent-CLI integration implements. Every
// method is pure and goroutine-safe: given the same inputs it returns the same
// outputs, holds no mutable state, and may be shared by value across goroutines.
type Adapter interface {
	// Name is the stable, non-empty identifier of the CLI this adapter drives
	// (e.g. "claude-code"). It never changes across calls.
	Name() string

	// Detect reports whether the CLI is installed, where, at what version, and
	// whether that version is within SupportedVersions.
	Detect() (Detection, error)

	// SupportedVersions is the inclusive version range this adapter is known to
	// drive correctly.
	SupportedVersions() VersionConstraint

	// Command composes the exec argv for a fresh launch. argv[0] is a real
	// program path — never a shell, never a single metacharacter-bearing string.
	// It is pure: the same spec yields the same argv, and it opens nothing.
	Command(spec LaunchSpec) ([]string, error)

	// Options is the declarative launch-option schema the launch form renders.
	Options() []OptionSpec

	// SignalSources declares how this CLI's idle/active signal is observed
	// (hooks, an event stream, or grid heuristics).
	SignalSources() []SignalSource

	// Resume composes the exec argv to resume an existing conversation. With no
	// ConversationID it returns an empty argv (nothing to resume); a non-empty
	// argv always carries the id.
	Resume(spec ResumeSpec) ([]string, error)

	// ExtractConversationID reads the conversation id out of the CLI's rendered
	// grid and/or raw transcript tail. It is TOTAL — never panics on a nil or
	// garbage grid+tail — and deterministic. ok==true implies a non-empty id.
	ExtractConversationID(grid *vt.Snap, tail []byte) (string, bool)
}

// Detection is the result of probing the host for the CLI.
type Detection struct {
	Found   bool   // the CLI binary was located
	Path    string // absolute path to the binary, when Found
	Version string // detected version string, when Found
	InRange bool   // Version falls within SupportedVersions
}

// VersionConstraint is an inclusive [Min, Max] version range.
type VersionConstraint struct {
	Min string
	Max string
}

// OptionSpec declares one launch option the form renders and validates.
type OptionSpec struct {
	Key      string   // unique option key
	Label    string   // human-readable label
	Type     string   // "string" | "bool" | "choice"
	Default  string   // default value; for a choice it must be "" or one of Choices
	Choices  []string // permitted values when Type == "choice"
	Required bool     // the form must collect a value
}

// SignalSource declares one way the CLI's idle/active state is observed. Kind is
// one of "hook", "event", or "heuristic"; Descriptor carries kind-specific
// metadata (e.g. the hook event name or the grid heuristic).
type SignalSource struct {
	Kind       string
	Descriptor map[string]string
}

// LaunchSpec is the input to Command: the working directory, the resolved
// option values, and an optional initial prompt.
type LaunchSpec struct {
	Cwd           string
	Options       map[string]string
	InitialPrompt string
}

// ResumeSpec is the input to Resume: the working directory, the conversation to
// resume, and the resolved option values.
type ResumeSpec struct {
	Cwd            string
	ConversationID string
	Options        map[string]string
}
