// Package claude is the Claude Code adapter (Epic 11, E11.2/E11.6/E11.8): a
// stateless, goroutine-safe strategy object over the `claude` CLI. It answers the
// frozen adapter.Adapter contract and NOTHING more — it owns no process, fd,
// socket, or disk (core owns all lifecycle), so its only in-module dependencies
// are the contract package and internal/vt (the T-5 boundary E11.8 greps for).
//
// Claude Code reports status through SETTINGS-CONFIGURED HOOKS: the documented
// events (PreToolUse/PostToolUse/Notification/Stop/SubagentStart/SubagentStop/
// UserPromptSubmit), plus PermissionRequest as the DEDICATED permission event,
// each posting a JSON payload. Because the adapter owns no fds it cannot write a
// settings file, so Command injects the hooks as an INLINE-JSON --settings value
// (T-2, per-invocation — never a global-config mutation) that wires every event
// to `swarm hook <event>`.
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

// transcriptExt is the suffix Claude Code gives each conversation transcript inside
// a project directory (the stem is the session id).
const transcriptExt = ".jsonl"

// hookEvents are Claude Code's settings-configured hook events and their status
// mapping (the engine's generic "turn"/"interaction" dimensions). The values are
// the status-package string constants, spelled literally so this package depends
// only on the contract + vt (T-5): a hook may not import internal/status.
//
// Notification is subtype-driven: its interaction comes from the payload subtype via
// subtypeMap. The map leads with the values a live claude actually posts
// (permission_prompt / idle_prompt, confirmed 3/3 runs in docs/verification/spike-SB.md)
// and keeps the shorter documented spellings behind them. The nominal descriptor
// interaction (permission) is the DOCUMENTED value, but at runtime a MISSING subtype
// degrades to interaction=none in the engine (B5) — the engine never asserts a
// permission prompt it cannot confirm from the payload — and an UNRECOGNIZED subtype
// (a value a newer claude added) yields NO interaction dimension at all, so it cannot
// clobber a permission the dedicated event just set. PermissionRequest is the
// unconditional, dedicated permission event, so a genuine permission signal never
// depends on guessing a Notification subtype.
//
// SubagentStart/SubagentStop bracket every background child (docs/verification/
// spike-SE.md F1). SubagentStart is the only hook reporting that a child BEGAN, so
// it maps turn=active. SubagentStop reports that a child ENDED, which is never
// evidence the session is working — that mapping was the agents-tracker-707 race
// source — so its turn is EMPTY: a blank turn declares NO turn dimension at all
// (the engine's deriveDims drops it), leaving the turn exactly as it found it.
// Whether a session with outstanding children is still working is the engine's
// accounting to make, not this one event's to assert.
//
// `capture` is ADR-010 §5's structured-capture declaration: the body of a row that sets it is
// PRESERVED for interaction.go's shaper instead of being flattened to top-level strings at
// ingest (§6). It is set on exactly the five rows that shaper turns into items, because both
// directions are conformance violations — a shaped event that does not declare it never receives
// a body, and a declared event that shapes nothing preserves one for nobody.
//
// Stop is the fifth row (ADR-010's 2026-08-07 amendment): its body carries
// `last_assistant_message`, the only agent PROSE anywhere in the recorded corpus. Capture here
// is what puts the agent's replies in the phone's transcript at all — the other four rows carry
// the human's messages, the tool cards and the approvals, and none of them the answer.
var hookEvents = []struct {
	event, turn, interaction string
	subtypeField, subtypeMap string
	capture                  bool
}{
	{"UserPromptSubmit", "active", "none", "", "", true},
	{"PreToolUse", "active", "none", "", "", true},
	{"PostToolUse", "active", "none", "", "", true},
	{"Notification", "idle", "permission", "notification_type", "permission_prompt=permission;idle_prompt=none;idle=none;permission=permission;prompt=prompt", false},
	{"Stop", "idle", "none", "", "", true},
	{"SubagentStart", "active", "none", "", "", false},
	{"SubagentStop", "", "none", "", "", false},
	{"PermissionRequest", "idle", "permission", "", "", true},
}

// claudeAdapter is the stateless Claude Code strategy object. It carries no state,
// so it is shared by value and is safe across goroutines.
type claudeAdapter struct{}

// New builds the Claude Code adapter. Returning the concrete stateless value lets
// callers discover its optional extensions by type assertion while it continues
// to satisfy Adapter everywhere the frozen interface is required.
func New() claudeAdapter { return claudeAdapter{} }

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
		// Editable free text; left/right cycle these aliases the claude CLI accepts.
		// Empty value = the CLI's own default model.
		{Key: "model", Label: "Model", Type: "string", Suggest: []string{"sonnet", "opus", "haiku"}},
		{Key: "dangerously-skip-permissions", Label: "Skip permission prompts", Type: "bool", Default: "false"},
	}
}

// SignalSources declares Claude Code's six hook events with their status mapping,
// plus the claude grid signature as the T-3 fallback (ADR-007). The heuristic
// Descriptor names the "claude" signature; the engine interprets it (the adapter
// stays I/O-free, ADR-001).
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
		if h.capture {
			desc[adapter.DescriptorCapture] = adapter.CaptureRaw
		}
		sources = append(sources, adapter.SignalSource{Kind: "hook", Descriptor: desc})
	}
	sources = append(sources, adapter.SignalSource{
		Kind:       "heuristic",
		Descriptor: map[string]string{"grid": "claude"},
	})
	return sources
}

