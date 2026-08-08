package persist

import "strings"

// Normative launch-environment allowlist (E1.8, ADR-004 item 6). The launch
// environment is allowlist-filtered before it is persisted into meta.json, so a
// session's captured env cannot immortalize every secret in the launching shell
// (S-2). This list is the contract and deliberately lives with the code.
//
// Allowed:
//   - PATH, HOME, SHELL, TERM      — process/terminal basics
//   - LANG, LANGUAGE, LC_*         — locale family (LC_ALL, LC_CTYPE, ...)
//   - VIRTUAL_ENV, CONDA_PREFIX,
//     CONDA_DEFAULT_ENV            — Python venv / conda context
//   - ANTHROPIC_API_KEY,
//     OPENAI_API_KEY               — provider credentials the v1 agent CLIs
//     (Claude Code, Codex) need to run
//
// Provider credentials are matched by exact name, not a loose *_API_KEY glob, so
// an unrelated secret such as AWS_SECRET_ACCESS_KEY can never slip through.
// Everything else is dropped, including injection vectors (LD_PRELOAD,
// DYLD_INSERT_LIBRARIES) and unrelated secrets.
var envAllowExact = map[string]bool{
	"PATH":              true,
	"HOME":              true,
	"SHELL":             true,
	"TERM":              true,
	"LANG":              true,
	"LANGUAGE":          true,
	"VIRTUAL_ENV":       true,
	"CONDA_PREFIX":      true,
	"CONDA_DEFAULT_ENV": true,
	"ANTHROPIC_API_KEY": true,
	"OPENAI_API_KEY":    true,
}

// envAllowKey reports whether an env variable name is on the normative allowlist.
func envAllowKey(key string) bool {
	return envAllowExact[key] || strings.HasPrefix(key, "LC_")
}

// remoteControlEnvPrefixes is the launch-environment DENYLIST: a variable whose
// NAME starts with one of these is stripped from a supervised agent's
// environment even if the allowlist above would admit it, and even when it was
// injected DOWNSTREAM of FilterEnv.
//
// Claude Code ships a first-party Remote Control that swarm never offers on a
// session it supervises. A supervised session that inherited it from the
// launching shell would route its approvals to Anthropic's relay behind swarm's
// back and race swarm's own PermissionRequest hook answer (agents-tracker-n047,
// ADR-010 section 5), so no variable in this family has a legitimate value here.
//
// The two prefixes are the family as the SHIPPED binary spells it, not a guess:
// every CLAUDE_*REMOTE* name in `strings` over Claude Code 2.1.224 that belongs
// to the remote-control/remote-worker surface falls under one of them —
// CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX, CLAUDE_REMOTE_WORKFLOW_{SCRIPT,ARGS},
// CLAUDE_CODE_REMOTE and its CLAUDE_CODE_REMOTE_* session/settings group. They
// are prefixes rather than that fixed list so a sibling shipped later is covered
// on the day it appears. The allowlist already drops all of them today; this is
// the floor that still holds if the allowlist ever widens.
var remoteControlEnvPrefixes = []string{
	"CLAUDE_REMOTE_",     // CLAUDE_REMOTE_CONTROL_*, CLAUDE_REMOTE_WORKFLOW_*
	"CLAUDE_CODE_REMOTE", // CLAUDE_CODE_REMOTE itself and its CLAUDE_CODE_REMOTE_* group
}

// ScrubRemoteControl returns env without any remote-control variable, in input
// order. It is applied at the last gate before exec (internal/shim), so it holds
// regardless of what composed the environment upstream.
func ScrubRemoteControl(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		// The variable name leads the entry, so a prefix test on the whole
		// KEY=VALUE is anchored to the NAME — a value that happens to contain or
		// start with the text is untouched.
		if !hasAnyPrefix(kv, remoteControlEnvPrefixes) {
			out = append(out, kv)
		}
	}
	return out
}

// hasAnyPrefix reports whether s starts with any of the prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// FilterEnv returns the allowlisted subset of env in input order, each entry
// passed through verbatim (the whole KEY=VALUE, including values that themselves
// contain '='). The variable name is the text before the first '='.
func FilterEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if envAllowKey(key) {
			out = append(out, kv)
		}
	}
	return out
}
