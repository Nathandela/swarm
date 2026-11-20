package tui

import (
	"reflect"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/voice"
)

// The NUL keeps the internal section distinct from user-supplied tags and repo names.
const dispatcherSection = "\x00discord-dispatcher"

func isDiscordManager(s protocol.SessionView) bool {
	return s.Role == "manager" && strings.HasPrefix(s.ManagerConversationID, voice.DiscussionConversationPrefix)
}

func WithLinkedDispatchers(list func() ([]protocol.SessionView, error), attach AttachRunner) Option {
	return func(m *rootModel) { m.dispatcherList = list; m.dispatcherAttach = attach }
}

type dispatcherRows []protocol.SessionView
type dispatcherPoll struct{}

func (m rootModel) loadDispatchers() tea.Msg {
	rows, err := m.dispatcherList()
	if err != nil {
		return dispatcherRows(nil)
	}
	parents := make(map[string]bool)
	for _, s := range rows {
		if isDiscordManager(s) {
			parents[s.ID] = true
			for _, prior := range s.Supersedes {
				parents[prior] = true
			}
		}
	}
	var members dispatcherRows
	for _, s := range protocol.VisibleDiscussions(rows) {
		parent := protocol.NamespacedID(s.EndpointID, s.SpawnedFrom)
		if isDiscordManager(s) || s.SpawnedFrom != "" && parents[parent] {
			members = append(members, s)
		}
	}
	return members
}
func (m rootModel) isLinkedDispatcher(s protocol.SessionView) bool {
	return m.general.linkedDispatcherIDs[s.ID]
}
func (m rootModel) applyDispatcherRows(rows dispatcherRows) (tea.Model, tea.Cmd) {
	tick := tea.Tick(3*time.Second, func(time.Time) tea.Msg { return dispatcherPoll{} })
	if reflect.DeepEqual(m.linkedDispatchers, []protocol.SessionView(rows)) {
		return m, tick
	}
	firstAtTop := len(m.linkedDispatchers) == 0 && m.general.top == 0
	var local []protocol.SessionView
	for _, s := range m.general.sessions {
		if !m.isLinkedDispatcher(s) {
			local = append(local, s)
		}
	}
	m.linkedDispatchers = rows
	m.general.linkedDispatcherIDs = make(map[string]bool, len(rows))
	for _, s := range rows {
		m.general.linkedDispatcherIDs[s.ID] = true
	}
	banner := m.reconcileRoster(local)
	if firstAtTop {
		m.general.top = 0
	}
	return m, tea.Batch(tick, banner)
}