// Resume composes a hook-enabled `claude --resume <id>` invocation; an empty id
// resumes nothing. Resumed sessions need the same per-invocation hooks as fresh
// sessions so swarm remains their status and lifecycle observer.
func (claudeAdapter) Resume(spec adapter.ResumeSpec) ([]string, error) {
	if spec.ConversationID == "" {
		return nil, nil
	}
	settings, err := hookSettingsJSON()
	if err != nil {
		return nil, err
	}
	argv := []string{binary, "--settings", settings}
	argv = append(argv, optionFlags(spec.Options)...)
	argv = append(argv, "--resume", spec.ConversationID)
	return argv, nil
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

// ConversationIDFromEvent reads Claude's canonical top-level session_id from an
// authenticated hook body. The assembly authenticates the callback before this
// pure parser is invoked.
func (claudeAdapter) ConversationIDFromEvent(p adapter.HookPayload) (string, bool) {
	return adapter.CanonicalTopLevelConversationID(p.Raw, "session_id")
}

// ProjectDirName makes the claude adapter an adapter.TranscriptLayout: Claude Code
// files a cwd's transcripts under ~/.claude/projects/<encoded cwd>/, and this is
// that encoding. Every character that is not an ASCII letter or digit becomes one
// '-'; nothing else changes, and the value is not cleaned (the caller cleans its
// own cwd, as resolveClaude does).
//
// THE ENCODER IS RUNE-WISE, AND THAT IS MEASURED, NOT ASSUMED. Running the real
// `claude` CLI on 2026-08-26 with cwd "/Users/Nathan/.claude/jobs/20bd7184/tmp/
// café.tëst/测试" produced the directory
// "-Users-Nathan--claude-jobs-20bd7184-tmp-caf--t-st---": the two-byte 'é' and each
// three-byte ideograph yielded exactly ONE dash. A byte-wise loop would have
// written "caf---t--st" and seven trailing dashes, so ranging over runes here is
// the behavior the CLI actually has rather than the one Go makes convenient.
func (claudeAdapter) ProjectDirName(cwd string) string {
	var out strings.Builder
	for _, r := range cwd {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			out.WriteRune(r)
		} else {
			out.WriteByte('-')
		}
	}
	return out.String()
}

// TranscriptFileName is the layout's second half: within a project directory,
// Claude Code names each conversation's transcript "<sessionId>.jsonl" (observed
// 2026-08-26, "f41b0e35-6fa4-4c8b-bfea-8687b311255b.jsonl", whose stem is exactly
// the record's own sessionId).
//
// THE DIRECTORY IS NOT FLAT -- that same capture found a `memory` entry beside the
// transcript -- so a resolver must name this file EXACTLY and never glob.
//
// It does not validate convID, deliberately: this is a naming rule, not a gate.
// The caller owes adapter.IsCanonicalConversationID before it and an os.Root
// anchor after it, because filepath.Join CLEANS and an id like "../../etc/passwd"
// would otherwise resolve clean outside the projects root.
func (claudeAdapter) TranscriptFileName(convID string) string {
	return convID + transcriptExt
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
//
// `model` is free-form launch-form text and is the only option whose VALUE
// becomes an argv token, so it is the only place an operator can smuggle a flag
// into a supervised argv — `--remote-control` above all, which would hand the
// session's approvals to Anthropic's relay and race swarm's PermissionRequest
// hook (agents-tracker-n047, ADR-010 section 5). No model alias starts with '-',
// so a flag-shaped value is dropped and the CLI's own default model is used —
// the same outcome as an empty value, and consistent with how a
// dangerously-skip-permissions value other than "true" is ignored below.
//
// ponytail: this closes the option-derived tokens only. The positional
// InitialPrompt is still appended unseparated in Command, so a prompt that
// begins with '-' is parsed as a flag; closing that needs a `--` separator whose
// acceptance must be confirmed against the live CLI. Out of scope here and no
// threat under the frozen model (B133: the phone is trusted and the operator
// types their own prompt) — recorded in docs/verification/a1b-rc-scrub.md.
func optionFlags(opts map[string]string) []string {
	var flags []string
	if m := opts["model"]; m != "" && !strings.HasPrefix(m, "-") {
		flags = append(flags, "--model", m)
	}
	if opts["dangerously-skip-permissions"] == "true" {
		flags = append(flags, "--dangerously-skip-permissions")
	}
	return flags
}

// sessionIDFrom accepts a canonical id only from a complete, line-anchored
// "Session " record. A value running to EOF with no terminator is a transcript
// read mid-write and is retried after the line is complete. Multiple conflicting
// records or marker-shaped prose fail closed.
func sessionIDFrom(s string) (string, bool) {
	if len(s) > 1<<20 {
		return "", false
	}
	var found string
	for _, line := range strings.SplitAfter(s, "\n") {
		if !strings.Contains(line, sessionMarker) {
			continue
		}
		left := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(left, sessionMarker) {
			return "", false
		}
		rest := left[len(sessionMarker):]
		end := strings.IndexAny(rest, " \t\r\n")
		if end < 0 {
			return "", false
		}
		id := rest[:end]
		if !adapter.IsCanonicalConversationID(id) || strings.TrimSpace(rest[end:]) != "" {
			return "", false
		}
		if found != "" && found != id {
			return "", false
		}
		found = id
	}
	return found, found != ""
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
