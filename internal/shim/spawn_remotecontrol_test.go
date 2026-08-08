package shim

// bd agents-tracker-e8nn (code half) — a supervised agent must never inherit
// ambient Remote Control. The n047 investigation (recorded in ADR-010 section 5
// and spike-SC.md's closing note) found spike probes displaying a live
// "remote control" status bar inherited from the job's own environment. A
// supervised session that did the same would route its approvals to Anthropic's
// relay behind swarm's back and race swarm's own PermissionRequest hook answer.
//
// cfg.Env reaching exec is the LAST gate: the daemon's launch-env allowlist
// (persist.FilterEnv, ADR-004 item 6 / ADR-007 D8) already drops these variables
// upstream, so this test pins the floor that survives an allowlist widening, a
// post-filter injection, or a hand-written shim-launch.json.

import (
	"strings"
	"testing"
)

// rcDeniedEnv is the Remote Control variable family, taken from the shipped
// binary rather than guessed: every CLAUDE_*REMOTE* name in `strings` over
// Claude Code 2.1.224 that belongs to the remote-control/remote-worker surface,
// plus one invented CLAUDE_REMOTE_CONTROL_* sibling so the fence is proven to be
// a family prefix and not a fixed name list.
var rcDeniedEnv = []string{
	"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=SWARM",
	"CLAUDE_REMOTE_CONTROL_FUTURE_KNOB=1", // invented sibling: the prefix must cover it
	"CLAUDE_REMOTE_WORKFLOW_SCRIPT=/tmp/w.sh",
	"CLAUDE_REMOTE_WORKFLOW_ARGS=--go",
	"CLAUDE_CODE_REMOTE=1",
	"CLAUDE_CODE_REMOTE_SESSION_ID=abc",
	"CLAUDE_CODE_REMOTE_SETTINGS_PATH=/tmp/s.json",
}

// rcAllowedEnv are the narrowness controls: entries that must survive untouched,
// so the fence is a name-anchored family prefix rather than a substring sweep
// that would quietly strip unrelated variables (or match on a VALUE).
var rcAllowedEnv = []string{
	"CLAUDE_CODE_ENTRYPOINT=cli",              // a CLAUDE_ variable outside the family
	"SWARM_CLAUDE_REMOTE_CONTROL_NOTE=1",      // contains the text, but is not a CLAUDE_ name
	"EDITOR=CLAUDE_REMOTE_CONTROL_IS_A_VALUE", // the text in a VALUE, never a name
}

// TestSpawn_EnvScrubsRemoteControl — no remote-control variable in cfg.Env
// reaches the spawned agent, and nothing outside that family is disturbed.
func TestSpawn_EnvScrubsRemoteControl(t *testing.T) {
	cfg := helperConfig(t, modeInfo, nil, append(append([]string(nil), rcDeniedEnv...), rcAllowedEnv...))
	_, _, env := runInfo(t, cfg)

	got := map[string]bool{}
	for _, kv := range env {
		got[kv] = true
	}
	for _, kv := range rcDeniedEnv {
		if got[kv] {
			t.Errorf("agent env carries %q — a supervised session must not inherit ambient Remote Control (e8nn)", kv)
		}
	}
	for _, kv := range rcAllowedEnv {
		if !got[kv] {
			t.Errorf("agent env lost %q — the scrub must be a name-anchored remote-control prefix, not a substring sweep\nenv:\n%s",
				kv, strings.Join(env, "\n"))
		}
	}
}
