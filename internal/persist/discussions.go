package persist

import (
	"sort"

	"github.com/Nathandela/swarm/internal/status"
)

// ProjectDiscussions returns visible resume attempts and the local IDs each
// supersedes. It never deletes history. Only explicit resume lineage is grouped;
// handoffs and independent launches remain separate. Conflicting live attempts
// stay visible so an extra actor is never concealed.
func ProjectDiscussions(metas []Meta) ([]Meta, map[string][]string) {
	linked := false
	for _, m := range metas {
		linked = linked || m.ResumedFrom != "" || m.RosterHidden
	}
	if !linked {
		return metas, nil
	}
	agents := make(map[string]string, len(metas))
	for _, m := range metas {
		agents[m.ID] = m.AgentType
	}
	parents := make(map[string]string)
	var root func(string) string
	root = func(id string) string {
		p, ok := parents[id]
		if !ok {
			parents[id] = id
			return id
		}
		if p != id {
			parents[id] = root(p)
		}
		return parents[id]
	}
	key := func(m Meta, id string) string { return m.AgentType + "\x00" + id }
	identities := make(map[string]string)
	for _, m := range metas {
		identities[root(key(m, m.ID))] = m.ConversationID
	}
	for _, m := range metas {
		a := root(key(m, m.ID))
		if agent, exists := agents[m.ResumedFrom]; m.ResumedFrom != "" && (!exists || agent == m.AgentType) {
			b := root(key(m, m.ResumedFrom))
			// A resume that reached another conversation is a failed recovery, not
			// authority to hide its source. Check component identities too: empty
			// or missing parents cannot bridge two different known conversations.
			if identities[a] != "" && identities[b] != "" && identities[a] != identities[b] {
				continue
			}
			parents[a] = b
			if identities[b] == "" {
				identities[b] = identities[a]
			}
		}
	}
	members := make(map[string][]Meta)
	for _, m := range metas {
		r := root(key(m, m.ID))
		members[r] = append(members[r], m)
	}
	nodes := make(map[string][]string)
	for node := range parents {
		r := root(node)
		nodes[r] = append(nodes[r], node)
	}
	visible := make(map[string]bool)
	supersedes := make(map[string][]string)
	for r, group := range members {
		var winners []Meta
		var latest *Meta
		for _, m := range group {
			if m.Status.Process == status.ProcessRunning {
				winners = append(winners, m)
			}
			if !m.RosterHidden && (latest == nil || m.CreatedAt.After(latest.CreatedAt) || (m.CreatedAt.Equal(latest.CreatedAt) && m.ID > latest.ID)) {
				copy := m
				latest = &copy
			}
		}
		if len(winners) == 0 && latest != nil {
			winners = append(winners, *latest)
		}
		for _, m := range winners {
			visible[m.ID] = true
		}
		// Missing ancestors may still be displayed by an already connected client.
		var hidden []string
		for _, node := range nodes[r] {
			id := node[len(group[0].AgentType)+1:]
			if !visible[id] {
				hidden = append(hidden, id)
			}
		}
		sort.Strings(hidden)
		for _, m := range winners {
			supersedes[m.ID] = hidden
		}
	}
	out := make([]Meta, 0, len(visible))
	for _, m := range metas {
		if visible[m.ID] {
			out = append(out, m)
		}
	}
	return out, supersedes
}
