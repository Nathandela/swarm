package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/protocol"
)

// DaemonReconnector restores the client connection without changing agent lifecycle.
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
	owned      *ownedClient
}
type rosterLoadedMsg struct {
	generation uint64
	from       <-chan protocol.Event
	sessions   []protocol.SessionView
	revision   uint64
}

func reconnectDelay(generation uint64, attempt int) tea.Cmd {
	delay := time.Second << min(attempt, 3)
	return tea.Tick(delay, func(time.Time) tea.Msg { return daemonReconnectTickMsg{generation: generation} })
}
func (m rootModel) retryDaemonConnection() (tea.Model, tea.Cmd) {
	if m.reconnector == nil {
		return m, m.general.setBanner("reconnect unavailable - restart swarm")
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
	if !m.connectionLost || !m.reconnecting || m.reconnectDialing || m.reconnector == nil || msg.generation != m.reconnectGeneration {
		return m, nil
	}
	r, generation, lifetime := m.reconnector, m.reconnectGeneration, m.connections
	m.reconnectDialing = true
	return m, func() tea.Msg { c, err := r(); return prepareDaemonConnection(lifetime, generation, c, err) }
}
func prepareDaemonConnection(lifetime *clientLifetime, generation uint64, c Client, err error) daemonReconnectedMsg {
	result := daemonReconnectedMsg{generation: generation, client: c, err: err, owned: lifetime.track(c)}
	if result.err == nil && c == nil {
		result.err = errors.New("daemon returned no client")
	}
	if result.err == nil && result.owned.ctx.Err() != nil {
		result.err = result.owned.ctx.Err()
	}
	if result.err == nil {
		events, subErr := c.Subscribe()
		if subErr != nil {
			result.err = fmt.Errorf("event stream failed: %w", subErr)
		}
		if subErr == nil && events == nil {
			result.err = errors.New("daemon returned no subscription")
		}
		if result.err == nil {
			result.events = rosterInvalidations(result.owned.ctx, events)
			result.sessions = reconnectRoster(c)
			if result.sessions == nil {
				result.err = errors.New("daemon roster unavailable")
			}
		}
	}
	if result.err == nil && result.owned.ctx.Err() != nil {
		result.err = result.owned.ctx.Err()
	}
	if result.err != nil {
		lifetime.discard(result.owned)
		result.client = nil
	}
	return result
}

// Unversioned events invalidate Lists; replaying them could regress a newer snapshot.
func rosterInvalidations(ctx context.Context, events <-chan protocol.Event) <-chan protocol.Event {
	dirty := make(chan protocol.Event, 1)
	go func() {
		defer close(dirty)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				if !ok {
					return
				}
				select {
				case dirty <- protocol.Event{}:
				default:
				}
			}
		}
	}()
	return dirty
}

// Nil means a failed/timed-out read; a nonnil empty slice is authoritative.
func reconnectRoster(c Client) []protocol.SessionView {
	type result struct {
		sessions []protocol.SessionView
		err      error
	}
	done := make(chan result, 1)
	go func() { sessions, err := c.List(); done <- result{sessions, err} }()
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
		if msg.owned != nil {
			m.connections.discard(msg.owned)
		} else {
			closeReconnectClient(msg.client)
		}
		return m, nil
	}
	m.reconnectDialing = false
	if msg.err == nil && (msg.client == nil || msg.events == nil || msg.sessions == nil) {
		msg.err = errors.New("incomplete daemon recovery")
		if msg.owned != nil {
			m.connections.discard(msg.owned)
		} else {
			closeReconnectClient(msg.client)
		}
	}
	if msg.err != nil || msg.client == nil {
		m.reconnectAttempts = min(m.reconnectAttempts+1, 3)
		return m, reconnectDelay(m.reconnectGeneration, m.reconnectAttempts)
	}
	if msg.owned == nil {
		msg.owned = m.connections.track(msg.client)
	}
	if !m.connections.adopt(msg.owned) {
		return m, nil
	}
	m.client = msg.client
	m.events = msg.events
	m.connectionLost = false
	m.reconnecting = false
	m.rosterInvalidations = true
	m.rosterRefreshing = false
	m.rosterDirty = false
	m.invalidateOptionsConnection()
	m.accountsClientGeneration++
	m.accountsGeneration++
	m.accounts.generation = m.accountsGeneration
	m.accounts.busy = false
	if bv, ok := msg.client.(interface{ BuildVersion() string }); ok {
		m.daemonVersion = bv.BuildVersion()
	}
	banner := m.reconcileRoster(msg.sessions)
	cmds := []tea.Cmd{waitForEvent(m.events), banner}
	if m.screen == screenGeneral && !m.ticking {
		m.ticking = true
		cmds = append(cmds, repaintTick())
	}
	cmds = append(cmds, m.armWorkingAnimation())
	if m.screen == screenOptions || m.screen == screenAccounts {
		old := m.options.contextGuard
		m.optionsGeneration++
		fresh, load := newOptionsModel(m.options.grouping, m.options.ordering, m.client, m.optionsGeneration)
		if old.dirty() {
			fresh.contextGuard.reconnectDraft = &contextGuardDraft{compact: old.autoCompact, threshold: old.threshold}
		} else {
			fresh.contextGuard.reconnectDraft = old.reconnectDraft
		}
		m.options.contextGuard = fresh.contextGuard
		if !fresh.contextGuard.available && (m.options.focus == optionsFocusAutoCompact || m.options.focus == optionsFocusThreshold) {
			m.options.focus = optionsFocusGroup
		}
		cmds = append(cmds, load)
	}
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
func (m *rootModel) reconcileRoster(sessions []protocol.SessionView) tea.Cmd {
	selected := m.general.selectedID()
	visible := protocol.VisibleDiscussions(sessions)
	var banners []tea.Cmd
	for _, session := range visible {
		banners = append(banners, m.general.apply(session))
	}
	m.general.sessions = visible
	m.general.refreshLayout(selected)
	return tea.Batch(banners...)
}
func (m rootModel) refreshRoster() (tea.Model, tea.Cmd) {
	m.rosterDirty = true
	if m.rosterRefreshing || m.connectionLost {
		return m, nil
	}
	m.rosterRefreshing = true
	m.rosterDirty = false
	c, generation, events, revision := m.client, m.reconnectGeneration, m.events, m.rosterRevision
	return m, func() tea.Msg {
		return rosterLoadedMsg{generation: generation, from: events, sessions: reconnectRoster(c), revision: revision}
	}
}
func (m rootModel) applyRoster(msg rosterLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.reconnectGeneration || msg.from != m.events || m.connectionLost {
		return m, nil
	}
	m.rosterRefreshing = false
	if msg.revision != m.rosterRevision {
		return m.refreshRoster()
	}
	if msg.sessions == nil {
		return m.Update(connectionLostMsg{from: m.events})
	}
	banner := m.reconcileRoster(msg.sessions)
	if m.rosterDirty {
		next, refresh := m.refreshRoster()
		return next, tea.Batch(banner, refresh)
	}
	return m, tea.Batch(banner, m.armWorkingAnimation())
}
