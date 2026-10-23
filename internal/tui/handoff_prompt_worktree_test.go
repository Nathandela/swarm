package tui

// FAILING-FIRST for ADR-010 Amendment 10: the receiving agent starts in a new git
// worktree at the source's current commit, so the source must be told that only
// committed work travels -- before it runs `swarm handoff`, while it can still act on it.

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestHandoffPromptTellsTheSourceToCommitBeforeLaunching(t *testing.T) {
	for _, mode := range []string{protocol.SupervisionPassive, protocol.SupervisionManual, protocol.SupervisionNone} {
		got, err := renderHandoffPrompt("codex", "", mode)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		launch := strings.Index(got, `child_id="$(swarm handoff`)
		note := strings.Index(got, "new git worktree")
		if note < 0 {
			t.Fatalf("%s prompt never says the receiving agent starts in a new git worktree:\n%s", mode, got)
		}
		if note > launch {
			t.Fatalf("%s prompt explains the worktree only after the launch command; the source must commit first:\n%s", mode, got)
		}
		if !strings.Contains(strings.ToLower(got[:launch]), "commit") {
			t.Fatalf("%s prompt does not ask the source to commit before launching:\n%s", mode, got)
		}
	}
}
