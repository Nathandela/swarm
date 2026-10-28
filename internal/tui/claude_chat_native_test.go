package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
)

// Existing controller/owned connections; these tests execute no native process.
func nativeClaudeController(t *testing.T) (rootModel, *claudeControllerClient) {
	t.Helper()
	m, c := claudeController(t)
	m.chat.session.Agent = "claude"
	m.chat.session.Capabilities = &protocol.SessionCapabilities{Provider: "claude", ProviderVersion: "2.1.310", SessionInstance: "native-instance", NativeInteractionVersion: 2, NativeComposer: true}
	c.sessions = []protocol.SessionView{m.chat.session}
	c.history = schema.NativeHistoryPage{Version: 2, Scope: strings.Repeat("a", 64), Floor: true}
	return m, c
}

func nativeClaudeUIRows(t *testing.T, uuid, text string) []json.RawMessage {
	t.Helper()
	rows := []any{
		map[string]any{"type": "user", "uuid": uuid, "sessionId": "1f700d8f-d3e3-42c1-9ab0-dcdb22f27772", "parentUuid": nil, "isSidechain": false, "message": map[string]any{"role": "user", "content": text}},
		map[string]any{"type": "assistant", "uuid": "79b79d77-e62d-44b9-8e24-94e908f08935", "sessionId": "1f700d8f-d3e3-42c1-9ab0-dcdb22f27772", "parentUuid": uuid, "isSidechain": false, "message": map[string]any{"id": "msg_native_different_id", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "native first block"}, map[string]any{"type": "thinking", "thinking": "not chat text"}, map[string]any{"type": "text", "text": "native last block"}}}},
	}
	var out []json.RawMessage
	for _, row := range rows {
		raw, e := json.Marshal(row)
		if e != nil {
			t.Fatal(e)
		}
		out = append(out, raw)
	}
	return out
}

func TestClaudeNativeChatUIReadsExactNativeV2AndAllTextBlocks(t *testing.T) {
	m, c := nativeClaudeController(t)
	c.history.Items = nativeClaudeUIRows(t, "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", "owner Unicode 🐝\nsecond line")
	result := claudeChatReadCmd(m.chat)().(claudeChatReadMsg)
	if result.err != nil {
		t.Fatalf("native Claude v2 read refused: %v", result.err)
	}
	next, _, _ := m.updateClaudeChatMessage(result)
	chat := next.(rootModel).chat
	raw, _ := json.Marshal(chat.view.items)
	for _, want := range []string{"owner Unicode", "second line", "native first block", "native last block"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("native history dropped %q: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), "not chat text") || c.sends != 0 || !chat.ready {
		t.Fatal("read leaked thinking, sent input, or lost explicit readiness")
	}
}

func TestClaudeNativeChatUIEchoRequiresExactUUIDAndStringText(t *testing.T) {
	for _, kind := range []string{"exact", "wrong-uuid", "wrong-text", "wrong-session"} {
		t.Run(kind, func(t *testing.T) {
			m, c := nativeClaudeController(t)
			const text = "retain until own native echo 🐝"
			const uuid = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
			m.chat.operation = "native-op"
			m.chat.pendingText = text
			m.chat.busy = true
			m.chat.ready = false
			m.chat.view.draft = newLineEditor(text)
			echoed, body := uuid, text
			if kind == "wrong-uuid" {
				echoed = "2389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
			}
			if kind == "wrong-text" {
				body = "other"
			}
			c.history.Items = nativeClaudeUIRows(t, echoed, body)
			if kind == "wrong-session" {
				c.history.Items[0] = json.RawMessage(strings.Replace(string(c.history.Items[0]), "1f700d8f", "2f700d8f", 1))
			}
			receipt := protocol.SemanticMessageReceipt{OperationID: m.chat.operation, Session: m.chat.session.ID, SessionInstance: "native-instance", State: "admitted", Agent: "claude", NativeMessageID: uuid, NativeSessionID: "1f700d8f-d3e3-42c1-9ab0-dcdb22f27772"}
			next, _, _ := m.updateClaudeChatMessage(claudeChatReadMsg{generation: 1, page: c.history, session: m.chat.session, receipt: receipt})
			draft := next.(rootModel).chat.view.draft.text
			if (kind == "exact" && draft != "") || (kind != "exact" && draft != text) || c.sends != 0 {
				t.Fatalf("echo gate changed/replayed draft: kind=%s draft=%q sends=%d", kind, draft, c.sends)
			}
		})
	}
}

func TestClaudeNativeChatUIBlockedHumanRetainsHistoryAndDedicatedStop(t *testing.T) {
	m, c := nativeClaudeController(t)
	m.chat.session.Capabilities.NativeComposer = false
	m.chat.operation = "native-human-op"
	m.chat.busy = true
	m.chat.ready = false
	m.chat.pendingText = "owner"
	c.history.Items = nativeClaudeUIRows(t, "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", "owner")
	receipt := protocol.SemanticMessageReceipt{OperationID: m.chat.operation, Session: m.chat.session.ID, SessionInstance: "native-instance", State: "admitted", Reason: "Unsupported native human request: AskUserQuestion; Stop available"}
	next, _, _ := m.updateClaudeChatMessage(claudeChatReadMsg{generation: 1, page: c.history, session: m.chat.session, receipt: receipt})
	blocked := next.(rootModel)
	if blocked.chat.ready || !strings.Contains(blocked.chat.notice, "AskUserQuestion") || len(blocked.chat.view.items) == 0 {
		t.Fatal("human wait hid history/request or enabled composer")
	}
	_, send, _ := blocked.updateClaudeChatMessage(claudeChatActionMsg{generation: 1, intent: chatSendIntent{Text: "must not answer"}})
	if send != nil || c.sends != 0 {
		t.Fatal("unsupported human request was answered")
	}
	_, stop := blocked.updateClaudeChat(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if stop == nil {
		t.Fatal("human wait disabled dedicated Stop")
	}
	if _, ok := stop().(claudeChatStoppedMsg); !ok {
		t.Fatal("Stop did not use existing controller result")
	}
}

func TestClaudeNativeChatUIRefusesScopeAndVersionDowngrade(t *testing.T) {
	for _, kind := range []string{"instance", "provider", "read-version", "runtime", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			m, c := nativeClaudeController(t)
			changed := m.chat.session
			cap := *changed.Capabilities
			changed.Capabilities = &cap
			switch kind {
			case "instance":
				cap.SessionInstance = "replacement"
			case "provider":
				cap.Provider = "fake"
			case "read-version":
				c.history.Version = 1
			case "runtime":
				changed.RequiredRuntime = ""
			case "terminal":
				cap.TerminalFallback = true
			}
			c.sessions = []protocol.SessionView{changed}
			if result := claudeChatReadCmd(m.chat)().(claudeChatReadMsg); result.err == nil {
				t.Fatalf("native read accepted %s", kind)
			}
			if c.sends != 0 {
				t.Fatal("scope refusal wrote input")
			}
		})
	}
}
