package skeleton

import (
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// runningConversation prevents two actors from resuming different generations of
// the same provider thread. Empty identities never group unrelated conversations.
func runningConversation(roster []persist.Meta, source persist.Meta) (persist.Meta, bool) {
	visible, supersedes := persist.ProjectDiscussions(roster)
	var found persist.Meta
	for _, m := range visible {
		if m.AgentType != source.AgentType || m.Status.Process != status.ProcessRunning {
			continue
		}
		if source.ConversationID != "" && m.ConversationID != source.ConversationID {
			continue
		}
		related := m.ID == source.ID
		for _, id := range supersedes[m.ID] {
			related = related || id == source.ID
		}
		if !related {
			continue
		}
		if found.ID == "" || m.CreatedAt.After(found.CreatedAt) || (m.CreatedAt.Equal(found.CreatedAt) && m.ID > found.ID) {
			found = m
		}
	}
	return found, found.ID != ""
}

// authRecoveryReady proves a current backend subscribed to the resumed thread.
// Metadata is seeded before exec, so neither it nor a PID proves recovery.
// This is transport readiness, not proof of a successful authenticated model turn.
func (d *Daemon) authRecoveryReady(m persist.Meta) bool {
	if m.Status.Process != status.ProcessRunning || m.ConversationID == "" {
		return false
	}
	instance, ok := d.sessionInstance(m.ID)
	if !ok {
		return false
	}
	d.backend.mu.Lock()
	defer d.backend.mu.Unlock()
	b := d.backend.live[m.ID]
	return b != nil && b.sessionInstance == instance && b.threadID == m.ConversationID && b.subscribed && b.feed != nil && !b.feed.retired.Load()
}
