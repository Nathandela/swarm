package tui

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/charmbracelet/x/ansi"
)

type loginsClient interface {
	Capabilities() []string
	Logins(protocol.LoginsReq) (protocol.LoginsReply, error)
}

type LoginRunner func(protocol.LoginJobView) error

func WithLoginRunner(r LoginRunner) Option { return func(m *rootModel) { m.loginRunner = r } }

type loginsModel struct {
	generation              uint64
	focus                   int
	available, loaded, busy bool
	reply                   protocol.LoginsReply
	provider                string
	mode                    string
	label                   lineEditor
	job                     protocol.LoginJobView
	attached                bool
	cancelAfterStart        bool
	err, notice             string
}

type loginsReplyMsg struct {
	generation, clientGeneration uint64
	action                       string
	reply                        protocol.LoginsReply
	err                          error
}
type loginsPollMsg struct{ generation, clientGeneration uint64 }
type loginRunnerDoneMsg struct {
	generation, clientGeneration uint64
	err                          error
}

func (m rootModel) enterLogins() (tea.Model, tea.Cmd) {
	m.screen = screenLogins
	m.loginsGeneration++
	m.logins = loginsModel{generation: m.loginsGeneration}
	if c, ok := m.client.(loginsClient); ok {
		for _, cap := range c.Capabilities() {
			if cap == protocol.CapLoginsManage {
				m.logins.available = true
			}
		}
	}
	if !m.logins.available || m.connectionLost {
		return m, nil
	}
	return m.beginLoginsRequest(protocol.LoginsReq{Action: "list"})
}

func (m rootModel) beginLoginsRequest(req protocol.LoginsReq) (tea.Model, tea.Cmd) {
	l := &m.logins
	if !l.available || l.busy || m.connectionLost {
		return m, nil
	}
	c, ok := m.client.(loginsClient)
	if !ok {
		return m, nil
	}
	m.loginsGeneration++
	l.generation, l.busy = m.loginsGeneration, true
	if req.Action != "list" && req.Action != "status" {
		req.ExpectedRevision = l.reply.Revision
	}
	g, cg := l.generation, m.loginsClientGeneration
	return m, func() tea.Msg { reply, err := c.Logins(req); return loginsReplyMsg{g, cg, req.Action, reply, err} }
}

func loginPoll(generation, clientGeneration uint64) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return loginsPollMsg{generation, clientGeneration} })
}

func (m rootModel) applyLoginsReply(msg loginsReplyMsg) (tea.Model, tea.Cmd) {
	l := &m.logins
	if m.screen != screenLogins || msg.generation != l.generation || msg.clientGeneration != m.loginsClientGeneration {
		return m, nil
	}
	l.busy = false
	if msg.err != nil {
		l.err = "Login request failed; press r to retry or reconnect."
		if msg.action == "remove" && strings.Contains(msg.err.Error(), "login is active or owned by a switch") {
			l.err = "Return to native sign-in (n) or activate another login before removing the active one."
		}
		if errors.Is(msg.err, protocol.ErrLoginsUnsafeNativeCLI) {
			l.err = "Provider CLI permissions are unsafe. Reinstall it or ask its owner to remove group/other write access, then try signing in again."
			l.cancelAfterStart = false
			return m, nil
		}
		return m, loginPoll(l.generation, m.loginsClientGeneration)
	}
	l.err = ""
	if msg.action == "list" || msg.reply.Logins != nil || msg.reply.Providers != nil {
		l.reply, l.loaded = msg.reply, true
	} else if msg.reply.Revision > l.reply.Revision {
		l.reply.Revision = msg.reply.Revision
	}
	job := msg.reply.Job
	if msg.action == "list" && job == nil && l.job.ID == "" {
		for _, job := range msg.reply.Jobs {
			if job.State == "starting" || job.State == "authenticating" || job.State == "verifying" || job.State == "cancelling" || job.State == "ready" {
				l.job = job
				break
			}
		}
	}
	if job != nil {
		l.job = *job
		if l.cancelAfterStart {
			l.cancelAfterStart = false
			return m.beginLoginsRequest(protocol.LoginsReq{Action: "cancel_login", JobID: l.job.ID})
		}
		if l.job.State == "ready" {
			return m.beginLoginsRequest(protocol.LoginsReq{Action: "complete", JobID: l.job.ID})
		}
	}
	if msg.action == "complete" {
		if l.job.State != "complete" {
			return m, nil
		}
		l.job, l.mode, l.attached = protocol.LoginJobView{}, "", false
		l.notice = "Sign-in complete."
		return m.beginLoginsRequest(protocol.LoginsReq{Action: "list"})
	}
	if msg.action == "cancel_login" && msg.reply.Job != nil {
		l.job = *msg.reply.Job
		if l.job.State == "cancelled" || l.job.State == "failed" {
			l.job, l.attached, l.mode = protocol.LoginJobView{}, false, ""
			return m.beginLoginsRequest(protocol.LoginsReq{Action: "list"})
		}
	}
	if job == nil && l.cancelAfterStart && l.job.ID != "" {
		l.cancelAfterStart = false
		return m.beginLoginsRequest(protocol.LoginsReq{Action: "cancel_login", JobID: l.job.ID})
	}
	if msg.action != "list" && msg.action != "status" {
		return m.beginLoginsRequest(protocol.LoginsReq{Action: "list"})
	}
	if l.job.State == "failed" || l.job.State == "cancelled" || l.job.State == "complete" {
		return m, nil
	}
	return m, loginPoll(l.generation, m.loginsClientGeneration)
}

