package tui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type claudeControllerClient struct {
	*fakeClient
	sends   int
	sendErr error
	history schema.NativeHistoryPage
	receipt protocol.SemanticMessageReceipt
	reads   int
	readErr error
}

func (c *claudeControllerClient) SemanticSend(string, protocol.ComposerSendReq) error {
	c.sends++
	return c.sendErr
}
func (c *claudeControllerClient) SemanticOperation(string, string) (protocol.SemanticMessageReceipt, error) {
	return c.receipt, nil
}
func (c *claudeControllerClient) NativeInteractionHistory(schema.InteractionHistoryReq) (schema.NativeHistoryPage, error) {
	c.reads++
	return c.history, c.readErr
}
func (c *claudeControllerClient) EndpointID() string { return "endpoint" }

func claudeController(t *testing.T) (rootModel, *claudeControllerClient) {
	t.Helper()
	c := &claudeControllerClient{fakeClient: newFakeClient()}
	m := New(c, nil).(rootModel)
	ctx, cancel := context.WithCancel(m.connections.ctx)
	m.width, m.height = testCols, testRows
	m.screen = screenChat
	m.chatGeneration = 1
	m.chat = claudeChatModel{generation: 1, ctx: ctx, cancel: cancel, ready: true, session: protocol.SessionView{ID: "endpoint/chat", EndpointID: "endpoint", Agent: "fake", RequiredRuntime: "claude-chat", Capabilities: &protocol.SessionCapabilities{Provider: "fake", SessionInstance: "fixture-instance", NativeInteractionVersion: 1, NativeComposer: true}}, send: m.connections.track(c), control: m.connections.track(&claudeControllerClient{fakeClient: newFakeClient()}), view: newChatView(nil, nil, "ready", "", true), readUntil: time.Now().Add(time.Second)}
	t.Cleanup(func() { _ = m.Close() })
	return m, c
}

func TestClaudeChatEscapeRevokesQueuedSendSynchronously(t *testing.T) {
	m, c := claudeController(t)
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	queued, cmd := next.Update(claudeChatActionMsg{generation: 1, intent: chatSendIntent{Text: "must remain unsent"}})
	if cmd != nil {
		_ = cmd()
	}
	if c.sends != 0 || queued.(rootModel).screen == screenChat {
		t.Fatal("queued send retained authority after Escape", c.sends)
	}
}

func TestClaudeChatControllerProjectsHistoryIntoScrollableView(t *testing.T) {
	m, _ := claudeController(t)
	m.height = 5
	text := strings.Repeat("long fixture row\n", 20)
	item, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}})
	next, _, _ := m.updateClaudeChatMessage(claudeChatReadMsg{generation: 1, session: m.chat.session, page: schema.NativeHistoryPage{Version: 1, Scope: strings.Repeat("a", 64), Floor: true, Items: []json.RawMessage{item}}})
	before := next.(rootModel)
	after, _ := before.updateClaudeChat(tea.KeyPressMsg{Code: tea.KeyUp})
	if len(after.(rootModel).chat.view.items) != 1 || after.(rootModel).chat.view.top <= 0 {
		t.Fatal("controller history is not navigable")
	}
}

func TestClaudeChatPrewriteRejectionRetainsExactDraft(t *testing.T) {
	m, c := claudeController(t)
	c.sendErr = protocol.ErrSemanticSendRejected
	const draft = "exact Unicode 🐝\nsecond line"
	m.chat.view.draft = newLineEditor(draft)
	next, cmd, _ := m.updateClaudeChatMessage(claudeChatActionMsg{generation: 1, intent: chatSendIntent{Text: draft}})
	if cmd == nil {
		t.Fatal("no explicit send")
	}
	next, _, _ = next.(rootModel).updateClaudeChatMessage(cmd())
	state := next.(rootModel).chat
	if state.view.draft.text != draft || state.pendingText != draft || state.receiptState != "rejected" || c.sends != 1 {
		t.Fatal("refusal lost or replayed original draft")
	}
}

