package tui

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type claudeChatClient interface {
	Client
	SemanticSend(string, protocol.ComposerSendReq) error
	SemanticOperation(string, string) (protocol.SemanticMessageReceipt, error)
	NativeInteractionHistory(schema.InteractionHistoryReq) (schema.NativeHistoryPage, error)
}

var errClaudeChatScope = errors.New("fixture chat scope changed")

type claudeChatModel struct {
	session                                      protocol.SessionView
	generation                                   uint64
	ctx                                          context.Context
	cancel                                       context.CancelFunc
	send, control                                *ownedClient
	view                                         chatView
	page                                         schema.NativeHistoryPage
	operation, pendingText, receiptState, notice string
	busy, ready, stopped, stopPending            bool
	readUntil                                    time.Time
}

func WithClaudeChatDialer(d DaemonReconnector) Option { return func(m *rootModel) { m.chatDialer = d } }

type claudeChatReadConnectionMsg struct {
	generation uint64
	client     *ownedClient
	err        error
}

type claudeChatReadyMsg struct {
	generation    uint64
	send, control *ownedClient
	err           error
}
type claudeChatReadMsg struct {
	generation uint64
	page       schema.NativeHistoryPage
	session    protocol.SessionView
	receipt    protocol.SemanticMessageReceipt
	err        error
}
type claudeChatSentMsg struct {
	generation uint64
	err        error
}
type claudeChatStoppedMsg struct {
	generation uint64
	err        error
}
type claudeChatPollMsg struct{ generation uint64 }
type claudeChatActionMsg struct {
	generation uint64
	intent     tea.Msg
}

func (m rootModel) openClaudeChat(session protocol.SessionView) (tea.Model, tea.Cmd) {
	previous := m.chat
	sameScope := previous.session.ID == session.ID && previous.session.Capabilities != nil && session.Capabilities != nil && previous.session.Capabilities.Validate() == nil && session.Capabilities.Validate() == nil && session.Capabilities.SessionInstance != "" && previous.session.Capabilities.SessionInstance == session.Capabilities.SessionInstance
	m.closeClaudeChat()
	m.chatGeneration++
	ctx, cancel := context.WithCancel(m.connections.ctx)
	m.chat = claudeChatModel{session: session, generation: m.chatGeneration, ctx: ctx, cancel: cancel, notice: "Connecting to fixture chat", readUntil: time.Now().Add(5 * time.Second)}
	m.chat.view = newChatView(nil, nil, m.chat.notice, "", false).resize(m.width, m.height)
	if sameScope {
		m.chat.view = previous.view.resize(m.width, m.height)
		m.chat.page = previous.page
		m.chat.operation, m.chat.pendingText, m.chat.receiptState = previous.operation, previous.pendingText, previous.receiptState
		m.chat.stopped, m.chat.stopPending = previous.stopped, previous.stopPending
	}
	m.screen = screenChat
	if m.chatDialer == nil {
		m.chat.notice = "Chat connection unavailable; raw attach is refused"
		return m, nil
	}
	r, lifetime, generation := m.chatDialer, m.connections, m.chatGeneration
	return m, func() tea.Msg {
		var send, control *ownedClient
		closeBoth := func() { lifetime.discard(send); lifetime.discard(control) }
		c, err := r()
		if c != nil {
			send = lifetime.track(c)
		}
		if err == nil && send == nil {
			err = errors.New("message connection unavailable")
		}
		if err == nil && ctx.Err() == nil {
			c, err = r()
			if c != nil {
				control = lifetime.track(c)
			}
		}
		if err == nil && control == nil {
			err = errors.New("stop connection unavailable")
		}
		if err == nil {
			if _, ok := send.Client.(claudeChatClient); !ok {
				err = errors.New("message connection lacks fixture chat contract")
			}
		}
		if err == nil {
			for _, owned := range []*ownedClient{send, control} {
				endpoint, ok := owned.Client.(interface{ EndpointID() string })
				if !ok || endpoint.EndpointID() != session.EndpointID {
					err = errClaudeChatScope
					break
				}
			}
		}
		if err != nil || ctx.Err() != nil {
			closeBoth()
			if err == nil {
				err = ctx.Err()
			}
			return claudeChatReadyMsg{generation: generation, err: err}
		}
		return claudeChatReadyMsg{generation: generation, send: send, control: control}
	}
}

