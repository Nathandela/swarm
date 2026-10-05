package skeleton

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestAccountCoverageCountsVisibleDiscussionsAndKeepsConflictingWriters(t *testing.T) {
	old := persist.Meta{ID: "old", AgentType: "codex", ConversationID: "conversation-a", CreatedAt: time.Unix(1, 0)}
	resumed := old
	resumed.ID, resumed.ResumedFrom, resumed.CreatedAt = "resumed", "old", time.Unix(2, 0)
	resumed.AccountBinding = &accounts.Binding{Provider: "codex"}
	running := persist.Meta{ID: "running", AgentType: "claude", Status: status.Status{Process: status.ProcessRunning}}
	conflict := running
	conflict.ID, conflict.ResumedFrom = "conflict", "running"
	archived := persist.Meta{ID: "archived", AgentType: "codex", RosterHidden: true}
	other := persist.Meta{ID: "other", AgentType: "shell"}
	got := accountCoverage([]persist.Meta{old, resumed, running, conflict, archived, other})
	if got["codex"].Managed != 1 || got["codex"].Unmanaged != 0 {
		t.Fatalf("resume history counted as another discussion: %+v", got["codex"])
	}
	if got["claude"].Unmanaged != 2 || got["claude"].RunningUnmanaged != 2 {
		t.Fatalf("conflicting native writers concealed: %+v", got["claude"])
	}
	if len(got) != 2 {
		t.Fatalf("unsupported provider included: %+v", got)
	}
}
