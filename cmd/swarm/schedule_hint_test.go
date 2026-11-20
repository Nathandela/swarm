package main

import (
	"testing"

	"github.com/Nathandela/swarm/internal/schedule"
)

func TestGitHubNetworkHint(t *testing.T) {
	for _, tc := range []struct {
		prompt, agent, authority string
		want                     bool
	}{
		{"Check Dependabot updates", "codex", "workspace-write", true},
		{"Invoke `gh pr list`", "codex", "observe", true},
		{"Review this paragraph", "codex", "observe", false},
		{"Review GitHub PRs", "claude", "agent-default", false},
		{"Read GitHub issues", "codex", "agent-default", false},
	} {
		spec := schedule.JobSpec{Prompt: tc.prompt, Agents: []string{tc.agent}, Authority: tc.authority}
		if got := githubNetworkHint(spec); got != tc.want {
			t.Errorf("%q, %s, %s: got %v", tc.prompt, tc.agent, tc.authority, got)
		}
	}
}