func (m *rootModel) closeClaudeChat() {
	if m.chat.cancel != nil {
		m.chat.cancel()
	}
	if m.connections != nil {
		m.connections.discard(m.chat.send)
		m.connections.discard(m.chat.control)
	}
}

func claudeChatReadCmd(chat claudeChatModel) tea.Cmd {
	return func() tea.Msg {
		result := claudeChatReadMsg{generation: chat.generation}
		if chat.ctx.Err() != nil {
			result.err = chat.ctx.Err()
			return result
		}
		client, ok := chat.send.Client.(claudeChatClient)
		if !ok {
			result.err = errors.New("chat connection unavailable")
			return result
		}
		result.page, result.err = client.NativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: chat.session.ID, Limit: 200})
		if result.err != nil {
			return result
		}
		if chat.page.Scope != "" && result.page.Scope != chat.page.Scope {
			result.err = errClaudeChatScope
			return result
		}
		rows, err := client.List()
		if err != nil {
			result.err = err
			return result
		}
		found := false
		for _, row := range rows {
			if row.ID == chat.session.ID {
				result.session = row
				found = true
				break
			}
		}
		if !found || result.session.RequiredRuntime != "claude-chat" || result.session.Agent != "fake" || result.page.Version != 1 {
			result.err = errClaudeChatScope
			return result
		}
		if result.session.Capabilities == nil || result.session.Capabilities.NativeInteractionVersion != 1 {
			result.err = errors.New("fixture history not ready")
			return result
		}
		if result.session.Capabilities.Validate() != nil {
			result.err = errClaudeChatScope
			return result
		}
		if chat.session.Capabilities != nil && chat.session.Capabilities.SessionInstance != result.session.Capabilities.SessionInstance {
			result.err = errClaudeChatScope
			return result
		}
		if chat.operation != "" {
			result.receipt, result.err = client.SemanticOperation(chat.session.ID, chat.operation)
		}
		return result
	}
}

func claudeChatPoll(chat claudeChatModel) tea.Cmd {
	generation := chat.generation
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return claudeChatPollMsg{generation} })
}

