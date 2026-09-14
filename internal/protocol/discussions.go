package protocol

import (
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func namespaceSuperseded(endpoint string, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = NamespacedID(endpoint, id)
	}
	return out
}

func stampSuperseded(view *SessionView, ids []string, successor string) *SessionView {
	view.Supersedes = namespaceSuperseded(view.EndpointID, ids)
	if successor != "" {
		view.SupersededBy = NamespacedID(view.EndpointID, successor)
	}
	return view
}

// Preserve actual event identity for session-scoped consumers such as swarm wait.
// Roster clients use the annotations to avoid resurrecting historical attempts.
func (s *Server) projectEvent(m *persist.Meta) ([]string, string) {
	metas := s.d.List()
	found := false
	for _, current := range metas {
		if current.ID == m.ID {
			m.RosterHidden = current.RosterHidden && current.Status.Process != status.ProcessRunning
			found = true
			break
		}
	}
	if !found {
		// An old recovery may have deleted the source. Its buffered running event
		// must not invent a second live actor beside the current replacement.
		visible, hidden := persist.ProjectDiscussions(metas)
		for _, candidate := range visible {
			for _, id := range hidden[candidate.ID] {
				if id == m.ID {
					return nil, candidate.ID
				}
			}
		}
		metas = append(metas, *m)
	}
	visible, hidden := persist.ProjectDiscussions(metas)
	for _, candidate := range visible {
		if candidate.ID == m.ID {
			return hidden[m.ID], ""
		}
	}
	for _, candidate := range visible {
		for _, id := range hidden[candidate.ID] {
			if id == m.ID {
				return nil, candidate.ID
			}
		}
	}
	return nil, ""
}

// VisibleDiscussions filters an annotated raw roster for display. List itself
// retains historical IDs for callers targeting a specific attempt (watch/handoff).
func VisibleDiscussions(rows []SessionView) []SessionView {
	out := make([]SessionView, 0, len(rows))
	for _, row := range rows {
		if !row.RosterHidden && row.SupersededBy == "" {
			out = append(out, row)
		}
	}
	return out
}

func discussionSuccessors(hidden map[string][]string) map[string]string {
	by := make(map[string]string)
	for successor, ids := range hidden {
		for _, id := range ids {
			if by[id] == "" || successor < by[id] {
				by[id] = successor
			}
		}
	}
	return by
}
