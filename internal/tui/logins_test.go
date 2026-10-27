package tui

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"charm.land/lipgloss/v2"
	"github.com/Nathandela/swarm/internal/protocol"
)

type loginsTestClient struct {
	*fakeClient
	mu       sync.Mutex
	requests []protocol.LoginsReq
	listing  protocol.LoginsReply
}

func newLoginsTestClient() *loginsTestClient {
	c := &loginsTestClient{fakeClient: newFakeClient()}
	c.listing = protocol.LoginsReply{Revision: 3, Providers: []protocol.ProviderLoginView{{Provider: "claude", State: "ready"}, {Provider: "codex", State: "needs_login"}}}
	return c
}

func (c *loginsTestClient) Capabilities() []string { return []string{protocol.CapLoginsManage} }

func (c *loginsTestClient) Logins(req protocol.LoginsReq) (protocol.LoginsReply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
	switch req.Action {
	case "list":
		return c.listing, nil
	case "start":
		return protocol.LoginsReply{Revision: 4, Job: &protocol.LoginJobView{ID: "job-1", Provider: req.Provider, Label: req.Label, State: "authenticating", VerificationURL: "https://auth.example.test/device", UserCode: "DEVICE-CODE", LoginSocket: "/private/login.sock"}}, nil
	case "cancel_login":
		return protocol.LoginsReply{Job: &protocol.LoginJobView{ID: req.JobID, State: "cancelled"}}, nil
	}
	return protocol.LoginsReply{Revision: 4}, nil
}

func (c *loginsTestClient) lastRequest() protocol.LoginsReq {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests[len(c.requests)-1]
}

func (c *loginsTestClient) request(action string) protocol.LoginsReq {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.requests) - 1; i >= 0; i-- {
		if c.requests[i].Action == action {
			return c.requests[i]
		}
	}
	return protocol.LoginsReq{}
}

func TestLogins_AddNativeProviderAndHideSocket(t *testing.T) {
	c := newLoginsTestClient()
	m := New(c, nil).(rootModel)
	next, list := m.enterLogins()
	m = send(next, list()).(rootModel)
	m = send(m, keyRune('a')).(rootModel)
	if !strings.Contains(view(m), "Claude Code") {
		t.Fatal("provider picker did not render")
	}
	m = send(m, keyRight).(rootModel)
	m = send(m, keyEnter).(rootModel)
	m = send(m, tea.PasteMsg{Content: "Work Codex"}).(rootModel)
	started, cmd := m.Update(keyEnter)
	if cmd == nil {
		t.Fatal("label did not start provider login")
	}
	startedModel, listCmd := started.Update(cmd())
	if listCmd == nil {
		t.Fatal("started login did not refresh the list")
	}
	m = send(startedModel, listCmd()).(rootModel)
	if req := c.request("start"); req.Provider != "codex" || req.Label != "Work Codex" {
		t.Fatalf("start request = %#v", req)
	}
	text := view(m)
	if !strings.Contains(text, "DEVICE-CODE") || !strings.Contains(text, "auth.example.test") || strings.Contains(text, "/private/login.sock") {
		t.Fatalf("unexpected sign-in view: %s", text)
	}
	narrow := m.logins.view(24, 8, false, true)
	if !strings.Contains(strings.ReplaceAll(narrow, "\n", ""), "https://auth.example.test/device") {
		t.Fatalf("narrow view clipped the provider URL:\n%s", narrow)
	}
	for _, line := range strings.Split(narrow, "\n") {
		if lipgloss.Width(line) > 24 {
			t.Fatalf("narrow line overflows 24 cells: %q", line)
		}
	}
	cancelling, cancel := m.Update(keyEsc)
	if cancel == nil {
		t.Fatalf("Esc did not cancel pending sign-in: %#v", m.logins)
	}
	cancelled := send(cancelling, cancel()).(rootModel)
	if req := c.lastRequest(); req.Action != "cancel_login" || req.JobID != "job-1" {
		t.Fatalf("cancel request = %#v", req)
	}
	if cancelled.logins.job.ID != "" {
		t.Fatal("cancelled sign-in remained open")
	}
}

