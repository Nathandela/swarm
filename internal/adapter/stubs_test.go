package adapter

// Epic 9 — shared test stubs (white-box package adapter).
//
// These stubs implement the FROZEN Adapter contract (E9.1 / T-1) in-process so
// the conformance harness can be exercised WITHOUT importing any real adapter
// package: the reference adapter lives in internal/adapter/refadapter and is
// tested there (it consumes this contract package, so it cannot compile until
// the contract exists — see refadapter/refadapter_test.go). Keeping the
// contract-package tests self-contained is what lets the pinned RED command
//
//	go test ./internal/adapter/ ./cmd/swarm-char/
//
// fail with UNDEFINED SYMBOLS ONLY (no "no non-test Go files" import errors).
//
// baseAdapter is a fully conformant strategy object: stateless, goroutine-safe,
// owns no fds/disk/sockets. Every violator below embeds it and overrides ONE
// method with a single, targeted defect, so a conformance failure pinpoints the
// rule under test.

import (
	"bytes"
	"fmt"
	"sync/atomic"

	"github.com/Nathandela/swarm/internal/vt"
)

// convMarker is the token baseAdapter.ExtractConversationID scans for. A real
// adapter scans the CLI's real transcript/grid; the stub uses a fixed marker so
// the extraction is deterministic and testable.
const convMarker = "conv-id="

// baseAdapter is the conformant reference stub for exercising the harness.
type baseAdapter struct{}

func (baseAdapter) Name() string { return "stub" }

func (baseAdapter) Detect() (Detection, error) {
	return Detection{Found: true, Path: "/usr/bin/stub", Version: "1.2.0", InRange: true}, nil
}

func (baseAdapter) SupportedVersions() VersionConstraint {
	return VersionConstraint{Min: "1.0.0", Max: "2.0.0"}
}

func (baseAdapter) Command(spec LaunchSpec) ([]string, error) {
	argv := []string{"stub-cli", "--cwd", spec.Cwd}
	if spec.InitialPrompt != "" {
		argv = append(argv, "--prompt", spec.InitialPrompt)
	}
	return argv, nil
}

func (baseAdapter) Options() []OptionSpec {
	return []OptionSpec{
		{Key: "model", Label: "Model", Type: "choice", Choices: []string{"fast", "smart"}, Default: "smart", Required: true},
		{Key: "yolo", Label: "Skip permissions", Type: "bool", Default: "false"},
		{Key: "note", Label: "Note", Type: "string"},
	}
}

func (baseAdapter) SignalSources() []SignalSource {
	return []SignalSource{
		{Kind: "hook", Descriptor: map[string]string{"event": "Stop"}},
		{Kind: "event", Descriptor: map[string]string{"stream": "app-server"}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "esc-to-interrupt"}},
	}
}

func (baseAdapter) Resume(spec ResumeSpec) ([]string, error) {
	if spec.ConversationID == "" {
		return nil, nil // cannot resume without an id
	}
	return []string{"stub-cli", "--resume", spec.ConversationID, "--cwd", spec.Cwd}, nil
}