func (m rootModel) updateClaudeChatMessage(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch v := msg.(type) {
	case claudeChatReadConnectionMsg:
		if m.screen != screenChat || v.generation != m.chat.generation || m.chat.stopped || m.chat.stopPending || m.chat.ctx.Err() != nil {
			m.connections.discard(v.client)
			return m, nil, true
		}
		if v.err != nil {
			m.chat.notice = "Read connection unconfirmed: " + v.err.Error()
			return m, nil, true
		}
		m.connections.discard(m.chat.send)
		m.chat.send = v.client
		return m, claudeChatReadCmd(m.chat), true

	case claudeChatReadyMsg:
		if m.screen != screenChat || v.generation != m.chat.generation {
			m.connections.discard(v.send)
			m.connections.discard(v.control)
			return m, nil, true
		}
		if v.err != nil {
			m.chat.notice = v.err.Error()
			return m, nil, true
		}
		m.chat.send, m.chat.control = v.send, v.control
		m.chat.notice = "Loading fixture history"
		return m, m.readClaudeChat(), true
	case claudeChatReadMsg:
		if m.screen != screenChat || v.generation != m.chat.generation {
			return m, nil, true
		}
		if v.err != nil {
			m.chat.ready = false
			m.chat.notice = v.err.Error()
			if !errors.Is(v.err, errClaudeChatScope) && time.Now().Before(m.chat.readUntil) && m.chat.ctx.Err() == nil && !m.chat.stopped {
				return m, claudeChatPoll(m.chat), true
			}
			return m, nil, true
		}
		m.chat.session, m.chat.page = v.session, v.page
		m.chat.projectHistory()
		m.chat.ready = v.session.Capabilities.NativeComposer && !m.chat.stopped && !m.chat.stopPending && m.chat.operation == ""
		m.chat.notice = "Dev fixture; workflow custody remains incomplete · ctrl+x Stop · esc back"
		if m.chat.operation != "" {
			m.chat.receiptState = v.receipt.State
			if (v.receipt.State == "admitted" || v.receipt.State == "completed") && fixtureEchoVisible(v.page, v.receipt, m.chat.pendingText) && m.chat.view.draft.text == m.chat.pendingText {
				m.chat.view.draft.set("")
			}
			if v.receipt.State == "completed" || v.receipt.State == "rejected" {
				m.chat.busy = false
				return m, nil, true
			}
			if time.Now().Before(m.chat.readUntil) {
				return m, claudeChatPoll(m.chat), true
			}
			m.chat.notice = "Receipt pending; no resend · ctrl+x Stop"
		}
		return m, nil, true
	case claudeChatSentMsg:
		if m.screen != screenChat || v.generation != m.chat.generation {
			return m, nil, true
		}
		if v.err != nil {
			m.chat.busy = false
			m.chat.ready = false
			m.chat.receiptState = "uncertain"
			if errors.Is(v.err, protocol.ErrSemanticSendRejected) {
				m.chat.receiptState = "rejected"
			}
			m.chat.notice = v.err.Error()
			if m.chat.receiptState == "uncertain" {
				m.chat.readUntil = time.Now().Add(5 * time.Second)
				return m, m.readClaudeChat(), true
			}
			return m, nil, true
		}
		m.chat.readUntil = time.Now().Add(5 * time.Second)
		return m, m.readClaudeChat(), true
	case claudeChatStoppedMsg:
		if m.screen != screenChat || v.generation != m.chat.generation {
			return m, nil, true
		}
		m.chat.ready = false
		if v.err != nil {
			m.chat.notice = "Stop unconfirmed: " + v.err.Error()
		} else {
			m.chat.stopped = true
			m.chat.notice = "Stop requested; custody remains incomplete"
			m.connections.discard(m.chat.send)
		}
		return m, nil, true
	case claudeChatPollMsg:
		if m.screen == screenChat && v.generation == m.chat.generation && !m.chat.stopped && !m.chat.stopPending && m.chat.send != nil && m.chat.ctx.Err() == nil {
			return m, m.readClaudeChat(), true
		}
		return m, nil, true
	case claudeChatActionMsg:
		if m.screen != screenChat || v.generation != m.chat.generation {
			return m, nil, true
		}
		switch intent := v.intent.(type) {
		case chatBackIntent:
			m.closeClaudeChat()
			cmd := m.enterGeneral()
			return m, cmd, true
		case chatSendIntent:
			if m.pairing != nil || !m.chat.ready || m.chat.busy || m.chat.stopPending || len(intent.Text) > 16<<10 {
				return m, nil, true
			}
			op, err := protocol.NewSemanticOperationID()
			if err != nil {
				m.chat.notice = err.Error()
				return m, nil, true
			}
			m.chat.operation, m.chat.pendingText, m.chat.busy, m.chat.ready, m.chat.receiptState = op, intent.Text, true, false, "prepared"
			client := m.chat.send.Client.(claudeChatClient)
			generation := m.chat.generation
			req := protocol.ComposerSendReq{Session: m.chat.session.ID, SessionInstance: m.chat.session.Capabilities.SessionInstance, Text: intent.Text}
			return m, func() tea.Msg { return claudeChatSentMsg{generation: generation, err: client.SemanticSend(op, req)} }, true
		}
		return m, nil, true
	}
	return m, nil, false
}