func TestLogins_ReadyJobCompletesAutomatically(t *testing.T) {
	c := newLoginsTestClient()
	m := rootModel{screen: screenLogins, client: c, loginsClientGeneration: 2, logins: loginsModel{available: true, generation: 8, job: protocol.LoginJobView{ID: "job-ready", State: "verifying"}}}
	next, cmd := m.applyLoginsReply(loginsReplyMsg{generation: 8, clientGeneration: 2, action: "status", reply: protocol.LoginsReply{Job: &protocol.LoginJobView{ID: "job-ready", State: "ready"}}})
	if cmd == nil {
		t.Fatal("ready job did not auto-complete")
	}
	_ = next
	cmd()
	if req := c.lastRequest(); req.Action != "complete" || req.JobID != "job-ready" {
		t.Fatalf("completion request = %#v", req)
	}
}

func TestLogins_NativeSocketAttachesOnlyOnEnter(t *testing.T) {
	called := false
	m := rootModel{
		screen:      screenLogins,
		loginRunner: func(job protocol.LoginJobView) error { called = job.LoginSocket == "/private/login.sock"; return nil },
		logins:      loginsModel{generation: 2, available: true, job: protocol.LoginJobView{ID: "claude-job", State: "authenticating", LoginSocket: "/private/login.sock"}},
	}
	if !strings.Contains(m.logins.view(50, 10, false, true), "Enter attaches to native sign-in") {
		t.Fatal("native sign-in attachment hint missing")
	}
	next, attach := m.Update(keyEnter)
	if attach == nil || called || !next.(rootModel).logins.attached {
		t.Fatal("native socket was attached before the runner command executed")
	}
	attach()
	if !called {
		t.Fatal("Enter did not attach to the private native sign-in socket")
	}
}

func TestLogins_DropsStaleReplies(t *testing.T) {
	m := rootModel{screen: screenLogins, loginsClientGeneration: 5, logins: loginsModel{generation: 9}}
	next, cmd := m.applyLoginsReply(loginsReplyMsg{generation: 8, clientGeneration: 5, action: "list", reply: protocol.LoginsReply{Revision: 99}})
	if cmd != nil || next.(rootModel).logins.reply.Revision != 0 {
		t.Fatal("stale login response changed current screen")
	}
}

func TestLogins_RejectedCompletionStaysVisible(t *testing.T) {
	m := rootModel{screen: screenLogins, logins: loginsModel{generation: 3, job: protocol.LoginJobView{ID: "job", State: "ready"}}}
	next, cmd := m.applyLoginsReply(loginsReplyMsg{generation: 3, action: "complete", reply: protocol.LoginsReply{Job: &protocol.LoginJobView{ID: "job", State: "failed", Message: "This account is already saved."}}})
	got := next.(rootModel).logins
	if cmd != nil || got.job.State != "failed" || got.job.Message == "" || got.notice != "" {
		t.Fatal("rejected native sign-in displayed as complete")
	}
}

func TestLogins_ProviderNativeAndActiveStates(t *testing.T) {
	if loginProviderStatus("native") != "Native sign-in" || loginProviderStatus("active") != "Active" {
		t.Fatal("provider native/active states were rendered as needs-sign-in")
	}
}

