// Package codex is the Codex adapter (Epic 11, E11.4/E11.6/E11.8): a stateless,
// goroutine-safe strategy object over the `codex` CLI. Like every adapter it owns
// no process, fd, socket, or disk (core owns all lifecycle), so its only in-module
// dependencies are the contract package and internal/vt (the T-5 boundary).
//
// Codex reports status through TYPED EVENTS from its app-server JSON-RPC stream —
// turn/started (active) and turn/completed (idle) are NOTIFICATIONS carrying a
// nested params.turn object, and item/commandExecution/requestApproval (permission)
// is a server REQUEST (it carries a JSON-RPC id) — NOT settings hooks. That is the
// second signal style Epic 11 proves against the one frozen interface (claude =
// hooks, codex = events). The app-server carries the conversation as a threadId in
// its JSON-RPC params; ExtractConversationID recovers it from the transcript tail
// regardless of the surrounding nesting. The generic grid heuristic is the T-3
// fallback — and, per audit-010, Codex's v1 RUNTIME status driver, since the live
// app-server typed-event producer is deferred to Epic 14's flagged real-CLI smoke;
// the typed mapping here is fixture-proven pending that live wiring. The status
// mapping keys off the mapped turn/interaction values, so it is resilient to a
// method/field-name drift (T-6, Epic 14 VERIFY).
package codex

import (
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

// binary is the `codex` executable name on PATH.
const binary = "codex"

// threadIDKey is the JSON field carrying the codex conversation id (its app-server
// "threadId") in the transcript tail.
const threadIDKey = `"threadId"`

// eventSources are Codex's typed app-server JSON-RPC status methods and their
// mapping onto the engine's generic "turn"/"interaction" dimensions. The values are
// the status-package string constants, spelled literally so this package depends
// only on the contract + vt (T-5): an adapter may not import internal/status.
var eventSources = []struct {
	event, turn, interaction string
}{
	{"turn/started", "active", "none"},
	{"turn/completed", "idle", "none"},
	{"item/commandExecution/requestApproval", "idle", "permission"},
}

// codexAdapter is the stateless Codex strategy object; shared by value, safe
// across goroutines.
type codexAdapter struct{}

// New builds the Codex adapter.
func New() adapter.Adapter { return codexAdapter{} }

func (codexAdapter) Name() string { return "codex" }

func (codexAdapter) Binary() string { return binary }

func (codexAdapter) VersionArgs() []string { return []string{"--version"} }

// ParseVersion reads the first dotted-numeric token out of the version banner
// (`codex --version` prints e.g. "codex-cli 0.144.1"). It is pure and total.
func (codexAdapter) ParseVersion(output string) (string, bool) {
	return firstDottedNumeric(output)
}

// SupportedVersions is the inclusive range this adapter drives. The floor sits
// well above the ancient 0.1 era so an out-of-range CLI greys in the picker (L-2);
// the ceiling is an open sentinel.
func (codexAdapter) SupportedVersions() adapter.VersionConstraint {
	return adapter.VersionConstraint{Min: "0.100.0", Max: "9999.0.0"}
}

// Command composes the launch argv: `codex` + any declared option flags + the
// initial prompt (positional). It is pure and deterministic.
func (codexAdapter) Command(spec adapter.LaunchSpec) ([]string, error) {
	argv := []string{binary}
	argv = append(argv, optionFlags(spec.Options)...)
	if spec.InitialPrompt != "" {
		argv = append(argv, spec.InitialPrompt)
	}
	return argv, nil
}

// Options is the declarative launch-option schema the launch form renders.
func (codexAdapter) Options() []adapter.OptionSpec {
	return []adapter.OptionSpec{
		{Key: "model", Label: "Model", Type: "string"},
		{Key: "sandbox", Label: "Sandbox mode", Type: "choice",
			Choices: []string{"read-only", "workspace-write", "danger-full-access"}, Default: "workspace-write"},
	}
}

// SignalSources declares Codex's typed status events with their mapping, plus the
// generic grid heuristic as the T-3 fallback. Codex uses events, never hooks.
func (codexAdapter) SignalSources() []adapter.SignalSource {
	sources := make([]adapter.SignalSource, 0, len(eventSources)+1)
	for _, e := range eventSources {
		sources = append(sources, adapter.SignalSource{
			Kind: "event",
			Descriptor: map[string]string{
				"event":       e.event,
				"turn":        e.turn,
				"interaction": e.interaction,
			},
		})
	}
	sources = append(sources, adapter.SignalSource{
		Kind:       "heuristic",
		Descriptor: map[string]string{"grid": "prompt-marker"},
	})
	return sources
}

// Resume composes `codex resume <id>`; an empty id resumes nothing.
func (codexAdapter) Resume(spec adapter.ResumeSpec) ([]string, error) {
	if spec.ConversationID == "" {
		return nil, nil
	}
	return []string{binary, "resume", spec.ConversationID}, nil
}

// ExtractConversationID recovers the conversation id (Codex's app-server threadId)
// from the raw transcript tail's JSON-RPC messages, falling back to the rendered
// grid. It is total (a nil/garbage grid and tail never panic) and deterministic;
// ok==true implies a non-empty id.
func (codexAdapter) ExtractConversationID(grid *vt.Snap, tail []byte) (string, bool) {
	if id, ok := threadIDFrom(string(tail)); ok {
		return id, true
	}
	return threadIDFrom(gridText(grid))
}

// optionFlags translates resolved option values into codex flags in a fixed
// order, so Command stays deterministic.
func optionFlags(opts map[string]string) []string {
	var flags []string
	if m := opts["model"]; m != "" {
		flags = append(flags, "--model", m)
	}
	if s := opts["sandbox"]; s != "" {
		flags = append(flags, "--sandbox", s)
	}
	return flags
}

// threadIDFrom extracts the double-quoted value of the JSON "threadId" field from
// s (Codex's app-server conversation id). It is total: an absent field, missing
// colon/quotes, or empty value yields ("", false), and it never panics on any
// input. It tolerates optional whitespace between the key, the colon, and the value.
func threadIDFrom(s string) (string, bool) {
	i := strings.Index(s, threadIDKey)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(threadIDKey):]
	j := 0
	for j < len(rest) && (rest[j] == ':' || rest[j] == ' ' || rest[j] == '\t') {
		j++
	}
	if j >= len(rest) || rest[j] != '"' {
		return "", false
	}
	rest = rest[j+1:]
	end := strings.IndexByte(rest, '"')
	if end <= 0 {
		return "", false // no closing quote (end<0) or an empty value (end==0)
	}
	return rest[:end], true
}

// gridText concatenates a snapshot's visible text, newline-separated. It is
// nil-safe (a nil or empty grid yields "").
func gridText(snap *vt.Snap) string {
	if snap == nil {
		return ""
	}
	var b strings.Builder
	for _, line := range snap.Lines {
		for _, r := range line.Runs {
			b.WriteString(r.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// firstDottedNumeric scans output for the first "x.y[.z...]" all-numeric token
// (tolerating a leading "v"). It is pure and total.
func firstDottedNumeric(output string) (string, bool) {
	for _, field := range strings.Fields(output) {
		v := strings.TrimPrefix(field, "v")
		parts := strings.Split(v, ".")
		if len(parts) < 2 {
			continue
		}
		if allNumeric(parts) {
			return v, true
		}
	}
	return "", false
}

// allNumeric reports whether every part is non-empty and all ASCII digits.
func allNumeric(parts []string) bool {
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}