func (baseAdapter) ExtractConversationID(grid *vt.Snap, tail []byte) (string, bool) {
	// Total: a nil grid and a nil/empty tail must not panic. bytes.Index(nil,
	// ...) is -1, so a missing marker yields ("", false) cleanly. The stub reads
	// only the tail; the grid is ignored but exercised as safe-when-nil.
	_ = grid
	if i := bytes.Index(tail, []byte(convMarker)); i >= 0 {
		rest := tail[i+len(convMarker):]
		end := bytes.IndexAny(rest, " \t\r\n")
		if end < 0 {
			end = len(rest)
		}
		if id := string(rest[:end]); id != "" {
			return id, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Targeted violators — each breaks exactly one conformance rule.
// ---------------------------------------------------------------------------

// emptyName violates: Name non-empty.
type emptyName struct{ baseAdapter }

func (emptyName) Name() string { return "" }

// unstableName violates: Name stable across calls.
type unstableName struct {
	baseAdapter
	n atomic.Int64
}

func (u *unstableName) Name() string { return fmt.Sprintf("stub-%d", u.n.Add(1)) }

// shellCommand violates: argv[0] must never be a shell.
type shellCommand struct{ baseAdapter }

func (shellCommand) Command(spec LaunchSpec) ([]string, error) {
	return []string{"sh", "-c", "stub-cli --cwd " + spec.Cwd}, nil
}

// singleStringCommand violates: never a shell-metacharacter-interpretable
// single string; argv[0] must be a real program path.
type singleStringCommand struct{ baseAdapter }

func (singleStringCommand) Command(spec LaunchSpec) ([]string, error) {
	return []string{"stub-cli --cwd " + spec.Cwd + " && rm -rf /"}, nil
}

// emptyCommand violates: Command must return at least the program.
type emptyCommand struct{ baseAdapter }

func (emptyCommand) Command(LaunchSpec) ([]string, error) { return nil, nil }

// nondeterministicCommand violates: Command is pure (same spec -> same argv).
type nondeterministicCommand struct {
	baseAdapter
	n atomic.Int64
}

func (c *nondeterministicCommand) Command(LaunchSpec) ([]string, error) {
	return []string{"stub-cli", fmt.Sprintf("--nonce=%d", c.n.Add(1))}, nil
}

// badDefaultOption violates: a required option's Default must be valid (empty
// or one of Choices).
type badDefaultOption struct{ baseAdapter }

func (badDefaultOption) Options() []OptionSpec {
	return []OptionSpec{{Key: "model", Label: "Model", Type: "choice", Choices: []string{"a", "b"}, Default: "zzz", Required: true}}
}

// emptyChoiceOption violates: Type=="choice" requires non-empty Choices.
type emptyChoiceOption struct{ baseAdapter }

func (emptyChoiceOption) Options() []OptionSpec {
	return []OptionSpec{{Key: "model", Label: "Model", Type: "choice"}}
}

// dupKeyOption violates: option Keys must be unique.
type dupKeyOption struct{ baseAdapter }

func (dupKeyOption) Options() []OptionSpec {
	return []OptionSpec{
		{Key: "x", Label: "X", Type: "string"},
		{Key: "x", Label: "X2", Type: "bool", Default: "false"},
	}
}

// badSignalKind violates: SignalSource.Kind in {hook,event,heuristic}.
type badSignalKind struct{ baseAdapter }

func (badSignalKind) SignalSources() []SignalSource {
	return []SignalSource{{Kind: "telepathy", Descriptor: map[string]string{}}}
}

// resumeWithoutID violates: Resume must be empty when no ConversationID is
// supplied (an argv that "resumes" nothing is malformed).
type resumeWithoutID struct{ baseAdapter }

func (resumeWithoutID) Resume(spec ResumeSpec) ([]string, error) {
	return []string{"stub-cli", "--resume", spec.ConversationID}, nil
}

// resumeOmitsID violates: a non-empty Resume argv must carry the
// ConversationID — an argv that "resumes" without naming the session is broken
// ("Resume argv omits nothing required").
type resumeOmitsID struct{ baseAdapter }

func (resumeOmitsID) Resume(spec ResumeSpec) ([]string, error) {
	if spec.ConversationID == "" {
		return nil, nil
	}
	return []string{"stub-cli", "--resume"}, nil // drops the id
}

// panicExtract violates: ExtractConversationID must be total (never panics).
type panicExtract struct{ baseAdapter }

func (panicExtract) ExtractConversationID(grid *vt.Snap, _ []byte) (string, bool) {
	// Dereferences grid unconditionally: panics on the nil grid the totality
	// probe feeds it.
	_ = grid.Cols
	return "", false
}

// okButEmptyExtract violates: when ok is true the id must be non-empty.
type okButEmptyExtract struct{ baseAdapter }

func (okButEmptyExtract) ExtractConversationID(*vt.Snap, []byte) (string, bool) {
	return "", true
}
