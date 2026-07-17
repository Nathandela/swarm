// Package refadapter is the fixture-only REFERENCE ADAPTER (E9.5 / T-5). It
// proves the anti-corruption boundary: a conforming adapter can be built purely
// from a recorded fixture and depends on NOTHING but the adapter contract plus
// internal/vt — so adding a real adapter later (Epic 10/11) touches only its own
// package, never daemon/shim/wire/TUI.
//
// It is a stateless strategy object: every method is pure and goroutine-safe,
// and it owns no fds, disk, or sockets. Its ExtractConversationID reads the
// conversation id out of the raw transcript tail (the grid is accepted and
// nil-safe), which is how it recognizes the id its own fixture recorded.
package refadapter

import (
	"bytes"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

// convMarker is the token the reference CLI prints before its conversation id.
const convMarker = "conv-id="

// refAdapter is built from a fixture; it carries only that fixture's identity.
type refAdapter struct {
	cli     string
	version string
}

// New builds the reference adapter from a recorded fixture.
func New(fx adapter.Fixture) adapter.Adapter {
	return refAdapter{cli: fx.CLI, version: fx.Version}
}

// program is the reference CLI's bare program name (argv[0]), falling back to a
// constant when the fixture carried no CLI identity.
func (r refAdapter) program() string {
	if r.cli == "" {
		return "reference-cli"
	}
	return r.cli
}

func (r refAdapter) Name() string { return r.program() }

func (r refAdapter) Detect() (adapter.Detection, error) {
	return adapter.Detection{
		Found:   true,
		Path:    "/usr/local/bin/" + r.program(),
		Version: r.version,
		InRange: true,
	}, nil
}

func (refAdapter) SupportedVersions() adapter.VersionConstraint {
	return adapter.VersionConstraint{Min: "0.0.0", Max: "9999.0.0"}
}

func (r refAdapter) Command(spec adapter.LaunchSpec) ([]string, error) {
	argv := []string{r.program(), "--cwd", spec.Cwd}
	if spec.InitialPrompt != "" {
		argv = append(argv, "--prompt", spec.InitialPrompt)
	}
	return argv, nil
}

func (refAdapter) Options() []adapter.OptionSpec {
	return []adapter.OptionSpec{
		{Key: "model", Label: "Model", Type: "choice", Choices: []string{"fast", "smart"}, Default: "smart", Required: true},
		{Key: "yolo", Label: "Skip permissions", Type: "bool", Default: "false"},
	}
}

func (refAdapter) SignalSources() []adapter.SignalSource {
	return []adapter.SignalSource{
		{Kind: "hook", Descriptor: map[string]string{"event": "Stop"}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "prompt-marker"}},
	}
}

func (r refAdapter) Resume(spec adapter.ResumeSpec) ([]string, error) {
	if spec.ConversationID == "" {
		return nil, nil // nothing to resume
	}
	return []string{r.program(), "--resume", spec.ConversationID, "--cwd", spec.Cwd}, nil
}

// ExtractConversationID scans the raw transcript tail for the conv-id marker.
// It is total (a nil/absent grid and a nil/garbage tail never panic) and
// deterministic; ok==true implies a non-empty id.
func (refAdapter) ExtractConversationID(grid *vt.Snap, tail []byte) (string, bool) {
	_ = grid // the id is read from the transcript tail; grid is accepted, nil-safe
	i := bytes.Index(tail, []byte(convMarker))
	if i < 0 {
		return "", false
	}
	rest := tail[i+len(convMarker):]
	end := bytes.IndexAny(rest, " \t\r\n")
	if end < 0 {
		end = len(rest)
	}
	if id := string(rest[:end]); id != "" {
		return id, true
	}
	return "", false
}
