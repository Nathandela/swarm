// Package agy is the Antigravity CLI (agy) adapter (v1.1 CLI duo, issue
// agents-tracker-5gv, plan .claude/tmp/cli-duo-implementation-plan.md Phase D):
// a stateless, goroutine-safe strategy object over the `agy` CLI. It answers
// the frozen adapter.Adapter contract and NOTHING more — it owns no process,
// fd, socket, or disk (core owns all lifecycle), so its only in-module
// dependencies are the contract package and internal/vt (the T-5 boundary).
//
// agy is heuristic-only (T-3): it has no argv-injectable hook mechanism (its
// hooks are file-configured under .agents/hooks.json — wiring them would
// mutate workspace config, out of scope per design doc docs/design/cli-trio-
// adapters.md section 4) and no typed event stream reachable without spawning
// a server. Status is read from the rendered grid via the R-C1 descriptor
// contract, using the FROZEN R-B4 marker table (docs/verification/cli-duo-
// adapters-evidence.md, Phase B): "esc to cancel" (a persistent footer hint,
// present through the whole generation incl. thought/tool sub-states — the
// load-bearing busy marker) and "Generating..." (spinner-label reinforcement)
// as busy-contains rules, and a bare ">" idle-line-equals rule (the bordered-
// box prompt). Both busy markers are required: Phase B's byte-granularity
// replay found a 72-byte transient window where a mid-redraw spinner glyph
// overwrites "esc to cancel" while "Generating..." is still being typed over
// the same footer cells — a single marker would risk a false idle read there.
//
// ExtractConversationID (R-D6) recovers the id from agy's exit screen
// ("agy --conversation=<uuid>"): the LAST occurrence of the full command
// context (not the bare "--conversation=" flag, which could appear in
// unrelated output) is searched, tail first then the rendered grid. The
// token following it must be completely and validly terminated — by any byte
// <= 0x20, == 0x7f, or >= 0x80 (the last clause stops a UTF-8-encoded C1
// control such as CSI without decoding it, cf. commit a817cfd) — and shaped
// as a lowercase-hex 8-4-4-4-12 UUID; a token that runs to EOF with no
// terminator is rejected (C3: a mid-write read may have truncated it).
package agy

import (
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

// binary is the `agy` executable name on PATH.
const binary = "agy"

// conversationMarker precedes the conversation id on agy's exit screen, e.g.
// "Resume with -c (or command below):\nagy --conversation=<uuid>" (Phase B
// evidence: raw offsets 7422 and 10035 in testdata/agy.json). The full command
// context is searched, not the bare "--conversation=" flag, so a match cannot
// be mistaken for unrelated flag mentions in scrolled output.
const conversationMarker = "agy --conversation="

// agyAdapter is the stateless agy strategy object. It carries no state, so it
// is shared by value and is safe across goroutines.
type agyAdapter struct{}

// New builds the agy adapter.
func New() adapter.Adapter { return agyAdapter{} }

func (agyAdapter) Name() string { return "agy" }

func (agyAdapter) Binary() string { return binary }

func (agyAdapter) VersionArgs() []string { return []string{"--version"} }

// ParseVersion reads the first dotted-numeric token out of the version banner
// (`agy --version` prints a bare "1.1.4\n"). It is pure and total: any string
// yields ("", false) without panicking.
func (agyAdapter) ParseVersion(output string) (string, bool) {
	return firstDottedNumeric(output)
}

// SupportedVersions is the inclusive version range this adapter drives. The
// floor sits at the characterized 1.1.x line (R-D1); the ceiling is an open
// sentinel — agy's background auto-updater mutates the installed binary
// between launches (design doc section 6), so pinning a ceiling would grey a
// freshly-updated, still-compatible CLI.
func (agyAdapter) SupportedVersions() adapter.VersionConstraint {
	return adapter.VersionConstraint{Min: "1.1.0", Max: "9999.0.0"}
}

// Command composes the launch argv: `agy` + [--model M] + [--mode M] +
// [--dangerously-skip-permissions] + [--prompt-interactive P], in that FIXED
// order (R-D2). It is pure and deterministic.
func (agyAdapter) Command(spec adapter.LaunchSpec) ([]string, error) {
	argv := []string{binary}
	argv = append(argv, optionFlags(spec.Options)...)
	if spec.InitialPrompt != "" {
		argv = append(argv, "--prompt-interactive", spec.InitialPrompt)
	}
	return argv, nil
}

// Options is the declarative launch-option schema the launch form renders
// (R-D3): model (free string, curated by the 8 `agy models` display names),
// mode (accept-edits|plan, no default — the CLI's own default applies when
// unset), and dangerously-skip-permissions (bool, default "false").
func (agyAdapter) Options() []adapter.OptionSpec {
	return []adapter.OptionSpec{
		{Key: "model", Label: "Model", Type: "string", Suggest: []string{
			"Gemini 3.5 Flash (Medium)", "Gemini 3.5 Flash (High)", "Gemini 3.5 Flash (Low)",
			"Gemini 3.1 Pro (Low)", "Gemini 3.1 Pro (High)",
			"Claude Sonnet 4.6 (Thinking)", "Claude Opus 4.6 (Thinking)", "GPT-OSS 120B (Medium)",
		}},
		{Key: "mode", Label: "Execution mode", Type: "choice", Choices: []string{"accept-edits", "plan"}},
		{Key: "dangerously-skip-permissions", Label: "Skip permission prompts", Type: "bool", Default: "false"},
	}
}

// SignalSources declares EXACTLY the frozen R-B4 marker table (R-D4): the
// generic prompt-marker fallback, the busy-contains UNION ("esc to cancel" +
// "Generating..." — see the package doc's spinner-overwrite note), and the
// idle-line-equals bare ">" rule. agy declares no hook or event source (T-3
// heuristic-only, v1).
func (agyAdapter) SignalSources() []adapter.SignalSource {
	return []adapter.SignalSource{
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "prompt-marker"}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "busy-contains", "value": "esc to cancel"}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "busy-contains", "value": "Generating..."}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "idle-line-equals", "value": ">"}},
	}
}

