// Package claude is the Claude Code adapter (Epic 11, E11.2/E11.6/E11.8): a
// stateless, goroutine-safe strategy object over the `claude` CLI. It answers the
// frozen adapter.Adapter contract and NOTHING more — it owns no process, fd,
// socket, or disk (core owns all lifecycle), so its only in-module dependencies
// are the contract package and internal/vt (the T-5 boundary E11.8 greps for).
//
// Claude Code reports status through SETTINGS-CONFIGURED HOOKS: the documented
// events (PreToolUse/PostToolUse/Notification/Stop/SubagentStop/UserPromptSubmit),
// plus PermissionRequest as the DEDICATED permission event, each posting a JSON
// payload. Because the adapter owns no fds it cannot write a settings file, so
// Command injects the hooks as an INLINE-JSON --settings value (T-2, per-invocation
// — never a global-config mutation) that wires every event to `swarm hook <event>`.
// Idle/active is derived from those hooks via the engine's SignalSource mapping,
// with the generic grid heuristic as the T-3 fallback. Notification is NOT
// unconditionally a permission prompt: it maps by its subtype (its default is a
// permission nudge, but an idle-subtype Notification maps to interaction none), so
// the permission signal proper is PermissionRequest. The exact real event set +
// the Notification subtype field are VERIFY items for Epic 14's live smoke (T-6).
package claude