func (m rootModel) applyLoginRunnerDone(msg loginRunnerDoneMsg) (tea.Model, tea.Cmd) {
	l := &m.logins
	if m.screen != screenLogins || msg.generation != l.generation || msg.clientGeneration != m.loginsClientGeneration {
		return m, nil
	}
	l.attached = false
	if msg.err != nil {
		l.err = "Native sign-in attachment ended; checking sign-in status."
	}
	return m.beginLoginsRequest(protocol.LoginsReq{Action: "status", JobID: l.job.ID})
}

func (m rootModel) updateLogins(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	l := &m.logins
	if k.Code == tea.KeyEsc {
		if l.mode == "label" {
			l.mode, l.label = "provider", lineEditor{}
			return m, nil
		}
		if l.mode == "provider" || l.mode == "deactivate" {
			l.mode = ""
			return m, nil
		}
		if l.busy {
			l.cancelAfterStart = true
			l.mode = ""
			return m, nil
		}
		if l.job.ID != "" {
			if l.job.State == "complete" || l.job.State == "failed" || l.job.State == "cancelled" {
				l.job = protocol.LoginJobView{}
				l.mode = ""
				return m.beginLoginsRequest(protocol.LoginsReq{Action: "list"})
			}
			if !l.busy {
				return m.beginLoginsRequest(protocol.LoginsReq{Action: "cancel_login", JobID: l.job.ID})
			}
			return m, nil
		}
		if m.options.focus == optionsFocusLogins {
			m.screen = screenOptions
			return m, nil
		}
		return m, m.enterGeneral()
	}
	if l.mode == "provider" {
		switch k.Code {
		case tea.KeyLeft, tea.KeyRight:
			if l.provider == "claude" {
				l.provider = "codex"
			} else {
				l.provider = "claude"
			}
		case tea.KeyEnter:
			l.mode, l.label = "label", lineEditor{}
		}
		return m, nil
	}
	if l.mode == "label" {
		if k.Code == tea.KeyEnter {
			label := strings.TrimSpace(l.label.text)
			if label == "" {
				l.err = "Enter a label for this login."
				return m, nil
			}
			if len(label) > 128 {
				l.err = "Login labels must be 128 bytes or fewer."
				return m, nil
			}
			l.mode, l.err = "", ""
			return m.beginLoginsRequest(protocol.LoginsReq{Action: "start", Provider: l.provider, Label: label})
		}
		l.label.update(k)
		return m, nil
	}
	if l.mode == "deactivate" {
		if k.Code == tea.KeyEnter {
			l.mode, l.err = "", ""
			return m.beginLoginsRequest(protocol.LoginsReq{Action: "deactivate", Provider: l.provider})
		}
		return m, nil
	}
	if l.job.ID != "" {
		if k.Code == tea.KeyEnter && !l.busy && m.loginRunner != nil && l.job.LoginSocket != "" {
			l.attached = true
			return m, m.runLoginAttach()
		}
		return m, nil
	}
	if k.Code == tea.KeyUp {
		l.focus = max(0, l.focus-1)
		return m, nil
	}
	if k.Code == tea.KeyDown {
		l.focus = min(max(0, len(l.reply.Logins)+1), l.focus+1)
		return m, nil
	}
	if k.Text == "a" {
		l.mode, l.provider = "provider", "claude"
		l.err = ""
		return m, nil
	}
	if k.Text == "r" {
		return m.beginLoginsRequest(protocol.LoginsReq{Action: "list"})
	}
	loginIndex := renderedLoginIndex(l.reply.Logins, l.focus)
	provider := "claude"
	if l.focus >= 1 {
		provider = "codex"
	}
	if loginIndex >= 0 {
		provider = l.reply.Logins[loginIndex].Provider
	}
	if k.Text == "c" {
		return m.beginLoginsRequest(protocol.LoginsReq{Action: "cancel_switch", Provider: provider})
	}
	if k.Text == "n" {
		if providerLogin(l.reply.Providers, provider).ActiveID == "" {
			l.notice = providerLabel(provider) + " already uses native sign-in"
			return m, nil
		}
		l.mode, l.provider, l.err = "deactivate", provider, ""
		return m, nil
	}
	if loginIndex < 0 || loginIndex >= len(l.reply.Logins) {
		return m, nil
	}
	login := l.reply.Logins[loginIndex]
	switch k.Code {
	case tea.KeyEnter:
		if !login.Active {
			return m.beginLoginsRequest(protocol.LoginsReq{Action: "activate", LoginID: login.ID})
		}
	}
	switch k.Text {
	case "s":
		return m.beginLoginsRequest(protocol.LoginsReq{Action: "relogin", LoginID: login.ID})
	case "d":
		return m.beginLoginsRequest(protocol.LoginsReq{Action: "remove", LoginID: login.ID})
	}
	return m, nil
}