func TestLogins_EnterRetriesOnlyUnavailableActiveClaude(t *testing.T) {
	for _, tc := range []struct {
		name, provider, providerState string
		active, wantActivate          bool
	}{
		{"unavailable active Claude", "claude", "unavailable", true, true},
		{"waiting active Claude", "claude", "waiting", true, false},
		{"ready active Claude", "claude", "ready", true, false},
		{"unavailable active Codex", "codex", "unavailable", true, false},
		{"ready active Codex", "codex", "ready", true, false},
		{"nonactive Claude", "claude", "unavailable", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newLoginsTestClient()
			focus := 1
			if tc.provider == "codex" {
				focus = 2
			}
			m := rootModel{
				screen: screenLogins, client: c,
				logins: loginsModel{available: true, loaded: true, focus: focus, reply: protocol.LoginsReply{
					Revision: 9,
					Providers: []protocol.ProviderLoginView{{Provider: tc.provider, State: tc.providerState}},
					Logins:    []protocol.LoginView{{ID: "login-1", Provider: tc.provider, Active: tc.active}},
				}},
			}
			_, cmd := m.updateLogins(keyEnter)
			if !tc.wantActivate {
				if cmd != nil || len(c.requests) != 0 {
					t.Fatalf("Enter started a request for active %s in provider state %q", tc.provider, tc.providerState)
				}
				return
			}
			if cmd == nil {
				t.Fatal("Enter did not dispatch activation")
			}
			cmd()
			if len(c.requests) != 1 || c.requests[0].Action != "activate" || c.requests[0].LoginID != "login-1" || c.requests[0].ExpectedRevision != 9 {
				t.Fatalf("requests = %#v; want one revision-checked activation", c.requests)
			}
		})
	}
}

func TestLogins_CancelBeforeStartReplyCancelsCreatedJob(t *testing.T) {
	c := newLoginsTestClient()
	m := rootModel{screen: screenLogins, client: c, loginsGeneration: 4, logins: loginsModel{generation: 4, available: true, mode: "label", provider: "claude", label: newLineEditor("Work")}}
	starting, start := m.Update(keyEnter)
	if start == nil {
		t.Fatal("start request was not dispatched")
	}
	cancelling, cancel := starting.Update(keyEsc)
	if cancel != nil || !cancelling.(rootModel).logins.cancelAfterStart {
		t.Fatal("Esc before the start reply did not defer cancellation")
	}
	cancellingModel := cancelling.(rootModel)
	next, cancel := cancellingModel.applyLoginsReply(loginsReplyMsg{generation: cancellingModel.logins.generation, action: "start", reply: protocol.LoginsReply{Job: &protocol.LoginJobView{ID: "late-job", State: "authenticating"}}})
	if cancel == nil {
		t.Fatal("late start reply did not dispatch cancellation")
	}
	cancel()
	if req := c.lastRequest(); req.Action != "cancel_login" || req.JobID != "late-job" {
		t.Fatalf("deferred cancel = %#v", req)
	}
	if next.(rootModel).logins.cancelAfterStart {
		t.Fatal("deferred cancellation flag was not consumed")
	}
}

func TestLogins_ReconnectInvalidatesOldClientReplies(t *testing.T) {
	old, fresh := newLoginsTestClient(), newLoginsTestClient()
	m := rootModel{
		client: old, connections: newClientLifetime(old), events: old.events,
		screen: screenLogins, connectionLost: true, reconnecting: true, reconnectGeneration: 7,
		logins: loginsModel{available: true, generation: 3},
	}
	next, _ := m.applyDaemonReconnected(daemonReconnectedMsg{generation: 7, client: fresh, events: fresh.events, sessions: []protocol.SessionView{}})
	m = next.(rootModel)
	if m.client != fresh || m.loginsClientGeneration != 1 || !m.logins.busy {
		t.Fatal("reconnect did not start a login refresh on the replacement client")
	}
	stale := loginsReplyMsg{generation: m.logins.generation, clientGeneration: 0, action: "list", reply: protocol.LoginsReply{Revision: 99}}
	next, cmd := m.applyLoginsReply(stale)
	if cmd != nil || next.(rootModel).logins.reply.Revision == 99 {
		t.Fatal("old-client login reply landed after reconnect")
	}
	_ = fresh.events
}

