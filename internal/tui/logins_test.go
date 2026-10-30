package tui

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

func TestLoginsNativeSignInKeyOpensConfirmation(t *testing.T) {
	m := rootModel{screen: screenLogins, client: newLoginsTestClient(), logins: loginsModel{available: true, loaded: true, focus: 1, err: "old error", reply: protocol.LoginsReply{Revision: 3, Providers: []protocol.ProviderLoginView{{Provider: "codex", ActiveID: "codex-work", State: "active"}}, Logins: []protocol.LoginView{{ID: "codex-work", Provider: "codex", Label: "Work Codex", Active: true}}}}}
	next, cmd := m.Update(keyRune('n'))
	got := next.(rootModel).logins
	if cmd != nil || got.mode != "deactivate" || got.provider != "codex" || got.err != "" {
		t.Fatalf("n did not open native sign-in confirmation: mode=%q provider=%q err=%q cmd=%v", got.mode, got.provider, got.err, cmd != nil)
	}
}

func TestLoginsDeactivateConfirmSendsRequest(t *testing.T) {
	providers := []protocol.ProviderLoginView{{Provider: "claude", ActiveID: "claude-work", State: "active"}, {Provider: "codex", ActiveID: "codex-work", State: "active"}}
	saved := []protocol.LoginView{{ID: "claude-work", Provider: "claude", Label: "Work Claude", Active: true}, {ID: "codex-work", Provider: "codex", Label: "Work Codex", Active: true}}
	for _, tt := range []struct {
		name     string
		focus    int
		provider string
	}{
		{"provider row", 0, "claude"},
		{"login row", 3, "codex"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newLoginsTestClient()
			m := rootModel{screen: screenLogins, client: c, logins: loginsModel{available: true, loaded: true, focus: tt.focus, reply: protocol.LoginsReply{Revision: 3, Providers: providers, Logins: saved}}}
			confirming, _ := m.Update(keyRune('n'))
			next, cmd := confirming.Update(keyEnter)
			if cmd == nil || next.(rootModel).logins.mode != "" {
				t.Fatal("Enter did not confirm native sign-in")
			}
			cmd()
			if req := c.lastRequest(); req.Action != "deactivate" || req.Provider != tt.provider || req.ExpectedRevision != 3 {
				t.Fatalf("deactivate request = %#v", req)
			}
		})
	}
}

func TestLoginsDeactivateEscCancels(t *testing.T) {
	c := newLoginsTestClient()
	m := rootModel{screen: screenLogins, client: c, logins: loginsModel{available: true, loaded: true, reply: protocol.LoginsReply{Revision: 3, Providers: []protocol.ProviderLoginView{{Provider: "claude", ActiveID: "claude-work", State: "active"}}, Logins: []protocol.LoginView{{ID: "claude-work", Provider: "claude", Label: "Work Claude", Active: true}}}}}
	confirming, _ := m.Update(keyRune('n'))
	next, cmd := confirming.Update(keyEsc)
	if cmd != nil || next.(rootModel).logins.mode != "" || next.(rootModel).screen != screenLogins {
		t.Fatalf("Esc did not cancel native sign-in confirmation: %#v", next.(rootModel).logins)
	}
	if req := c.request("deactivate"); req.Action != "" {
		t.Fatalf("Esc sent deactivate request: %#v", req)
	}
}

func TestLoginsNativeSignInWhenAlreadyNativeShowsNotice(t *testing.T) {
	m := rootModel{screen: screenLogins, client: newLoginsTestClient(), logins: loginsModel{available: true, loaded: true, reply: protocol.LoginsReply{Revision: 3, Providers: []protocol.ProviderLoginView{{Provider: "claude", State: "native"}, {Provider: "codex", State: "native"}}}}}
	for _, tt := range []struct {
		focus  int
		notice string
	}{
		{0, "Claude Code already uses native sign-in"},
		{1, "Codex already uses native sign-in"},
	} {
		m.logins.focus = tt.focus
		next, cmd := m.Update(keyRune('n'))
		if got := next.(rootModel).logins; cmd != nil || got.mode != "" || got.notice != tt.notice {
			t.Fatalf("n on native provider row: mode=%q notice=%q cmd=%v", got.mode, got.notice, cmd != nil)
		}
	}
}