func (m rootModel) runLoginAttach() tea.Cmd {
	l := m.logins
	if m.loginRunner == nil || l.job.LoginSocket == "" {
		return nil
	}
	l.attached = true
	g, cg, runner, job := l.generation, m.loginsClientGeneration, m.loginRunner, l.job
	return func() tea.Msg { return loginRunnerDoneMsg{g, cg, runner(job)} }
}

func (l loginsModel) view(width, height int, connectionLost, canAttach bool) string {
	if width < 20 {
		width = 76
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render("swarm") + styleDim.Render(" · logins") + "\n\n")
	if !l.available {
		return b.String() + styleDim.Render("  login management unavailable on this daemon")
	}
	if connectionLost {
		return b.String() + styleError.Render("  daemon disconnected; waiting to reload logins")
	}
	if l.err != "" {
		b.WriteString(styleError.Render(wrapLoginText("  "+l.err, width)) + "\n")
	}
	if l.notice != "" {
		b.WriteString(styleDim.Render("  "+l.notice) + "\n")
	}
	if l.mode == "provider" {
		b.WriteString("Provider  " + picker([]string{"Claude Code", "Codex"}, boolIndex(l.provider == "codex"), width-18) + "\n\nEnter to continue · Esc to cancel")
		return b.String()
	}
	if l.mode == "label" {
		b.WriteString("Label  " + l.label.cursorView() + "\n\nEnter to start sign-in · Esc to cancel")
		return b.String()
	}
	if l.mode == "deactivate" {
		label := providerLabel(l.provider)
		b.WriteString(wrapLoginText("Return "+label+" to native sign-in?", width) + "\n\n" +
			wrapLoginText("Running "+label+" discussions stop at their next safe point and end. Continue them later with hand-off. The saved login stays parked and can be activated again.", width) + "\n\n" +
			wrapLoginText("Enter to confirm · Esc to cancel", width))
		return b.String()
	}
	if l.job.ID != "" {
		b.WriteString("Sign in to " + providerLabel(l.job.Provider) + "\n\n")
		if l.job.VerificationURL != "" {
			b.WriteString(wrapLoginText("Open this address in your browser:", max(12, width-2)) + "\n" + wrapLoginText(l.job.VerificationURL, max(12, width-2)) + "\n")
		}
		if l.job.UserCode != "" {
			b.WriteString(wrapLoginText("Enter this code: "+l.job.UserCode, max(12, width-2)) + "\n")
		}
		if l.job.Message != "" {
			b.WriteString(wrapLoginText(l.job.Message, max(12, width-2)) + "\n")
		}
		b.WriteString("\n" + wrapLoginText(loginJobStatus(l.job.State), max(12, width-2)))
		if l.job.LoginSocket != "" && canAttach {
			b.WriteString("\n" + wrapLoginText("Enter attaches to native sign-in", max(12, width-2)))
		}
		return b.String()
	}
	if !l.loaded {
		return b.String() + styleDim.Render("  loading provider logins…")
	}
	rows := 0
	for _, p := range []string{"claude", "codex"} {
		mark := "  "
		if l.focus == rows {
			mark = "> "
		}
		rows++
		pv := providerLogin(l.reply.Providers, p)
		b.WriteString(mark + styleAccent.Render(providerLabel(p)) + "  " + loginProviderStatus(pv.State) + "\n")
		if pv.Message != "" {
			b.WriteString("  " + clampLoginText(pv.Message, max(12, width-4)) + "\n")
		}
		found := false
		for _, login := range l.reply.Logins {
			if login.Provider != p {
				continue
			}
			found = true
			mark := "  "
			if l.focus == rows {
				mark = "> "
			}
			rows++
			state := login.State
			if login.Active {
				state = "Active · " + state
			}
			b.WriteString(mark + clampLoginText(login.Label, max(8, width/2)) + "  " + loginDisplayState(state) + "\n")
			if login.Active {
				now := time.Now()
				usage := "week " + loginUsagePercent(login.Weekly, now)
				if p == "claude" {
					usage = "5h " + loginUsagePercent(login.FiveHour, now) + " · " + usage
				}
				b.WriteString(wrapLoginText("    Remaining  "+usage, max(12, width-2)) + "\n")
			}
		}
		if !found {
			b.WriteString("  " + styleDim.Render("No saved login") + "\n")
		}
	}
	if l.busy {
		b.WriteString("\n" + styleDim.Render("  working…"))
	}
	return b.String()
}

