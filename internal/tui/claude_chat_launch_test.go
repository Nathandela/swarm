package tui

import (
	"errors"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"strings"
	"testing"
	"time"
)

func TestClaudeChatNativeLaunchUsesRefreshedRuntimeInsteadOfSyntheticAttach(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "readable-blocked", true: "composer-ready"}[ready], func(t *testing.T) {
			m, c := nativeClaudeController(t)
			row := m.chat.session
			row.Status.Process = status.ProcessRunning
			row.Capabilities.NativeComposer = ready
			c.sessions = []protocol.SessionView{row}
			c.launchID = row.ID
			runner := &recordingRunner{}
			m.attachRunner = runner.run
			m.screen = screenGeneral
			req := protocol.LaunchReq{Agent: "claude", Cwd: t.TempDir(), Options: map[string]string{"claude_interface": "swarm-chat"}}
			result := launchCmd(c, req)()
			next, cmd := m.Update(result)
			state := next.(rootModel)
			if state.screen != screenChat || state.chat.session.RequiredRuntime != "claude-chat" || state.chat.session.Capabilities == nil || state.chat.session.Capabilities.NativeInteractionVersion != 2 || state.chat.session.ID != row.ID {
				t.Fatal("launch routed from synthetic metadata instead of exact refreshed native row")
			}
			if len(runner.recorded()) != 0 || c.sends != 0 {
				t.Fatal("chat launch sent raw attach or automatic owner message")
			}
			_ = cmd // Connecting/read commands are covered by existing controller tests.
		})
	}
}

func TestClaudeChatNativeLaunchMissingOrUnsafeRefreshNeverFallsBackToRawAttach(t *testing.T) {
	for _, kind := range []string{"missing-row", "other-row", "list-error", "wrong-runtime", "wrong-provider", "wrong-version", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			m, c := nativeClaudeController(t)
			row := m.chat.session
			row.Status.Process = status.ProcessRunning
			c.launchID = row.ID
			switch kind {
			case "missing-row":
				c.sessions = nil
			case "other-row":
				row.ID = "endpoint/other-session"
			case "list-error":
				c.listErr = errors.New("roster unavailable")
			case "wrong-runtime":
				row.RequiredRuntime = ""
			case "wrong-provider":
				row.Capabilities.Provider = "fake"
			case "wrong-version":
				row.Capabilities.NativeInteractionVersion = 1
			case "terminal":
				row.Capabilities.TerminalFallback = true
			}
			if kind != "missing-row" {
				c.sessions = []protocol.SessionView{row}
			}
			runner := &recordingRunner{}
			m.attachRunner = runner.run
			m.screen = screenGeneral
			next, cmd := m.Update(launchCmd(c, protocol.LaunchReq{Agent: "claude", Cwd: t.TempDir(), Options: map[string]string{"claude_interface": "swarm-chat"}})())
			execCmd(cmd)
			if next.(rootModel).screen == screenAttach {
				t.Fatalf("unproved chat launch %s selected raw attach", kind)
			}
			if len(runner.recorded()) != 0 || c.sends != 0 {
				t.Fatal("failed refresh emitted input")
			}
		})
	}
}

func TestClaudeChatNativeReadReboundCapabilityInstanceRetainsDraftAndNoInput(t *testing.T) {
	m, c := nativeClaudeController(t)
	m.chat.view.draft = newLineEditor("keep exact draft")
	row := m.chat.session
	proof := *row.Capabilities
	proof.SessionInstance = "replacement-instance"
	row.Capabilities = &proof
	c.sessions = []protocol.SessionView{row}
	runner := &recordingRunner{}
	m.attachRunner = runner.run
	result := claudeChatReadCmd(m.chat)()
	next, cmd, _ := m.updateClaudeChatMessage(result)
	execCmd(cmd)
	state := next.(rootModel)
	if state.chat.ready || state.chat.view.draft.text != "keep exact draft" || c.sends != 0 || len(runner.recorded()) != 0 {
		t.Fatal("rebound capability cleared draft or enabled input/attach")
	}
}