// Resume composes `agy --conversation <id>`; an empty id resumes nothing
// (R-D5).
func (agyAdapter) Resume(spec adapter.ResumeSpec) ([]string, error) {
	if spec.ConversationID == "" {
		return nil, nil
	}
	return []string{binary, "--conversation", spec.ConversationID}, nil
}

// ExtractConversationID recovers the conversation id from the raw capture,
// falling back to the rendered grid (R-D6). It is total (a nil/garbage grid
// and tail never panic) and deterministic; ok==true implies a non-empty,
// UUID-shaped id.
func (agyAdapter) ExtractConversationID(grid *vt.Snap, tail []byte) (string, bool) {
	if id, ok := conversationIDFrom(string(tail)); ok {
		return id, true
	}
	return conversationIDFrom(gridText(grid))
}

// conversationIDFrom finds the LAST conversationMarker occurrence in s and
// reads its token. Total: an absent marker or an invalid/incomplete token
// yields ("", false).
func conversationIDFrom(s string) (string, bool) {
	i := strings.LastIndex(s, conversationMarker)
	if i < 0 {
		return "", false
	}
	return uuidTokenAt(s[i+len(conversationMarker):])
}

// uuidTokenAt reads the token at the start of rest, terminated by any byte
// <= 0x20, == 0x7f, or >= 0x80. A token that runs to EOF with no terminator is
// rejected (C3: a mid-write read may have truncated it), and the terminated
// token must match the UUID shape 8-4-4-4-12 lowercase hex.
func uuidTokenAt(rest string) (string, bool) {
	end := -1
	for j := 0; j < len(rest); j++ {
		if c := rest[j]; c <= 0x20 || c == 0x7f || c >= 0x80 {
			end = j
			break
		}
	}
	if end < 0 {
		return "", false // unterminated at EOF: token may be truncated mid-write
	}
	token := rest[:end]
	if !isUUID(token) {
		return "", false
	}
	return token, true
}

// isUUID reports whether s is a lowercase-hex 8-4-4-4-12 UUID.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return false
			}
			continue
		}
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// optionFlags translates resolved option values into agy flags in the FIXED
// order model, mode, dangerously-skip-permissions, so Command stays
// deterministic (R-D2).
func optionFlags(opts map[string]string) []string {
	var flags []string
	if m := opts["model"]; m != "" {
		flags = append(flags, "--model", m)
	}
	if m := opts["mode"]; m != "" {
		flags = append(flags, "--mode", m)
	}
	if opts["dangerously-skip-permissions"] == "true" {
		flags = append(flags, "--dangerously-skip-permissions")
	}
	return flags
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