func loginUsagePercent(window *protocol.LoginUsageWindow, now time.Time) string {
	if window == nil || !window.Fresh(now) {
		return "—"
	}
	return strconv.Itoa(window.RemainingPercent) + "%"
}

func (l loginsModel) hint(connectionLost, canAttach bool, width int) string {
	if connectionLost {
		return "daemon disconnected · esc back"
	}
	if l.mode == "provider" {
		return "←→ choose provider · enter continue · esc cancel"
	}
	if l.mode == "label" {
		return "type label · enter start native sign-in · esc cancel"
	}
	if l.mode == "deactivate" {
		return "enter return to native sign-in · esc cancel"
	}
	if l.job.ID != "" {
		if canAttach && l.job.LoginSocket != "" {
			return "enter attach · esc return to provider logins"
		}
		return "esc return to provider logins"
	}
	return "↑↓ select · enter make active · n native sign-in · a add login · s sign in again · d remove · c cancel switch · r reload · esc back"
}

func providerLogin(providers []protocol.ProviderLoginView, provider string) protocol.ProviderLoginView {
	for _, p := range providers {
		if p.Provider == provider {
			return p
		}
	}
	return protocol.ProviderLoginView{Provider: provider, State: "needs_login"}
}
func providerLabel(p string) string {
	if p == "codex" {
		return "Codex"
	}
	return "Claude Code"
}
func boolIndex(b bool) int {
	if b {
		return 1
	}
	return 0
}
func loginProviderStatus(s string) string {
	switch s {
	case "native":
		return "Native sign-in"
	case "active":
		return "Active"
	case "ready":
		return "Ready"
	case "switching", "waiting":
		return "Switching / Waiting"
	case "needs_login":
		return "Needs sign-in"
	case "unavailable":
		return "Unavailable"
	}
	return "Needs sign-in"
}
func loginDisplayState(s string) string {
	switch s {
	case "ready":
		return "Ready"
	case "needs-login", "needs_login":
		return "Needs sign-in"
	case "limited":
		return "Limited"
	}
	return s
}
func loginJobStatus(s string) string {
	switch s {
	case "starting":
		return "Starting sign-in…"
	case "authenticating":
		return "Waiting for provider sign-in…"
	case "verifying":
		return "Verifying sign-in…"
	case "ready":
		return "Sign-in ready"
	case "failed":
		return "Sign-in failed"
	case "cancelled":
		return "Sign-in cancelled"
	case "complete":
		return "Sign-in complete"
	case "cancelling":
		return "Stopping sign-in…"
	}
	return "Sign-in in progress…"
}
func clampLoginText(s string, width int) string {
	return clampCells(safeLoginText(s), width)
}

func wrapLoginText(s string, width int) string {
	return ansi.Wrap(safeLoginText(s), width, "")
}

func safeLoginText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func renderedLoginIndex(logins []protocol.LoginView, focus int) int {
	row := 0
	for _, provider := range []string{"claude", "codex"} {
		if row == focus {
			return -1
		}
		row++
		for i, login := range logins {
			if login.Provider != provider {
				continue
			}
			if row == focus {
				return i
			}
			row++
		}
	}
	return -1
}
