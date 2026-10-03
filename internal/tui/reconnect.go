package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/protocol"
)

// DaemonReconnector dials the owner daemon without restarting it. It is distinct
// from the upgrade restarter so reconnecting never changes daemon lifecycle.
type DaemonReconnector func() (Client, error)

func WithDaemonReconnector(r DaemonReconnector) Option {
	return func(m *rootModel) { m.reconnector = r }
}

type daemonReconnectTickMsg struct{ generation uint64 }
type daemonReconnectedMsg struct {
	generation uint64
	client     Client
	events     <-chan protocol.Event
	sessions   []protocol.SessionView
	err        error
}

func reconnectDelay(generation uint64, attempt int) tea.Cmd {
	delay := time.Second << min(attempt, 3)
	return tea.Tick(delay, func(time.Time) tea.Msg { return daemonReconnectTickMsg{generation: generation} })
}

func (m rootModel) retryDaemonConnection() (tea.Model, tea.Cmd) {
	if m.reconnector == nil {
		m.accounts.notice = "Reconnect is unavailable in this client. Restart Swarm to reconnect; sign-in workers may continue."
		return m, nil
	}
	if m.reconnecting {
		return m, nil
	}
	m.reconnectGeneration++
	m.reconnectAttempts = 0
	m.reconnecting = true
	return m.reconnectDaemon(daemonReconnectTickMsg{generation: m.reconnectGeneration})
}

func (m rootModel) reconnectDaemon(msg daemonReconnectTickMsg) (tea.Model, tea.Cmd) {
	if !m.connectionLost || !m.reconnecting || m.reconnector == nil || msg.generation != m.reconnectGeneration {
		return m, nil
	}
	r, generation := m.reconnector, m.reconnectGeneration
	return m, func() tea.Msg {
		c, err := r()
		result := daemonReconnectedMsg{generation: generation, client: c, err: err}
		if err != nil || c == nil {
			return result
		}
		result.events, result.err = c.Subscribe()
		if result.err == nil {
			result.sessions = reconnectRoster(c)
		}
		if result.err != nil {
			closeReconnectClient(c)
			result.client = nil
		}
		return result
	}
}

// A nonnil empty slice is an authoritative empty roster. Nil means a failed
// or timed-out read; reconnecting must keep the last known rows in that case.
func reconnectRoster(c Client) []protocol.SessionView {
	type result struct {
		sessions []protocol.SessionView
		err      error
	}
	done := make(chan result, 1)
	go func() { sessions, err := c.List(); done <- result{sessions: sessions, err: err} }()
	select {
	case loaded := <-done:
		if loaded.err != nil {
			return nil
		}
		if loaded.sessions == nil {
			return []protocol.SessionView{}
		}
		return loaded.sessions
	case <-time.After(listDialTimeout):
		return nil
	}
}

func closeReconnectClient(c Client) {
	if closer, ok := c.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func (m rootModel) applyDaemonReconnected(msg daemonReconnectedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.reconnectGeneration || !m.connectionLost || !m.reconnecting {
		if msg.client != nil {
			closeReconnectClient(msg.client)
		}
		return m, nil
	}
	if msg.err != nil || msg.client == nil {
		m.reconnectAttempts++
		if m.reconnectAttempts >= 6 {
			m.reconnecting = false
			m.accounts.notice = "Daemon is still unavailable. Press r to reconnect; sign-in workers may continue."
			return m, nil
		}
		return m, reconnectDelay(m.reconnectGeneration, m.reconnectAttempts)
	}
	m.client = msg.client
	m.events = msg.events
	m.connectionLost = false
	m.reconnecting = false
	m.accountsClientGeneration++
	m.accountsGeneration++
	m.accounts.generation = m.accountsGeneration
	m.accounts.busy = false
	if bv, ok := msg.client.(interface{ BuildVersion() string }); ok {
		m.daemonVersion = bv.BuildVersion()
	}
	// Reconcile the complete successful snapshot while retaining the board's
	// layout, selected identity and pending editor/confirmation buffers.
	if msg.sessions != nil {
		selected := m.general.selectedID()
		visible := protocol.VisibleDiscussions(msg.sessions)
		present := make(map[string]bool, len(visible))
		for _, session := range visible {
			present[session.ID] = true
		}
		for _, session := range m.general.sessions {
			if !present[session.ID] {
				m.general.tombstone(session.ID)
			}
		}
		m.general.sessions = visible
		m.general.refreshLayout(selected)
	}
	cmds := []tea.Cmd{waitForEvent(m.events)}
	if msg.sessions == nil {
		cmds = append(cmds, m.general.setBanner("daemon reconnected; roster refresh unavailable"))
	}
	if m.screen == screenGeneral && !m.ticking {
		m.ticking = true
		cmds = append(cmds, repaintTick())
	}
	cmds = append(cmds, m.armWorkingAnimation())
	if m.screen == screenAccounts {
		m.accounts.available = false
		if c, ok := msg.client.(accountsClient); ok {
			for _, cap := range c.Capabilities() {
				if cap == protocol.CapAccountsManage {
					m.accounts.available = true
				}
			}
		}
		updated, cmd := m.beginAccountsRequest(protocol.AccountsReq{Action: "list"})
		m = updated.(rootModel)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}