func TestClaudeChatUncertainWriteReconcilesOnlyByReadAndExactEcho(t *testing.T) {
	m, c := claudeController(t)
	c.sendErr = errors.New("lost write acknowledgment")
	const text = "retain until exact echo"
	m.chat.view.draft = newLineEditor(text)
	next, send, _ := m.updateClaudeChatMessage(claudeChatActionMsg{generation: 1, intent: chatSendIntent{Text: text}})
	if send == nil {
		t.Fatal("missing explicit send")
	}
	next, read, _ := next.(rootModel).updateClaudeChatMessage(send())
	if read == nil {
		t.Fatal("uncertain send abandoned read-only reconciliation")
	}
	state := next.(rootModel)
	c.sessions = []protocol.SessionView{state.chat.session}
	c.history = schema.NativeHistoryPage{Version: 1, Scope: strings.Repeat("a", 64), Floor: true}
	c.receipt = protocol.SemanticMessageReceipt{OperationID: state.chat.operation, Session: state.chat.session.ID, SessionInstance: "fixture-instance", State: "uncertain"}
	next, poll, _ := state.updateClaudeChatMessage(read())
	if poll == nil || next.(rootModel).chat.view.draft.text != text {
		t.Fatal("unknown receipt lost draft or stopped read reconciliation")
	}
	c.receipt.State = "admitted"
	c.receipt.NativeMessageID = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
	c.receipt.NativeSessionID = "1f700d8f-d3e3-42c1-9ab0-dcdb22f27772"
	row, _ := json.Marshal(map[string]any{"type": "user", "uuid": c.receipt.NativeMessageID, "sessionId": c.receipt.NativeSessionID, "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}})
	c.history.Items = []json.RawMessage{row}
	next, read, _ = next.(rootModel).updateClaudeChatMessage(poll())
	if read == nil {
		t.Fatal("read-only continuation missing")
	}
	next, _, _ = next.(rootModel).updateClaudeChatMessage(read())
	if next.(rootModel).chat.view.draft.text != "" || c.sends != 1 || c.reads != 2 {
		t.Fatal("exact own echo failed to admit or message was resent")
	}
}

func TestClaudeChatMissingDirectDialerNeverUsesBoardReconnector(t *testing.T) {
	m, _ := claudeController(t)
	calls := 0
	m.reconnector = func() (Client, error) { calls++; return newFakeClient(), nil }
	next, cmd := m.openClaudeChat(m.chat.session)
	if cmd != nil || calls != 0 || next.(rootModel).chat.ready {
		t.Fatal("chat used automatic board dial instead of direct owner dial")
	}
}

func TestClaudeChatInitialBusyReadRefreshesWithoutSending(t *testing.T) {
	m, c := claudeController(t)
	m.chat.ready = false
	c.sessions = []protocol.SessionView{m.chat.session}
	c.history = schema.NativeHistoryPage{Version: 1, Scope: strings.Repeat("a", 64), Floor: true}
	c.readErr = errors.New("primary read publication busy")
	next, poll, _ := m.updateClaudeChatMessage(claudeChatReadCmd(m.chat)())
	if poll == nil || next.(rootModel).chat.ready {
		t.Fatal("initial busy read did not remain read-only with bounded refresh")
	}
	c.readErr = nil
	next, read, _ := next.(rootModel).updateClaudeChatMessage(poll())
	if read == nil {
		t.Fatal("read refresh missing")
	}
	next, _, _ = next.(rootModel).updateClaudeChatMessage(read())
	if !next.(rootModel).chat.ready || c.sends != 0 {
		t.Fatal("fresh read did not enable explicit composer or submitted automatically")
	}
}

func TestClaudeChatReadRejectsInvalidCapabilityAndScopeChange(t *testing.T) {
	for _, kind := range []string{"capability", "scope"} {
		t.Run(kind, func(t *testing.T) {
			m, c := claudeController(t)
			c.sessions = []protocol.SessionView{m.chat.session}
			c.history = schema.NativeHistoryPage{Version: 1, Scope: strings.Repeat("a", 64), Floor: true}
			if kind == "capability" {
				bad := *m.chat.session.Capabilities
				bad.Provider = "claude"
				c.fakeClient.sessions[0].Capabilities = &bad
			} else {
				m.chat.page.Scope = strings.Repeat("b", 64)
			}
			result := claudeChatReadCmd(m.chat)().(claudeChatReadMsg)
			if result.err == nil {
				t.Fatal("invalid read authority accepted", kind)
			}
			next, poll, _ := m.updateClaudeChatMessage(result)
			if next.(rootModel).chat.ready || poll != nil || c.sends != 0 {
				t.Fatal("invalid authority enabled/retried composer")
			}
		})
	}
}

func TestClaudeChatPairingModalOwnsPaste(t *testing.T) {
	m, c := claudeController(t)
	m.chat.view.draft = newLineEditor("preserve draft")
	next, _ := m.Update(pairPendingMsg{SAS: sasFixture, DeviceName: "fixture peer"})
	next, _ = next.Update(tea.PasteMsg{Content: "must not enter chat"})
	if next.(rootModel).chat.view.draft.text != "preserve draft" || c.sends != 0 {
		t.Fatal("pairing modal leaked clipboard into chat")
	}
}

func TestClaudeChatPairingModalRevokesQueuedSend(t *testing.T) {
	m, c := claudeController(t)
	const draft = "preserve exact 🐝\nqueued draft"
	m.chat.view.draft = newLineEditor(draft)
	next, queued := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if queued == nil {
		t.Fatal("Enter did not queue a send intent")
	}
	next, _ = next.Update(pairPendingMsg{SAS: sasFixture, DeviceName: "fixture peer"})
	next, send := next.Update(queued())
	if send != nil {
		_ = send()
	}
	state := next.(rootModel).chat
	if c.sends != 0 || state.view.draft.text != draft || state.operation != "" {
		t.Fatal("queued send retained authority under pairing modal", c.sends)
	}
}

func TestClaudeChatEscapeReopenRetainsOnlySameScopedDraft(t *testing.T) {
	for _, scope := range []string{"same", "replacement", "different", "unknown-instance"} {
		t.Run(scope, func(t *testing.T) {
			m, _ := claudeController(t)
			m.chat.view.draft = newLineEditor("keep exact 🐝\nunsent text")
			target := m.chat.session
			capabilities := *target.Capabilities
			target.Capabilities = &capabilities
			switch scope {
			case "replacement":
				capabilities.SessionInstance = "new-instance"
			case "different":
				target.ID = "endpoint/other"
			case "unknown-instance":
				capabilities.SessionInstance = ""
			}
			next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
			next, _ = next.(rootModel).openClaudeChat(target)
			got := next.(rootModel).chat.view.draft.text
			if scope == "same" && got != "keep exact 🐝\nunsent text" {
				t.Fatal("same-incarnation navigation lost draft")
			}
			if scope != "same" && got != "" {
				t.Fatal("draft crossed session incarnation", scope)
			}
		})
	}
}

func TestClaudeChatViewModeNeverUsesRawAttach(t *testing.T) {
	s := sWorking("endpoint/chat", "fake", "~/fixture", "fixture chat", time.Minute)
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	row["required_runtime"] = json.RawMessage(`"claude-chat"`)
	raw, _ = json.Marshal(row)
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	f := newFakeClient(s)
	r := &recordingRunner{}
	m := New(f, nil, WithAttachRunner(r.run))
	defer func() { _ = m.(interface{ Close() error }).Close() }()
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	m = runAttachCmd(m)
	if calls := r.recorded(); len(calls) != 0 {
		t.Fatalf("chat runtime routed into raw attach: %+v", calls)
	}
	if got := m.(rootModel).screen; got == screenAttach {
		t.Fatal("chat runtime opened the terminal placeholder")
	}
}
