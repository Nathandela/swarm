// Package trioproto holds TEMPORARY prototype adapters for the cli-trio
// integration strategy phase (agy / opencode). They exist to prove,
// against the frozen adapter contract and real recorded PTY captures, that each
// CLI fits the Adapter interface — argv composition, version parsing, resume,
// and conversation-id extraction. Delete this package before the implementation
// epic; the real adapters will live in internal/adapter/{agy,opencode}
// with their own fixture corpus and full test suites.
package trioproto

import (
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

// ---- agy (Antigravity CLI, Google) ----

type agyAdapter struct{}

func NewAgy() adapter.Adapter { return agyAdapter{} }

func (agyAdapter) Name() string                           { return "agy" }
func (agyAdapter) Binary() string                         { return "agy" }
func (agyAdapter) VersionArgs() []string                  { return []string{"--version"} }
func (agyAdapter) ParseVersion(out string) (string, bool) { return firstDottedNumeric(out) }
func (agyAdapter) SupportedVersions() adapter.VersionConstraint {
	return adapter.VersionConstraint{Min: "1.1.0", Max: "9999.0.0"}
}

// Command composes `agy [--model M] [--mode MODE] [--dangerously-skip-permissions]
// [--prompt-interactive PROMPT]`. Verified 2026-07-18 against agy 1.1.4: --model
// accepts the display names `agy models` lists; --prompt-interactive runs an
// initial prompt and keeps the session interactive.
func (agyAdapter) Command(spec adapter.LaunchSpec) ([]string, error) {
	argv := []string{"agy"}
	if m := spec.Options["model"]; m != "" {
		argv = append(argv, "--model", m)
	}
	if m := spec.Options["mode"]; m != "" {
		argv = append(argv, "--mode", m)
	}
	if spec.Options["dangerously-skip-permissions"] == "true" {
		argv = append(argv, "--dangerously-skip-permissions")
	}
	if spec.InitialPrompt != "" {
		argv = append(argv, "--prompt-interactive", spec.InitialPrompt)
	}
	return argv, nil
}

func (agyAdapter) Options() []adapter.OptionSpec {
	return []adapter.OptionSpec{
		{Key: "model", Label: "Model", Type: "string", Suggest: []string{
			"Gemini 3.5 Flash (Medium)", "Gemini 3.5 Flash (High)", "Gemini 3.5 Flash (Low)",
			"Gemini 3.1 Pro (Low)", "Gemini 3.1 Pro (High)",
			"Claude Sonnet 4.6 (Thinking)", "Claude Opus 4.6 (Thinking)", "GPT-OSS 120B (Medium)"}},
		{Key: "mode", Label: "Execution mode", Type: "choice", Choices: []string{"accept-edits", "plan"}},
		{Key: "dangerously-skip-permissions", Label: "Skip permission prompts", Type: "bool", Default: "false"},
	}
}

// SignalSources: v1 is heuristic-driven (codex precedent). The busy-contains
// descriptor is a PROPOSED extension the engine does not read yet: agy renders
// a trailing "<spinner> Generating..." status line while working (verified in
// the recorded capture), which the stock last-line heuristic already catches;
// idle needs the proposed bottom-region rules (see design doc).
func (agyAdapter) SignalSources() []adapter.SignalSource {
	return []adapter.SignalSource{
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "prompt-marker"}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "busy-contains", "value": "Generating..."}},
	}
}

// Resume composes `agy --conversation <id>`; verified 2026-07-18 that a resumed
// print-mode conversation retains context.
func (agyAdapter) Resume(spec adapter.ResumeSpec) ([]string, error) {
	if spec.ConversationID == "" {
		return nil, nil
	}
	return []string{"agy", "--conversation", spec.ConversationID}, nil
}

// agyMarker precedes the conversation id on agy's exit screen:
// "Resume with -c (or command below):\nagy --conversation=<uuid>".
const agyMarker = "--conversation="

func (agyAdapter) ExtractConversationID(grid *vt.Snap, tail []byte) (string, bool) {
	if id, ok := tokenAfter(string(tail), agyMarker); ok {
		return id, true
	}
	return tokenAfter(gridText(grid), agyMarker)
}

// ---- opencode ----

type opencodeAdapter struct{}

func NewOpencode() adapter.Adapter { return opencodeAdapter{} }

func (opencodeAdapter) Name() string                           { return "opencode" }
func (opencodeAdapter) Binary() string                         { return "opencode" }
func (opencodeAdapter) VersionArgs() []string                  { return []string{"--version"} }
func (opencodeAdapter) ParseVersion(out string) (string, bool) { return firstDottedNumeric(out) }
func (opencodeAdapter) SupportedVersions() adapter.VersionConstraint {
	return adapter.VersionConstraint{Min: "1.0.0", Max: "9999.0.0"}
}

