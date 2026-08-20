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
//
// `capture` is ADR-010 §1's declaration that the event's BODY must be preserved rather than
// flattened to top-level strings. Wave R7 sets it on every row this package's
// InteractionSource actually reads a body from -- which is what makes the declaration and the
// shaper agree by construction (conformance obligation 1), and what makes
// r1-codex-fixtures/frame-samples.json the golden vector set for both.
var eventSources = []struct {
	event, turn, interaction string
	capture                  bool
}{
	{event: "turn/started", turn: "active", interaction: "none"},
	{event: "turn/completed", turn: "idle", interaction: "none", capture: true},
	{event: "item/commandExecution/requestApproval", turn: "idle", interaction: "permission", capture: true},
	// Wave R7 / M4.5. Both rows are RECORDED, and both close real holes.
	//
	// item/fileChange/requestApproval is the approval the R1 gate actually CAPTURED
	// (r1-codex-fixtures/approval-request.json). The commandExecution sibling above has been
	// declared since Epic 11 and the gate never saw it fire, so on the one approval flow
	// anybody has run end to end, the declared mapping did not fire at all.
	//
	// serverRequest/resolved is what CLEARS `permission`. The server broadcasts it to every
	// attached client the instant ANY of them answers (frame-samples.json,
	// r1-codex-gate.md:129-131), so without the row a session the OWNER approved at the
	// terminal keeps showing an awaiting-input badge on the phone until the turn ends.
	{event: "item/fileChange/requestApproval", turn: "idle", interaction: "permission", capture: true},
	{event: "serverRequest/resolved", interaction: "none"},
	// The CONTENT rows. They map no status dimension at all -- the engine's deriveDims
	// drops an empty turn and an empty interaction, so they are benign no-ops on the status
	// path -- and exist because their bodies are what M4.2 shapes into the transcript:
	// the prompt, the streamed prose increments, and a tool run's args and results.
	{event: "item/started", capture: true},
	{event: "item/completed", capture: true},
	{event: "item/agentMessage/delta", capture: true},
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
// codex grid signature as the T-3 fallback (ADR-007) — and, since Codex's typed
// event producer is deferred (D1), the runtime status driver. Codex uses events,
// never hooks. The heuristic Descriptor names the "codex" signature; the engine
// interprets it (the adapter stays I/O-free, ADR-001).
func (codexAdapter) SignalSources() []adapter.SignalSource {
	sources := make([]adapter.SignalSource, 0, len(eventSources)+1)
	for _, e := range eventSources {
		desc := map[string]string{
			"event":       e.event,
			"turn":        e.turn,
			"interaction": e.interaction,
		}
		if e.capture {
			desc[adapter.DescriptorCapture] = adapter.CaptureRaw
		}
		sources = append(sources, adapter.SignalSource{Kind: "event", Descriptor: desc})
	}
	sources = append(sources, adapter.SignalSource{
		Kind:       "heuristic",
		Descriptor: map[string]string{"grid": "codex"},
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