func TestClaudeChatNativeLaunchRetainsInitialDraftUntilExactHistoryEcho(t *testing.T) {
	m, c := nativeClaudeController(t)
	c.launchID = m.chat.session.ID
	c.sessions = []protocol.SessionView{m.chat.session}
	m.screen = screenGeneral
	const text = "keep initial caller draft 🐝"
	next, _ := m.Update(launchCmd(c, protocol.LaunchReq{Agent: "claude", Options: map[string]string{"claude_interface": "swarm-chat"}, InitialPrompt: text})())
	chat := next.(rootModel).chat
	if chat.view.draft.text != text || chat.pendingText != text || chat.operation != "claude-initial:chat" || chat.ready || c.sends != 0 {
		t.Fatal("launch lost/replayed initial draft or enabled a duplicate send")
	}
}

func TestClaudeChatNativeReconnectUsesSameScopedRefreshedProofAndNewConnections(t *testing.T) {
	for _, kind := range []string{"same", "rebound", "missing"} {
		t.Run(kind, func(t *testing.T) {
			m, c := nativeClaudeController(t)
			m.chat.view.draft = newLineEditor("retained draft")
			m.chat.operation = "retained-operation"
			m.chat.pendingText = "retained draft"
			row := m.chat.session
			proof := *row.Capabilities
			row.Capabilities = &proof
			if kind == "rebound" {
				proof.SessionInstance = "replacement"
			}
			rows := []protocol.SessionView{row}
			if kind == "missing" {
				rows = []protocol.SessionView{}
			}
			fresh := &claudeControllerClient{fakeClient: newFakeClient(rows...)}
			m.connectionLost = true
			m.reconnecting = true
			m.reconnectGeneration++
			m.chatDialer = func() (Client, error) { return fresh, nil }
			next, _ := m.applyDaemonReconnected(daemonReconnectedMsg{generation: m.reconnectGeneration, client: fresh, events: fresh.events, sessions: rows})
			state := next.(rootModel)
			if state.screen != screenChat || state.chat.ready || state.chat.view.draft.text != "retained draft" || c.sends != 0 || fresh.sends != 0 {
				t.Fatal("reconnect lost draft, enabled input or sent automatically")
			}
			if kind == "same" && (state.chat.generation == m.chat.generation || state.chat.control != nil || state.chat.operation != "retained-operation") {
				t.Fatal("same-scope reconnect reused stale control/read connections or lost operation")
			}
		})
	}
}

func TestClaudeChatNativeCompletedReceiptNeedsHistoryBeforeNextOwnerSend(t *testing.T) {
	for _, visible := range []bool{false, true} {
		t.Run(map[bool]string{false: "flush-pending", true: "exact-echo"}[visible], func(t *testing.T) {
			m, c := nativeClaudeController(t)
			const text = "exact initial caller text"
			const uuid = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
			m.chat.operation = "initial-operation"
			m.chat.pendingText = text
			m.chat.busy = true
			m.chat.ready = false
			m.chat.view.draft = newLineEditor(text)
			if visible {
				c.history.Items = nativeClaudeUIRows(t, uuid, text)
			}
			receipt := protocol.SemanticMessageReceipt{OperationID: m.chat.operation, Session: m.chat.session.ID, SessionInstance: "native-instance", State: "completed", NativeMessageID: uuid, NativeSessionID: "1f700d8f-d3e3-42c1-9ab0-dcdb22f27772"}
			next, cmd, _ := m.updateClaudeChatMessage(claudeChatReadMsg{generation: 1, session: m.chat.session, page: c.history, receipt: receipt})
			chat := next.(rootModel).chat
			if visible {
				if !chat.ready || chat.operation != "" || chat.view.draft.text != "" {
					t.Fatal("exact completed native echo did not permit a later explicit owner turn")
				}
			} else if chat.ready || chat.operation == "" || chat.view.draft.text != text || cmd == nil {
				t.Fatal("completed result bypassed pending native history/retained draft")
			}
			if c.sends != 0 {
				t.Fatal("completion generated automatic input")
			}
		})
	}
}