func TestLoginsUnsafeNativeCLIShowsPersistentClosedAction(t *testing.T) {
	for _, action := range []string{"start", "relogin"} {
		t.Run(action, func(t *testing.T) {
			m := rootModel{screen: screenLogins, loginsClientGeneration: 2, logins: loginsModel{available: true, loaded: true, busy: true, generation: 7, cancelAfterStart: true}}
			next, cmd := m.applyLoginsReply(loginsReplyMsg{generation: 7, clientGeneration: 2, action: action, err: fmt.Errorf("private-path/secret-sentinel: %w", protocol.ErrLoginsUnsafeNativeCLI)})
			result := next.(rootModel)
			if cmd != nil || result.logins.busy || result.logins.cancelAfterStart {
				t.Fatal("closed admission refusal polled away or retained pending cancellation")
			}
			text := result.logins.view(40, 20, false, false)
			if !strings.Contains(text, "permissions are unsafe") || !strings.Contains(text, "Reinstall") || strings.Contains(text, "secret-sentinel") || strings.Contains(text, "private-path") {
				t.Fatalf("actionable private-safe error missing: %s", text)
			}
			for _, line := range strings.Split(text, "\n") {
				if lipgloss.Width(line) > 40 {
					t.Fatalf("error overflows narrow UI: %q", line)
				}
			}
		})
	}
}
func TestLoginsUnknownRequestErrorsStayGeneric(t *testing.T) {
	m := rootModel{screen: screenLogins, logins: loginsModel{available: true, generation: 1}}
	next, _ := m.applyLoginsReply(loginsReplyMsg{generation: 1, action: "start", err: errors.New(protocol.ErrLoginsUnsafeNativeCLI.Error() + " private-secret-path")})
	text := next.(rootModel).logins.view(80, 20, false, false)
	if strings.Contains(text, "private-secret") || strings.Contains(text, "Reinstall") || !strings.Contains(text, "Login request failed") {
		t.Fatal("unknown error text promoted or leaked")
	}
}

func TestLogins_UsageRenderingIsActiveAndProviderSpecific(t *testing.T) {
	now := time.Now()
	window := func(percent int) *protocol.LoginUsageWindow {
		return &protocol.LoginUsageWindow{RemainingPercent: percent, ObservedAt: now, ResetsAt: now.Add(time.Hour)}
	}
	m := loginsModel{available: true, loaded: true, reply: protocol.LoginsReply{Logins: []protocol.LoginView{
		{Provider: "claude", Label: "Work Claude", State: "ready", Active: true, FiveHour: window(0), Weekly: window(100)},
		{Provider: "codex", Label: "Parked Codex", State: "ready", FiveHour: window(12), Weekly: window(34)},
		{Provider: "codex", Label: "Work Codex", State: "ready", Active: true, FiveHour: window(12), Weekly: window(71)},
	}}}
	text := m.view(30, 20, false, false)
	compact := strings.Join(strings.Fields(text), " ")
	if !strings.Contains(compact, "Remaining 5h 0% · week 100%") {
		t.Fatalf("Claude remaining windows missing: %s", text)
	}
	if !strings.Contains(compact, "Remaining week 71%") || strings.Contains(compact, "week 34%") {
		t.Fatalf("Codex usage missing, or inactive login usage shown: %s", text)
	}
	if strings.Contains(text, "5h 12%") {
		t.Fatalf("Codex 5h usage was shown: %s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if lipgloss.Width(line) > 30 {
			t.Fatalf("usage line overflows narrow view: %q", line)
		}
	}
}

func TestLogins_UnknownOrStaleUsageShowsEmDash(t *testing.T) {
	now := time.Now()
	window := func(percent int, observed, reset time.Time) *protocol.LoginUsageWindow {
		return &protocol.LoginUsageWindow{RemainingPercent: percent, ObservedAt: observed, ResetsAt: reset}
	}
	m := loginsModel{available: true, loaded: true, reply: protocol.LoginsReply{Logins: []protocol.LoginView{{
		Provider: "claude", Label: "Work", Active: true,
		FiveHour: window(50, now.Add(-16*time.Minute), now.Add(time.Hour)),
		Weekly:   window(50, now.Add(time.Hour), now.Add(time.Hour)),
	}}}}
	text := m.view(80, 20, false, false)
	if !strings.Contains(strings.Join(strings.Fields(text), " "), "Remaining 5h — · week —") {
		t.Fatalf("stale and future samples were not hidden: %s", text)
	}
	if loginUsagePercent(window(50, now, now.Add(-time.Second)), now) != "—" || loginUsagePercent(window(101, now, now.Add(time.Hour)), now) != "—" {
		t.Fatal("expired reset or out-of-range usage was shown")
	}
}