// Command composes `opencode [--model provider/model] [--agent A] [--prompt P]`.
// The bare command starts the TUI in the working directory; --prompt seeds the
// first turn (root-level flag, verified in `opencode --help` 1.17.9).
func (opencodeAdapter) Command(spec adapter.LaunchSpec) ([]string, error) {
	argv := []string{"opencode"}
	if m := spec.Options["model"]; m != "" {
		argv = append(argv, "--model", m)
	}
	if a := spec.Options["agent"]; a != "" {
		argv = append(argv, "--agent", a)
	}
	if spec.InitialPrompt != "" {
		argv = append(argv, "--prompt", spec.InitialPrompt)
	}
	return argv, nil
}

func (opencodeAdapter) Options() []adapter.OptionSpec {
	return []adapter.OptionSpec{
		{Key: "model", Label: "Model (provider/model)", Type: "string"},
		{Key: "agent", Label: "Agent", Type: "string"},
	}
}

// SignalSources: opencode's own HTTP+SSE server emits typed session.status
// events (busy/idle/retry) plus discrete permission/question request objects —
// declared here as the future event path (codex precedent: declared, wired in a
// later epic). The heuristic entry is the v1 runtime driver.
func (opencodeAdapter) SignalSources() []adapter.SignalSource {
	return []adapter.SignalSource{
		{Kind: "event", Descriptor: map[string]string{"event": "session.status.busy", "turn": "active", "interaction": "none"}},
		{Kind: "event", Descriptor: map[string]string{"event": "session.status.idle", "turn": "idle", "interaction": "none"}},
		{Kind: "event", Descriptor: map[string]string{"event": "permission.requested", "turn": "idle", "interaction": "permission"}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "prompt-marker"}},
	}
}

// Resume composes `opencode --session <id>` (root-level -s/--session continues
// the given session in the TUI; verified working via `opencode run -s` on
// 1.17.9).
func (opencodeAdapter) Resume(spec adapter.ResumeSpec) ([]string, error) {
	if spec.ConversationID == "" {
		return nil, nil
	}
	return []string{"opencode", "--session", spec.ConversationID}, nil
}

// opencodeIDPrefix starts every opencode session id; the exit screen prints
// "Continue  opencode -s ses_<id>". The LAST occurrence is taken: earlier ones
// in a long transcript may be subagent/list-view ids.
const opencodeIDPrefix = "ses_"

func (opencodeAdapter) ExtractConversationID(grid *vt.Snap, tail []byte) (string, bool) {
	if id, ok := lastSessionToken(string(tail)); ok {
		return id, true
	}
	return lastSessionToken(gridText(grid))
}

// lastSessionToken finds the last "ses_<alnum>" token in s. It requires a
// terminator after the token (mid-write truncation discipline, C3) and a
// minimum id length so stray prose mentioning "ses_" is not misread.
func lastSessionToken(s string) (string, bool) {
	const minIDLen = 10
	for i := len(s); i > 0; {
		j := strings.LastIndex(s[:i], opencodeIDPrefix)
		if j < 0 {
			return "", false
		}
		rest := s[j+len(opencodeIDPrefix):]
		end := 0
		for end < len(rest) && isAlnum(rest[end]) {
			end++
		}
		if end >= minIDLen && end < len(rest) {
			return opencodeIDPrefix + rest[:end], true
		}
		i = j
	}
	return "", false
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// ---- shared helpers (each real adapter package will carry its own copies,
// mirroring claude/codex; duplicated here because T-5 forbids a shared helper
// package on the boundary) ----

// tokenAfter returns the token following marker in s, terminated by whitespace
// OR any control byte — a raw PTY tail can butt an ANSI sequence (e.g. \x1b[K)
// straight against the id, which pure-whitespace termination would swallow
// (caught by the recorded-capture test). A token running to EOF with no
// terminator is rejected (mid-write truncation discipline, C3). Total: absent
// marker or empty token yields ("", false).
func tokenAfter(s, marker string) (string, bool) {
	i := strings.Index(s, marker)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(marker):]
	end := -1
	for j := 0; j < len(rest); j++ {
		if rest[j] <= ' ' || rest[j] == 0x7f {
			end = j
			break
		}
	}
	if end <= 0 {
		return "", false
	}
	return rest[:end], true
}

// gridText concatenates a snapshot's visible text, newline-separated; nil-safe.
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
// (tolerating a leading "v"). Pure and total; mirrors claude/codex.
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