func TestClaudeChatNativeLongTurnPollsReadOnlyPastFixtureDeadline(t *testing.T) {
	m, c := nativeClaudeController(t)
	const text = "long-running explicit owner text"
	const uuid = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
	m.chat.operation = "long-native-operation"
	m.chat.pendingText = text
	m.chat.busy = true
	m.chat.ready = false
	m.chat.view.draft = newLineEditor(text)
	m.chat.readUntil = time.Now().Add(-time.Minute)
	receipt := protocol.SemanticMessageReceipt{OperationID: m.chat.operation, Session: m.chat.session.ID, SessionInstance: "native-instance", State: "admitted", NativeMessageID: uuid, NativeSessionID: "1f700d8f-d3e3-42c1-9ab0-dcdb22f27772"}
	next, poll, _ := m.updateClaudeChatMessage(claudeChatReadMsg{generation: 1, session: m.chat.session, page: c.history, receipt: receipt})
	if poll == nil || c.sends != 0 {
		t.Fatal("native long turn stopped read-only polling after fixture deadline")
	}
	state := next.(rootModel)
	receipt.State = "completed"
	next, poll, _ = state.updateClaudeChatMessage(claudeChatReadMsg{generation: 1, session: state.chat.session, page: c.history, receipt: receipt})
	if poll == nil || next.(rootModel).chat.ready {
		t.Fatal("completed result stopped polling before native history flush")
	}
	state = next.(rootModel)
	page := c.history
	page.Items = nativeClaudeUIRows(t, uuid, text)
	next, _, _ = state.updateClaudeChatMessage(claudeChatReadMsg{generation: 1, session: state.chat.session, page: page, receipt: receipt})
	state = next.(rootModel)
	if !state.chat.ready || state.chat.operation != "" || state.chat.view.draft.text != "" || c.sends != 0 {
		t.Fatal("late native completion/history did not restore explicit owner composer without sending")
	}
}

func TestClaudeChatNativeRejectedInitialRetainsDraftAndReadsUntilExplicitRecoveryReady(t *testing.T) {
	m, c := nativeClaudeController(t)
	const text = "unsent initial owner draft"
	m.chat.operation = "claude-initial:chat"
	m.chat.pendingText = text
	m.chat.busy = true
	m.chat.ready = false
	m.chat.view.draft = newLineEditor(text)
	m.chat.readUntil = time.Now().Add(-time.Minute)
	row := m.chat.session
	proof := *row.Capabilities
	proof.NativeComposer = false
	row.Capabilities = &proof
	receipt := protocol.SemanticMessageReceipt{OperationID: m.chat.operation, Session: m.chat.session.ID, SessionInstance: "native-instance", State: "rejected", Reason: "Initial message remains unsent; submit explicitly when ready"}
	next, poll, _ := m.updateClaudeChatMessage(claudeChatReadMsg{generation: 1, session: row, page: c.history, receipt: receipt})
	state := next.(rootModel)
	if poll == nil || state.chat.operation != "" || state.chat.ready || state.chat.view.draft.text != text || c.sends != 0 {
		t.Fatal("affirmatively unsent initial draft stayed locked or replayed")
	}
	proof.NativeComposer = true
	next, _, _ = state.updateClaudeChatMessage(claudeChatReadMsg{generation: 1, session: row, page: c.history})
	state = next.(rootModel)
	if !state.chat.ready || state.chat.view.draft.text != text || c.sends != 0 || !strings.Contains(state.chat.notice, "unsent") {
		t.Fatal("late readiness lost unsent note/draft or sent automatically")
	}
}

func TestClaudeChatNativePollingStillStopsOnScopeCancelAndStop(t *testing.T) {
	for _, kind := range []string{"scope", "cancel", "stop"} {
		t.Run(kind, func(t *testing.T) {
			m, c := nativeClaudeController(t)
			m.chat.operation = "pending"
			m.chat.ready = false
			m.chat.readUntil = time.Now().Add(-time.Minute)
			var err error
			switch kind {
			case "scope":
				err = errClaudeChatScope
			case "cancel":
				m.chat.cancel()
				err = errors.New("read unavailable")
			case "stop":
				m.chat.stopPending = true
				err = errors.New("read unavailable")
			}
			_, poll, _ := m.updateClaudeChatMessage(claudeChatReadMsg{generation: 1, err: err})
			if poll != nil || c.sends != 0 {
				t.Fatal("native polling survived its scope/cancellation/Stop fence")
			}
		})
	}
}