import (
	"encoding/json"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

// binary is the `claude` executable name on PATH.
const binary = "claude"

// hookCommandPrefix is the swarm subcommand each hook event invokes; the event
// name is appended (e.g. "swarm hook Stop").
const hookCommandPrefix = "swarm hook "

// sessionMarker is the label Claude Code prints before its session id, both in the
// rendered grid and the raw capture; the id is the token that follows it.
const sessionMarker = "Session "

// hookEvents are Claude Code's settings-configured hook events and their status
// mapping (the engine's generic "turn"/"interaction" dimensions). The values are
// the status-package string constants, spelled literally so this package depends
// only on the contract + vt (T-5): a hook may not import internal/status.
//
// Notification is subtype-driven: its interaction comes from the payload subtype via
// subtypeMap (permission->permission, idle->none, prompt->prompt). The nominal
// descriptor interaction (permission) is the DOCUMENTED value, but at runtime a
// MISSING or UNKNOWN subtype degrades to interaction=none in the engine (B5) — the
// engine never asserts a permission prompt it cannot confirm from the payload.
// PermissionRequest is the unconditional, dedicated permission event, so a genuine
// permission signal never depends on guessing a Notification subtype.
var hookEvents = []struct {
	event, turn, interaction string
	subtypeField, subtypeMap string
}{
	{"UserPromptSubmit", "active", "none", "", ""},
	{"PreToolUse", "active", "none", "", ""},
	{"PostToolUse", "active", "none", "", ""},
	{"Notification", "idle", "permission", "notification_type", "idle=none;permission=permission;prompt=prompt"},
	{"Stop", "idle", "none", "", ""},
	{"SubagentStop", "active", "none", "", ""},
	{"PermissionRequest", "idle", "permission", "", ""},
}

// claudeAdapter is the stateless Claude Code strategy object. It carries no state,
// so it is shared by value and is safe across goroutines.
type claudeAdapter struct{}

// New builds the Claude Code adapter.
func New() adapter.Adapter { return claudeAdapter{} }

func (claudeAdapter) Name() string { return "claude-code" }

func (claudeAdapter) Binary() string { return binary }

func (claudeAdapter) VersionArgs() []string { return []string{"--version"} }

// ParseVersion reads the first dotted-numeric token out of the version banner
// (`claude --version` prints e.g. "2.1.212 (Claude Code)"). It is pure and total:
// any string yields ("", false) without panicking.
func (claudeAdapter) ParseVersion(output string) (string, bool) {
	return firstDottedNumeric(output)
}

// SupportedVersions is the inclusive range this adapter drives. The floor sits
// above the pre-2.0 era so an out-of-range CLI greys in the picker (L-2); the
// ceiling is an open sentinel (mirrors the reference adapter).
func (claudeAdapter) SupportedVersions() adapter.VersionConstraint {
	return adapter.VersionConstraint{Min: "2.0.0", Max: "9999.0.0"}
}

// Command composes the launch argv: `claude` + the inline-JSON --settings hook
// injection (T-2) + any declared option flags + the initial prompt (positional).
// It is pure and deterministic — the settings JSON is emitted with sorted keys.
func (claudeAdapter) Command(spec adapter.LaunchSpec) ([]string, error) {
	settings, err := hookSettingsJSON()
	if err != nil {
		return nil, err
	}
	argv := []string{binary, "--settings", settings}
	argv = append(argv, optionFlags(spec.Options)...)
	if spec.InitialPrompt != "" {
		argv = append(argv, spec.InitialPrompt)
	}
	return argv, nil
}

// Options is the declarative launch-option schema the launch form renders.
func (claudeAdapter) Options() []adapter.OptionSpec {
	return []adapter.OptionSpec{
		{Key: "model", Label: "Model", Type: "string"},
		{Key: "dangerously-skip-permissions", Label: "Skip permission prompts", Type: "bool", Default: "false"},
	}
}

// SignalSources declares Claude Code's six hook events with their status mapping,
// plus the generic grid heuristic as the T-3 fallback.
func (claudeAdapter) SignalSources() []adapter.SignalSource {
	sources := make([]adapter.SignalSource, 0, len(hookEvents)+1)
	for _, h := range hookEvents {
		desc := map[string]string{
			"event":       h.event,
			"turn":        h.turn,
			"interaction": h.interaction,
		}
		// The optional subtype refinement (Notification): the engine reads these keys
		// (its descKeySubtypeField / descKeySubtypeMap) to map the interaction by a
		// payload subtype. Spelled literally to keep the T-5 boundary (no engine import).
		if h.subtypeField != "" {
			desc["subtype_field"] = h.subtypeField
			desc["subtype_interaction"] = h.subtypeMap
		}
		sources = append(sources, adapter.SignalSource{Kind: "hook", Descriptor: desc})
	}
	sources = append(sources, adapter.SignalSource{
		Kind:       "heuristic",
		Descriptor: map[string]string{"grid": "prompt-marker"},
	})
	return sources
}

// Resume composes `claude --resume <id>`; an empty id resumes nothing.
func (claudeAdapter) Resume(spec adapter.ResumeSpec) ([]string, error) {
	if spec.ConversationID == "" {
		return nil, nil
	}
	return []string{binary, "--resume", spec.ConversationID}, nil
}

// ExtractConversationID recovers the session id from the raw capture, falling back
// to the rendered grid. It is total (a nil/garbage grid and tail never panic) and
// deterministic; ok==true implies a non-empty id.
func (claudeAdapter) ExtractConversationID(grid *vt.Snap, tail []byte) (string, bool) {
	if id, ok := sessionIDFrom(string(tail)); ok {
		return id, true
	}
	return sessionIDFrom(gridText(grid))
}

// hookSettingsJSON renders the inline --settings value that installs the swarm
// hooks per-invocation. It marshals a fixed structure (sorted map keys), so the
// output is deterministic and valid JSON.
func hookSettingsJSON() (string, error) {
	type cmd struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type matcher struct {
		Hooks []cmd `json:"hooks"`
	}
	hooks := make(map[string][]matcher, len(hookEvents))
	for _, h := range hookEvents {
		hooks[h.event] = []matcher{{Hooks: []cmd{{Type: "command", Command: hookCommandPrefix + h.event}}}}
	}
	b, err := json.Marshal(struct {
		Hooks map[string][]matcher `json:"hooks"`
	}{Hooks: hooks})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// optionFlags translates resolved option values into claude flags in a fixed
// order, so Command stays deterministic.
func optionFlags(opts map[string]string) []string {
	var flags []string
	if m := opts["model"]; m != "" {
		flags = append(flags, "--model", m)
	}
	if opts["dangerously-skip-permissions"] == "true" {
		flags = append(flags, "--dangerously-skip-permissions")
	}
	return flags
}

// sessionIDFrom returns the whitespace-delimited token following the session
// marker in s. It is total; an absent marker or empty token yields ("", false).
func sessionIDFrom(s string) (string, bool) {
	i := strings.Index(s, sessionMarker)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(sessionMarker):]
	if end := strings.IndexAny(rest, " \t\r\n"); end >= 0 {
		rest = rest[:end]
	}
	if rest == "" {
		return "", false
	}
	return rest, true
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