func (m rootModel) updateClaudeChat(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.Code == tea.KeyEsc && key.Mod == 0 {
		m.closeClaudeChat()
		m.chatGeneration++
		cmd := m.enterGeneral()
		return m, cmd
	}

	if k, ok := msg.(tea.KeyPressMsg); ok && k.Code == 'x' && k.Mod == tea.ModCtrl {
		if m.chat.control == nil {
			m.chat.notice = "Stop connection unavailable; custody is unconfirmed"
			return m, nil
		}
		m.chat.ready = false
		m.chat.stopPending = true
		client, id, generation := m.chat.control, m.chat.session.ID, m.chat.generation
		return m, func() tea.Msg { return claudeChatStoppedMsg{generation: generation, err: client.Kill(id)} }
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && k.Code != tea.KeyEsc && k.Code != tea.KeyUp && k.Code != tea.KeyDown && (m.chat.busy || m.chat.operation != "") {
		return m, nil
	}
	if _, ok := msg.(tea.PasteMsg); ok && (m.chat.busy || m.chat.operation != "") {
		return m, nil
	}
	m.chat.view = m.chat.view.resize(m.width, m.height)
	m.chat.view.allowed = m.chat.ready && !m.chat.busy && m.chat.operation == ""
	var cmd tea.Cmd
	m.chat.view, cmd = m.chat.view.update(msg)
	if cmd == nil {
		return m, nil
	}
	generation := m.chat.generation
	return m, func() tea.Msg { return claudeChatActionMsg{generation: generation, intent: cmd()} }
}

func fixtureEchoVisible(page schema.NativeHistoryPage, receipt protocol.SemanticMessageReceipt, text string) bool {
	for _, raw := range page.Items {
		var row struct {
			Type, UUID string
			Session    string `json:"sessionId"`
			Message    struct {
				Role    string
				Content []struct{ Type, Text string }
			}
		}
		if json.Unmarshal(raw, &row) == nil && row.Type == "user" && row.UUID == receipt.NativeMessageID && row.Session == receipt.NativeSessionID && row.Message.Role == "user" && len(row.Message.Content) == 1 && row.Message.Content[0].Type == "text" && row.Message.Content[0].Text == text {
			return true
		}
	}
	return false
}

func (m *claudeChatModel) projectHistory() {
	var items []json.RawMessage
	for _, raw := range m.page.Items {
		var row struct {
			Type    string
			Message struct {
				Role    string
				Content []struct{ Type, Text string }
			}
		}
		if json.Unmarshal(raw, &row) == nil && (row.Type == "user" || row.Type == "assistant") && len(row.Message.Content) == 1 && row.Message.Content[0].Type == "text" {
			item, _ := json.Marshal(map[string]any{"kind": "message_snapshot", "role": row.Message.Role, "text": row.Message.Content[0].Text})
			items = append(items, item)
		}
	}
	m.view.items = items
	m.view = m.view.clampTop()
}

func (m claudeChatModel) rendered(width, height int) string {
	view := m.view
	view.status = m.notice
	view.allowed = m.ready && !m.busy
	if m.receiptState != "" {
		view.status = m.receiptState + " · " + m.notice
	}
	return view.view(width, height)
}

func (m rootModel) readClaudeChat() tea.Cmd {
	if closed, ok := m.chat.send.Client.(interface{ Closed() bool }); !ok || !closed.Closed() {
		return claudeChatReadCmd(m.chat)
	}
	if m.chatDialer == nil {
		return func() tea.Msg {
			return claudeChatReadMsg{generation: m.chat.generation, err: errors.New("read connection closed; receipt remains unconfirmed")}
		}
	}
	dial, lifetime, chat := m.chatDialer, m.connections, m.chat
	return func() tea.Msg {
		c, err := dial()
		var owned *ownedClient
		if c != nil {
			owned = lifetime.track(c)
		}
		if err == nil && owned == nil {
			err = errors.New("read connection unavailable")
		}
		if err == nil {
			endpoint, ok := owned.Client.(interface{ EndpointID() string })
			if !ok || endpoint.EndpointID() != chat.session.EndpointID {
				err = errClaudeChatScope
			}
			if _, ok := owned.Client.(claudeChatClient); !ok {
				err = errClaudeChatScope
			}
		}
		if err != nil || chat.ctx.Err() != nil {
			lifetime.discard(owned)
			if err == nil {
				err = chat.ctx.Err()
			}
			return claudeChatReadConnectionMsg{generation: chat.generation, err: err}
		}
		return claudeChatReadConnectionMsg{generation: chat.generation, client: owned}
	}
}
