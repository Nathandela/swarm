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
