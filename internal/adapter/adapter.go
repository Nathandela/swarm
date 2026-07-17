// Package adapter is the FROZEN anti-corruption boundary (Epic 9, T-1/T-5/T-6)
// between swarm's core (daemon/shim/wire/TUI) and the concrete agent CLIs it
// drives. An Adapter is a stateless, goroutine-safe STRATEGY OBJECT: it answers
// questions about a CLI (how it is named on PATH and how its version prints,
// which versions it supports, how to compose its launch/resume argv, which
// launch options and signal sources it exposes, how to read a conversation id
// out of its output) but owns NO lifecycle — no process, no fd, no socket, no
// disk. Core owns all of that. Detection is descriptor-based: the adapter
// supplies pure Binary/VersionArgs/ParseVersion descriptors and the CORE Detect
// function (below) does the LookPath/exec through a HostProber, so the boundary
// stays genuinely I/O-free (ADR-001 / E9.2).
//
// The boundary depends only on this package plus internal/vt (the Snap
// projection ExtractConversationID reads); adding a real adapter later touches
// only that adapter's own package. This file pins the interface method set and
// every data type; conformance.go pins the behavior a conforming adapter must
// exhibit; fixture.go/capability.go pin the characterization corpus and its
// derived capability matrix.
package adapter

import (
	"strconv"
	"strings"

	"github.com/Nathandela/swarm/internal/vt"
)

// Adapter is the ONE interface every agent-CLI integration implements. Every
// method is pure and goroutine-safe: given the same inputs it returns the same
// outputs, holds no mutable state, and may be shared by value across goroutines.
type Adapter interface {
	// Name is the stable, non-empty identifier of the CLI this adapter drives
	// (e.g. "claude-code"). It never changes across calls.
	Name() string

	// Binary is the executable name to find on PATH (e.g. "claude"). It is a
	// pure descriptor — non-empty, never a path with metacharacters — that the
	// CORE Detect function feeds to a HostProber's LookPath. The adapter owns no
	// fds: it merely names what to look for.
	Binary() string

	// VersionArgs are the arguments that make the CLI print its version
	// (e.g. {"--version"}). It is a pure descriptor and must be non-nil (an
	// empty slice, meaning "the bare binary prints its version", is allowed;
	// nil is not). Detect passes these to HostProber.Run.
	VersionArgs() []string

	// ParseVersion extracts a semantic version from the version command's stdout.
	// It is PURE and TOTAL: it never panics on any string (garbage, empty,
	// multibyte, unbounded) and is deterministic. ok==true implies a non-empty
	// version. Detect calls it on the stdout HostProber.Run captured.
	ParseVersion(output string) (version string, ok bool)

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

// HostProber is the host-I/O capability the CORE Detect function needs: locate a
// binary on PATH and run it to capture its stdout. Its implementations own the
// fds and exec and therefore live OUTSIDE this pure contract package (see
// internal/adapter/detect for the real exec-based Host). The contract names only
// the interface, so internal/adapter itself opens nothing.
type HostProber interface {
	// LookPath resolves name to an absolute program path, or returns an error if
	// it is not found on PATH.
	LookPath(name string) (string, error)
	// Run executes path with args and returns its combined version stdout.
	Run(path string, args []string) (stdout string, err error)
}

// Detect probes the host for a's CLI through h and reports what it found. It is
// the CORE-owned detection routine that replaces the old impure Detect method:
// the adapter contributes only pure descriptors (Binary, VersionArgs,
// ParseVersion, SupportedVersions) while h performs every LookPath/exec. A
// binary that is present but whose version cannot be run or parsed is reported
// Found with an empty Version (and InRange false).
func Detect(a Adapter, h HostProber) Detection {
	path, err := h.LookPath(a.Binary())
	if err != nil || path == "" {
		return Detection{}
	}
	det := Detection{Found: true, Path: path}
	out, err := h.Run(path, a.VersionArgs())
	if err != nil {
		return det
	}
	version, ok := a.ParseVersion(out)
	if !ok || version == "" {
		return det
	}
	det.Version = version
	det.InRange = versionInRange(a.SupportedVersions(), version)
	return det
}

// versionInRange reports whether version lies within the inclusive [Min, Max]
// range. It compares dotted numeric components (a leading "v" and any
// pre-release/build suffix are ignored); an empty Min or Max is an open bound.
// It is pure and total.
func versionInRange(vc VersionConstraint, version string) bool {
	if vc.Min != "" && compareSemver(version, vc.Min) < 0 {
		return false
	}
	if vc.Max != "" && compareSemver(version, vc.Max) > 0 {
		return false
	}
	return true
}

// compareSemver compares two dotted-numeric version strings, returning -1, 0, or
// +1. Non-numeric or missing components compare as 0; it never panics.
func compareSemver(a, b string) int {
	an := semverParts(a)
	bn := semverParts(b)
	for i := 0; i < len(an) || i < len(bn); i++ {
		var av, bv int
		if i < len(an) {
			av = an[i]
		}
		if i < len(bn) {
			bv = bn[i]
		}
		switch {
		case av < bv:
			return -1
		case av > bv:
			return 1
		}
	}
	return 0
}

// semverParts splits a version into its leading dotted numeric components,
// tolerating a leading "v" and stopping at the first non-numeric component
// (e.g. a "-rc1" pre-release suffix).
func semverParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var parts []int
	for _, seg := range strings.Split(v, ".") {
		// Trim any pre-release/build suffix on this segment (e.g. "3-rc1").
		if i := strings.IndexAny(seg, "-+"); i >= 0 {
			seg = seg[:i]
		}
		n, err := strconv.Atoi(seg)
		if err != nil {
			break
		}
		parts = append(parts, n)
	}
	return parts
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