func TestLoginsDeactivateConfirmationView(t *testing.T) {
	m := loginsModel{available: true, loaded: true, mode: "deactivate", provider: "claude"}
	text := m.view(30, 20, false, false)
	compact := strings.Join(strings.Fields(text), " ")
	for _, want := range []string{
		"Return Claude Code to native sign-in?",
		"Running Claude Code discussions stop at their next safe point and end.",
		"Continue them later with hand-off.",
		"The saved login stays parked and can be activated again.",
		"Enter to confirm · Esc to cancel",
	} {
		if !strings.Contains(compact, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if lipgloss.Width(line) > 30 {
			t.Fatalf("confirmation line overflows 30 cells: %q", line)
		}
	}
}

func TestLoginsHintMentionsNativeSignIn(t *testing.T) {
	list := loginsModel{available: true, loaded: true}
	if hint := list.hint(false, false, 120); !strings.Contains(hint, "enter make active · n native sign-in") {
		t.Fatalf("list hint omits native sign-in: %s", hint)
	}
	confirming := loginsModel{available: true, loaded: true, mode: "deactivate", provider: "claude"}
	if hint := confirming.hint(false, false, 120); hint != "enter return to native sign-in · esc cancel" {
		t.Fatalf("confirmation hint = %q", hint)
	}
}

// The daemon's in-use text never crosses the wire, so the client explains an
// active-login removal itself and sends nothing.
func TestLoginsRemoveActiveLoginExplainsNativeSignIn(t *testing.T) {
	saved := []protocol.LoginView{{ID: "claude-work", Provider: "claude", Label: "Work Claude", Active: true}, {ID: "claude-spare", Provider: "claude", Label: "Spare Claude"}}
	c := newLoginsTestClient()
	m := rootModel{screen: screenLogins, client: c, logins: loginsModel{available: true, loaded: true, focus: 1, reply: protocol.LoginsReply{Revision: 3, Providers: []protocol.ProviderLoginView{{Provider: "claude", ActiveID: "claude-work", State: "active"}}, Logins: saved}}}
	next, cmd := m.Update(keyRune('d'))
	if got := next.(rootModel).logins.err; cmd != nil || got != "Return to native sign-in (n) or activate another login before removing the active one." {
		t.Fatalf("active remove: err=%q cmd=%v", got, cmd != nil)
	}
	if req := c.request("remove"); req.Action != "" {
		t.Fatalf("active remove sent a request: %#v", req)
	}
	m.logins.focus = 2
	if _, cmd = m.Update(keyRune('d')); cmd == nil {
		t.Fatal("inactive remove sent nothing")
	}
	cmd()
	if req := c.lastRequest(); req.Action != "remove" || req.LoginID != "claude-spare" {
		t.Fatalf("inactive remove request = %#v", req)
	}
}

func TestLoginsDeactivateConfirmIsSizeGuarded(t *testing.T) {
	c := newLoginsTestClient()
	m := rootModel{screen: screenLogins, client: c, width: 31, height: 8, logins: loginsModel{available: true, loaded: true, mode: "deactivate", provider: "claude", reply: protocol.LoginsReply{Revision: 3, Providers: []protocol.ProviderLoginView{{Provider: "claude", ActiveID: "claude-work", State: "active"}}}}}
	next, cmd := m.Update(keyEnter)
	if cmd != nil || next.(rootModel).screen != screenLogins {
		t.Fatal("size guard let native sign-in confirmation through")
	}
	if req := c.request("deactivate"); req.Action != "" {
		t.Fatalf("below minimum sent deactivate request: %#v", req)
	}
}

func TestLoginsActionsFooterListsNativeSignIn(t *testing.T) {
	m := rootModel{screen: screenLogins, client: newLoginsTestClient(), width: 40, height: 8, logins: loginsModel{available: true, loaded: true, focus: 1, err: strings.Repeat("sign-in needs attention. ", 8), reply: protocol.LoginsReply{Revision: 3, Providers: []protocol.ProviderLoginView{{Provider: "claude", ActiveID: "claude-work", State: "active"}}, Logins: []protocol.LoginView{{ID: "claude-work", Provider: "claude", Label: "Work Claude", Active: true}}}}}
	// The actions line sits after the context, so page the reader to its end first.
	pageDown := tea.KeyPressMsg{Code: tea.KeyPgDown}
	if text := view(send(send(m, pageDown), pageDown)); !strings.Contains(text, "Actions · enter / n/c/d/s") {
		t.Fatalf("short-screen actions footer omits native sign-in:\n%s", text)
	}
}

func TestLoginsNativeSignInIgnoredUntilLoaded(t *testing.T) {
	m := rootModel{screen: screenLogins, client: newLoginsTestClient(), logins: loginsModel{available: true}}
	next, cmd := m.Update(keyRune('n'))
	if got := next.(rootModel).logins; cmd != nil || got.mode != "" || got.notice != "" {
		t.Fatalf("n before the first list: mode=%q notice=%q cmd=%v", got.mode, got.notice, cmd != nil)
	}
}

// With no active login but managed discussions still running, n offers to stop
// only those and sends the same deactivate request.
func TestLoginsStopOnlyConfirmation(t *testing.T) {
	c := newLoginsTestClient()
	m := rootModel{screen: screenLogins, client: c, logins: loginsModel{available: true, loaded: true, focus: 1, reply: protocol.LoginsReply{Revision: 3, Providers: []protocol.ProviderLoginView{{Provider: "claude", State: "native"}, {Provider: "codex", State: "native", ManagedRunning: 2}}}}}
	confirming, cmd := m.Update(keyRune('n'))
	got := confirming.(rootModel).logins
	if cmd != nil || got.mode != "deactivate" || got.provider != "codex" || got.notice != "" {
		t.Fatalf("n did not open stop-only confirmation: mode=%q provider=%q notice=%q", got.mode, got.provider, got.notice)
	}
	text := got.view(30, 20, false, false)
	compact := strings.Join(strings.Fields(text), " ")
	for _, want := range []string{
		"Stop 2 Codex discussions still running in Swarm's managed home?",
		"They stop at their next safe point and end. Continue them later with hand-off. Codex already uses native sign-in for new discussions.",
		"Enter to confirm · Esc to cancel",
	} {
		if !strings.Contains(compact, want) {
			t.Fatalf("stop-only confirmation missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(compact, "Return Codex to native sign-in?") {
		t.Fatalf("stop-only confirmation kept the return text:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if lipgloss.Width(line) > 30 {
			t.Fatalf("confirmation line overflows 30 cells: %q", line)
		}
	}
	_, cmd = confirming.Update(keyEnter)
	if cmd == nil {
		t.Fatal("Enter did not confirm stop-only return")
	}
	cmd()
	if req := c.lastRequest(); req.Action != "deactivate" || req.Provider != "codex" || req.ExpectedRevision != 3 {
		t.Fatalf("deactivate request = %#v", req)
	}
}
