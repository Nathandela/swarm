// Package opencode is the opencode adapter (Phase E, R-E1..R-E8): a stateless,
// goroutine-safe strategy object over the `opencode` CLI. Like every adapter it
// owns no process, fd, socket, or disk (core owns all lifecycle), so its only
// in-module dependencies are the contract package and internal/vt (the T-5
// boundary).
//
// opencode's real status surface is its own HTTP+SSE server: a SINGLE
// session.status event carrying a status payload (busy/idle/retry), plus
// separate permission/question request objects — not the per-transition typed
// events (turn/started, turn/completed, ...) the engine's exact-event-name +
// static-turn mapping expects today (that shape fits Codex's app-server, not
// opencode's single status event). Declaring invented flattened event names
// (e.g. "session.status.busy") would encode a fake wire schema, so
// SignalSources declares HEURISTICS ONLY (R-E4): the generic grid prompt-marker
// plus a busy-contains rule on "esc interrupt", the one stable footer marker the
// Phase B characterization (docs/verification/cli-duo-adapters-evidence.md)
// proved present across the ENTIRE busy window with zero gaps. That same
// characterization could not jointly satisfy a stable idle substring (R-B2b/c),
// so opencode declares NO idle rule — unknown at rest is the honest T-4 outcome,
// not a guessed-wrong idle. The typed-event path (a payload-to-turn subtype
// contract for session.status) is documented future work, not implemented here.
package opencode

import (
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

// binary is the `opencode` executable name on PATH.
const binary = "opencode"

// idPrefix starts every opencode session id.
const idPrefix = "ses_"

// exitMarker precedes the session id on opencode's exit screen: "opencode -s
// ses_<id>" (Phase B evidence: raw offset 76231 in testdata/opencode.json).
// Extraction is anchored to the LAST occurrence of this full command context,
// not the bare "ses_" prefix alone (mirrors agy's "agy --conversation="
// anchor design) — the daemon scans the live transcript every 200ms and
// persists write-once first-wins, so a mid-session prose mention of a
// ses_-shaped token (e.g. "inspect ses_abcdefghij please") must never be
// mistaken for the real exit id, which would permanently stick.
const exitMarker = "opencode -s "

// minIDLen is the minimum alnum run length after idPrefix a match must carry,
// so a stray short "ses_" substring is not misread as a session id.
const minIDLen = 10

// opencodeAdapter is the stateless opencode strategy object; shared by value,
// safe across goroutines.
type opencodeAdapter struct{}

// New builds the opencode adapter.
func New() adapter.Adapter { return opencodeAdapter{} }

func (opencodeAdapter) Name() string { return "opencode" }

func (opencodeAdapter) Binary() string { return binary }

func (opencodeAdapter) VersionArgs() []string { return []string{"--version"} }

// ParseVersion reads the first dotted-numeric token out of the version banner
// (`opencode --version` prints a bare version, e.g. "1.17.9" — no CLI-name
// prefix, unlike claude/codex). It is pure and total.
func (opencodeAdapter) ParseVersion(output string) (string, bool) {
	return firstDottedNumeric(output)
}

// SupportedVersions is the inclusive range this adapter drives. The floor pins
// the characterized 1.x line; the ceiling is an open sentinel.
func (opencodeAdapter) SupportedVersions() adapter.VersionConstraint {
	return adapter.VersionConstraint{Min: "1.0.0", Max: "9999.0.0"}
}

// Command composes the launch argv: `opencode` + [--model provider/model] +
// [--agent A] + [--prompt P] (fixed order). It is pure and deterministic.
func (opencodeAdapter) Command(spec adapter.LaunchSpec) ([]string, error) {
	argv := []string{binary}
	argv = append(argv, optionFlags(spec.Options)...)
	if spec.InitialPrompt != "" {
		argv = append(argv, "--prompt", spec.InitialPrompt)
	}
	return argv, nil
}

// Options is the declarative launch-option schema the launch form renders.
func (opencodeAdapter) Options() []adapter.OptionSpec {
	return []adapter.OptionSpec{
		{Key: "model", Label: "Model (provider/model)", Type: "string"},
		{Key: "agent", Label: "Agent", Type: "string"},
	}
}

// SignalSources declares opencode's heuristic-only status surface — see the
// package doc for why there are no event declarations and no idle rule.
func (opencodeAdapter) SignalSources() []adapter.SignalSource {
	return []adapter.SignalSource{
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "prompt-marker"}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "busy-contains", "value": "esc interrupt"}},
	}
}

// Resume composes `opencode --session <id>`; an empty id resumes nothing.
func (opencodeAdapter) Resume(spec adapter.ResumeSpec) ([]string, error) {
	if spec.ConversationID == "" {
		return nil, nil
	}
	return []string{binary, "--session", spec.ConversationID}, nil
}

// ExtractConversationID recovers the session id from the raw capture, falling
// back to the rendered grid. It is total (a nil/garbage grid and tail never
// panic) and deterministic; ok==true implies a non-empty id.
func (opencodeAdapter) ExtractConversationID(grid *vt.Snap, tail []byte) (string, bool) {
	if id, ok := lastSessionToken(string(tail)); ok {
		return id, true
	}
	return lastSessionToken(gridText(grid))
}

// optionFlags translates resolved option values into opencode flags in a fixed
// order, so Command stays deterministic.
func optionFlags(opts map[string]string) []string {
	var flags []string
	if m := opts["model"]; m != "" {
		flags = append(flags, "--model", m)
	}
	if a := opts["agent"]; a != "" {
		flags = append(flags, "--agent", a)
	}
	return flags
}

// lastSessionToken finds the id following the LAST exitMarker occurrence in
// s, requiring: idPrefix immediately after the marker, at least minIDLen
// alnum characters after the prefix, and a terminator byte after the token
// (any non-alnum byte, but NOT end-of-input: a token running to EOF is a
// transcript read mid-write and is rejected rather than committed partial,
// C3). It is total and deterministic.
func lastSessionToken(s string) (string, bool) {
	i := strings.LastIndex(s, exitMarker)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(exitMarker):]
	if !strings.HasPrefix(rest, idPrefix) {
		return "", false
	}
	rest = rest[len(idPrefix):]
	end := 0
	for end < len(rest) && isAlnum(rest[end]) {
		end++
	}
	if end >= minIDLen && end < len(rest) {
		return idPrefix + rest[:end], true
	}
	return "", false
}

// isAlnum reports whether c is an ASCII letter or digit.
func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
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
