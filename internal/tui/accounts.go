package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nathandela/swarm/internal/protocol"
)

// Account management is optional: old daemons keep Options and its Accounts
// action usable, and show a clear unavailable page instead of a broken form.
type accountsClient interface {
	Capabilities() []string
	Accounts(protocol.AccountsReq) (protocol.AccountsReply, error)
}

type accountsModel struct {
	generation               uint64
	available, loaded, busy  bool
	reply                    protocol.AccountsReply
	focus, detailFocus       int
	scroll                   int
	detail, retireConfirm    bool
	moveOpen, moveConfirm    bool
	moveFocus                int
	moveAccountID, moveLabel string
	moveSessions             []protocol.SessionView
	wizardOpen               bool
	cancelAfterStart         bool
	wizard                   accountWizard
	err, notice              string
}

type accountWizard struct {
	step, providerFocus, methodFocus int
	scroll                           int
	provider, method, targetID       string
	input, label                     lineEditor
	job                              protocol.AccountEnrollmentView
	err                              string
}

const (
	accountStepProvider = iota
	accountStepMethod
	accountStepAuthenticate
	accountStepVerify
	accountStepLabel
	accountStepReview
	accountTokenMaxBytes = 32 * 1024
)

type accountsReplyMsg struct {
	generation, clientGeneration uint64
	action                       string
	reply                        protocol.AccountsReply
	err                          error
}

type accountsPollMsg struct{ generation, clientGeneration uint64 }

// AccountLoginRunner attaches to a dedicated enrollment terminal. It has no
// discussion ID and never enters the ordinary session attach/transcript path.
type AccountLoginRunner func(protocol.AccountEnrollmentView) error

func WithAccountLoginRunner(r AccountLoginRunner) Option {
	return func(m *rootModel) { m.accountLoginRunner = r }
}

type accountLoginDoneMsg struct {
	generation, clientGeneration uint64
	err                          error
}

func (m rootModel) enterAccounts() (tea.Model, tea.Cmd) {
	m.screen = screenAccounts
	m.accountsGeneration++
	m.accounts = accountsModel{generation: m.accountsGeneration}
	c, ok := m.client.(accountsClient)
	if ok {
		for _, cap := range c.Capabilities() {
			if cap == protocol.CapAccountsManage {
				m.accounts.available = true
			}
		}
	}
	if !m.accounts.available || m.connectionLost {
		return m, nil
	}
	return m.beginAccountsRequest(protocol.AccountsReq{Action: "list"})
}

func (m rootModel) beginAccountsRequest(req protocol.AccountsReq) (tea.Model, tea.Cmd) {
	a := &m.accounts
	if !a.available || a.busy || m.connectionLost {
		return m, nil
	}
	c, ok := m.client.(accountsClient)
	if !ok {
		return m, nil
	}
	m.accountsGeneration++
	a.generation = m.accountsGeneration
	a.busy = true
	a.err = ""
	if req.Action != "list" && req.Action != "status" {
		req.ExpectedRevision = a.reply.Revision
	}
	generation, clientGeneration := a.generation, m.accountsClientGeneration
	return m, func() tea.Msg {
		reply, err := c.Accounts(req)
		return accountsReplyMsg{generation: generation, clientGeneration: clientGeneration, action: req.Action, reply: reply, err: err}
	}
}

func accountPoll(generation, clientGeneration uint64) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return accountsPollMsg{generation: generation, clientGeneration: clientGeneration}
	})
}

func (m rootModel) applyAccountsReply(msg accountsReplyMsg) (tea.Model, tea.Cmd) {
	a := &m.accounts
	if m.screen != screenAccounts || msg.generation != a.generation || msg.clientGeneration != m.accountsClientGeneration {
		return m, nil
	}
	a.busy = false
	if msg.err != nil {
		// Error strings can contain paths, provider output or a submitted token.
		// Only deliberately nonsecret progress DTOs are shown on this page.
		a.err = "Accounts request failed; press r to reload or reconnect."
		if msg.action == "move" {
			a.err = "Move refused. The daemon could not verify a safe handoff; this discussion stays on its current account."
			if errors.Is(msg.err, protocol.ErrAccountMoveUnmanaged) {
				a.err = "Move refused: unmanaged discussion. Migration requires verified writer ownership, history and configuration; that source migration is not available yet."
			}
			a.moveConfirm = false
		}
		if a.wizardOpen {
			a.wizard.err = "This step failed. Press r to retry, or Esc to leave sign-in."
			if a.wizard.step == accountStepAuthenticate && a.wizard.job.ID == "" && (a.wizard.method == "token-manual" || a.wizard.method == "import-native") {
				a.wizard.err = "This step failed. Enter the token or profile path again, or Esc to leave."
			}
		}
		return m, nil
	}
	selected := a.selectedKey()
	// Mutation/status replies may contain only a Job. Do not discard a useful
	// list snapshot until the next authoritative list reply arrives.
	if msg.action == "list" || msg.reply.Accounts != nil || msg.reply.Enabled != nil || msg.reply.Methods != nil {
		a.reply = msg.reply
		a.loaded = true
		a.restoreSelection(selected)
	} else if msg.reply.Revision > a.reply.Revision {
		a.reply.Revision = msg.reply.Revision
	}
	if a.wizardOpen && msg.reply.Job != nil {
		a.wizard.err = ""
		m.updateAccountJob(*msg.reply.Job)
	}
	if a.cancelAfterStart && msg.reply.Job != nil {
		a.cancelAfterStart = false
		return m.beginAccountsRequest(protocol.AccountsReq{Action: "cancel", JobID: msg.reply.Job.ID})
	}
	if a.wizardOpen && msg.action == "list" && a.wizard.job.ID != "" {
		for _, job := range msg.reply.Jobs {
			if job.ID == a.wizard.job.ID {
				m.updateAccountJob(job)
				break
			}
		}
	}
	if msg.action == "cancel" {
		a.wizard = accountWizard{}
		a.wizardOpen = false
		a.notice = "Sign-in cancelled."
		if msg.reply.Job != nil && msg.reply.Job.State == "admitted" {
			a.notice = "This account was already added. Open its details to retire it."
		}
	}
	if msg.action == "admit" {
		a.wizard = accountWizard{}
		a.wizardOpen = false
		a.notice = "Account added. Open details for quota; enable rotation separately."
	}
	if msg.action == "move" {
		a.moveOpen, a.moveConfirm = false, false
		a.moveSessions = nil
		a.notice = "Move requested. The discussion stays held until a safe handoff is verified; prompts and tools are not replayed."
	}
	if msg.action != "list" && msg.action != "status" {
		return m.beginAccountsRequest(protocol.AccountsReq{Action: "list"})
	}
	return m, accountPoll(a.generation, m.accountsClientGeneration)
}

func (m *rootModel) updateAccountJob(job protocol.AccountEnrollmentView) {
	w := &m.accounts.wizard
	if w.job.State != job.State {
		w.scroll = 0
	}
	w.job = job
	m.advanceAccountJob()
}

func (m *rootModel) advanceAccountJob() {
	w := &m.accounts.wizard
	if w.job.TargetAccountID != "" {
		w.targetID = w.job.TargetAccountID
	}
	switch w.job.State {
	case "ready":
		if w.step < accountStepVerify {
			w.step = accountStepVerify
			w.scroll = 0
			w.input = lineEditor{}
		}
	case "admitted":
		m.accounts.wizardOpen = false
		m.accounts.wizard = accountWizard{}
		m.accounts.notice = "Account was added."
	case "failed":
		if w.step != accountStepAuthenticate {
			w.step, w.scroll = accountStepAuthenticate, 0
		}
		w.input = lineEditor{}
		w.err = accountText(w.job.Message)
		if w.err == "" {
			w.err = "Sign-in failed. Press r to retry this method."
		}
	case "cancelled":
		if w.step != accountStepAuthenticate {
			w.step, w.scroll = accountStepAuthenticate, 0
		}
		w.input = lineEditor{}
		w.err = "Sign-in was cancelled. Press r to start a new attempt."
	default:
		if w.step >= accountStepVerify {
			w.step, w.scroll = accountStepAuthenticate, 0
		}
	}
}

type accountRow struct {
	kind, provider, id string
	index              int
}

func (a accountsModel) rows() []accountRow {
	var rows []accountRow
	for _, provider := range []string{"claude", "codex"} {
		rows = append(rows, accountRow{kind: "provider", provider: provider, id: provider})
		for i, account := range a.reply.Accounts {
			if account.Provider == provider {
				rows = append(rows, accountRow{kind: "account", provider: provider, id: account.ID, index: i})
			}
		}
		for i, job := range a.reply.Jobs {
			if job.Provider == provider && job.State != "admitted" && job.State != "cancelled" {
				rows = append(rows, accountRow{kind: "job", provider: provider, id: job.ID, index: i})
			}
		}
	}
	return rows
}

func (a accountsModel) selected() accountRow {
	rows := a.rows()
	if a.focus < 0 || a.focus >= len(rows) {
		return accountRow{}
	}
	return rows[a.focus]
}

func (a accountsModel) selectedKey() string { row := a.selected(); return row.kind + ":" + row.id }

func (a *accountsModel) restoreSelection(key string) {
	rows := a.rows()
	for i, row := range rows {
		if row.kind+":"+row.id == key {
			a.focus = i
			return
		}
	}
	if a.focus >= len(rows) {
		a.focus = len(rows) - 1
	}
	if a.focus < 0 {
		a.focus = 0
	}
}

func (m rootModel) updateAccounts(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	a := &m.accounts
	if a.wizardOpen {
		return m.updateAccountWizard(k)
	}
	if a.moveOpen {
		return m.updateAccountMove(k)
	}
	if a.retireConfirm {
		switch k.Code {
		case tea.KeyEsc, 'n':
			a.retireConfirm = false
		case tea.KeyEnter, 'y':
			row := a.selected()
			a.retireConfirm = false
			return m.beginAccountsRequest(protocol.AccountsReq{Action: "update", AccountID: row.id, Retire: true})
		}
		return m, nil
	}
	if k.Code == tea.KeyEsc {
		if a.detail {
			a.detail = false
			return m, nil
		}
		m.accountsGeneration++ // invalidate every page reply and timer
		a.generation = m.accountsGeneration
		m.screen = screenOptions
		return m, nil
	}
	if k.Code == tea.KeyPgDown || k.Code == tea.KeyPgUp {
		step := max(1, m.height-4)
		if k.Code == tea.KeyPgUp {
			step = -step
		}
		if a.detail || a.retireConfirm || !a.available {
			a.scroll = max(0, a.scroll+step)
		} else {
			a.focus = min(max(0, a.focus+step), max(0, len(a.rows())-1))
		}
		return m, nil
	}
	if m.connectionLost {
		if k.Text == "r" {
			return m.retryDaemonConnection()
		}
		return m, nil
	}
	if !a.available {
		return m, nil
	}
	if k.Text == "a" && !a.busy {
		a.wizardOpen = true
		a.wizard = accountWizard{}
		a.cancelAfterStart = false
		return m, nil
	}
	if !a.loaded {
		if k.Text == "r" {
			return m.beginAccountsRequest(protocol.AccountsReq{Action: "list"})
		}
		return m, nil
	}
	step := 0
	if k.Code == tea.KeyUp || k.Text == "k" {
		step = -1
	}
	if k.Code == tea.KeyDown || k.Code == tea.KeyTab || k.Text == "j" {
		step = 1
	}
	if step != 0 {
		if a.detail {
			a.detailFocus = wrapIndex(a.detailFocus+step, 7)
			a.scroll = -1
		} else {
			a.focus = wrapIndex(a.focus+step, len(a.rows()))
		}
		return m, nil
	}
	if k.Code == tea.KeyEnter && !a.detail && a.selected().kind == "account" {
		a.detail = true
		a.detailFocus = 0
		a.scroll = 0
		return m, nil
	}
	if k.Code == tea.KeyEnter && a.detail && a.detailFocus == 6 {
		a.detail = false
		return m, nil
	}
	if a.busy {
		return m, nil
	}
	if a.err != "" {
		if k.Text == "r" {
			return m.beginAccountsRequest(protocol.AccountsReq{Action: "list"})
		}
		return m, nil
	}
	row := a.selected()
	if row.kind == "job" && k.Code == tea.KeyEnter {
		job := a.reply.Jobs[row.index]
		targetID := job.TargetAccountID
		if targetID == "" && a.wizard.job.ID == job.ID {
			targetID = a.wizard.targetID
		}
		a.wizardOpen = true
		a.wizard = accountWizard{step: accountStepAuthenticate, provider: job.Provider, method: job.Method, job: job, targetID: targetID}
		m.advanceAccountJob()
		return m.beginAccountsRequest(protocol.AccountsReq{Action: "status", JobID: job.ID})
	}
	if k.Text == "e" || row.kind == "provider" && k.Code == tea.KeyEnter {
		return m.beginAccountsRequest(protocol.AccountsReq{Action: "enable", Provider: row.provider, Enabled: !a.reply.Enabled[row.provider]})
	}
	if row.kind != "account" {
		return m, nil
	}
	account := a.reply.Accounts[row.index]
	action := ""
	switch {
	case k.Text == "p":
		action = "pause"
	case k.Text == "r":
		action = "refresh"
	case k.Text == "l":
		action = "login"
	case k.Code == tea.KeyEnter && a.detail:
		action = []string{"pause", "refresh", "retry", "login", "move", "retire", "back"}[a.detailFocus]
	}
	switch action {
	case "pause":
		return m.beginAccountsRequest(protocol.AccountsReq{Action: "update", AccountID: account.ID, Paused: account.State != "paused"})
	case "refresh":
		if !account.RefreshSupported {
			a.notice = "This credential has no supported quota refresh. Unknown quota remains unknown."
			return m, nil
		}
		return m.beginAccountsRequest(protocol.AccountsReq{Action: "refresh", AccountID: account.ID})
	case "retry":
		if !account.RetrySupported {
			a.notice = "Retry availability is unavailable for this account. Refresh only updates observations."
			return m, nil
		}
		return m.beginAccountsRequest(protocol.AccountsReq{Action: "retry", AccountID: account.ID})
	case "login":
		a.wizardOpen = true
		a.wizard = accountWizard{step: accountStepMethod, provider: account.Provider, targetID: account.ID}
	case "retire":
		a.retireConfirm = true
	case "move":
		a.moveOpen, a.moveConfirm = true, false
		a.moveFocus = 0
		a.moveAccountID, a.moveLabel = account.ID, account.Label
		a.scroll = -1
		a.moveSessions = nil
		owner := ""
		if c, ok := m.client.(interface{ EndpointID() string }); ok {
			owner = c.EndpointID()
		}
		for _, session := range protocol.VisibleDiscussions(m.general.sessions) {
			if session.Agent == account.Provider && (owner == "" || session.EndpointID == owner) {
				a.moveSessions = append(a.moveSessions, session)
			}
		}
	case "back":
		a.detail = false
	}
	return m, nil
}

func (m rootModel) updateAccountMove(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	a := &m.accounts
	if k.Code == tea.KeyEsc || a.moveConfirm && k.Text == "n" {
		if a.moveConfirm {
			a.moveConfirm = false
		} else {
			a.moveOpen = false
			a.moveSessions = nil
		}
		return m, nil
	}
	if m.connectionLost {
		if k.Text == "r" {
			return m.retryDaemonConnection()
		}
		return m, nil
	}
	if a.busy || len(a.moveSessions) == 0 {
		return m, nil
	}
	if a.moveConfirm {
		if k.Code == tea.KeyEnter || k.Text == "y" {
			sessionID := a.moveSessions[a.moveFocus].ID
			if _, local, ok := protocol.ParseID(sessionID); ok {
				sessionID = local
			}
			return m.beginAccountsRequest(protocol.AccountsReq{Action: "move", AccountID: a.moveAccountID, SessionID: sessionID})
		}
		return m, nil
	}
	if k.Code == tea.KeyUp || k.Text == "k" {
		a.moveFocus = wrapIndex(a.moveFocus-1, len(a.moveSessions))
	}
	if k.Code == tea.KeyDown || k.Code == tea.KeyTab || k.Text == "j" {
		a.moveFocus = wrapIndex(a.moveFocus+1, len(a.moveSessions))
	}
	if k.Code == tea.KeyEnter {
		a.moveConfirm = true
	}
	return m, nil
}

func (m rootModel) updateAccountWizard(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	a, w := &m.accounts, &m.accounts.wizard
	if w.step >= accountStepVerify && w.job.State != "ready" {
		w.step, w.scroll = accountStepAuthenticate, 0
	}
	if k.Code == tea.KeyEsc {
		w.input = lineEditor{}
		a.wizardOpen = false
		if w.job.ID != "" && w.job.State != "failed" && w.job.State != "cancelled" {
			a.notice = "Sign-in continues in Accounts; reopen its row to review or Cancel."
		}
		w.job.VerificationURL, w.job.UserCode, w.job.LoginSocket = "", "", ""
		return m, nil
	}
	if k.Code == tea.KeyPgDown || k.Code == tea.KeyPgUp {
		step := max(1, m.height-4)
		if k.Code == tea.KeyPgUp {
			step = -step
		}
		w.scroll = max(0, w.scroll+step)
		return m, nil
	}
	// Ctrl+X is explicit cancellation; Esc merely leaves/detaches.
	if k.Code == 'x' && k.Mod == tea.ModCtrl {
		w.input = lineEditor{}
		if w.job.ID == "" {
			if a.busy {
				a.cancelAfterStart = true
				w.err = "Cancelling sign-in after the daemon identifies this attempt…"
				return m, nil
			}
			a.wizardOpen = false
			a.wizard = accountWizard{}
			return m, nil
		}
		// Explicit terminal intent supersedes an in-flight status/list read.
		a.busy = false
		return m.beginAccountsRequest(protocol.AccountsReq{Action: "cancel", JobID: w.job.ID})
	}
	if m.connectionLost {
		if k.Text == "r" {
			return m.retryDaemonConnection()
		}
		return m, nil
	}
	if k.Code == tea.KeyBackspace && w.step != accountStepLabel && w.step != accountStepAuthenticate || k.Code == tea.KeyTab && k.Mod == tea.ModShift {
		w.input = lineEditor{}
		if w.job.ID != "" {
			w.err = "Leave with Esc, or Cancel this attempt with Ctrl+X before starting another."
			return m, nil
		}
		if w.step > accountStepProvider {
			w.step--
			w.scroll = 0
			w.err = ""
		}
		return m, nil
	}
	if a.busy {
		return m, nil
	}
	switch w.step {
	case accountStepProvider:
		previous := w.providerFocus
		if k.Code == tea.KeyUp || k.Code == tea.KeyLeft || k.Text == "k" {
			w.providerFocus = wrapIndex(w.providerFocus-1, 2)
		}
		if k.Code == tea.KeyDown || k.Code == tea.KeyRight || k.Code == tea.KeyTab || k.Text == "j" {
			w.providerFocus = wrapIndex(w.providerFocus+1, 2)
		}
		if w.providerFocus != previous {
			w.scroll = -1
		}
		if k.Code == tea.KeyEnter {
			w.provider = []string{"claude", "codex"}[w.providerFocus]
			w.step = accountStepMethod
			w.scroll = 0
		}
	case accountStepMethod:
		methods := a.reply.Methods[w.provider]
		if len(methods) == 0 {
			return m, nil
		}
		previous := w.methodFocus
		if k.Code == tea.KeyUp || k.Text == "k" {
			w.methodFocus = wrapIndex(w.methodFocus-1, len(methods))
		}
		if k.Code == tea.KeyDown || k.Code == tea.KeyTab || k.Text == "j" {
			w.methodFocus = wrapIndex(w.methodFocus+1, len(methods))
		}
		if w.methodFocus != previous {
			w.scroll = -1
		}
		if k.Code == tea.KeyEnter {
			method := methods[w.methodFocus]
			if !method.Available || method.ID == "native-login" && m.accountLoginRunner == nil {
				w.err = accountText(method.Reason)
				if method.Available && method.ID == "native-login" && m.accountLoginRunner == nil {
					w.err = "Native terminal sign-in is unavailable in this client."
				}
				if w.err == "" {
					w.err = "This sign-in method is unavailable on this daemon."
				}
				return m, nil
			}
			w.method = method.ID
			w.step = accountStepAuthenticate
			w.scroll = 0
			w.err = ""
			if w.method != "token-manual" && w.method != "import-native" {
				return m.startAccountEnrollment()
			}
		}
	case accountStepAuthenticate:
		if w.method == "token-manual" || w.method == "import-native" {
			if w.job.ID == "" {
				if k.Code == tea.KeyEnter {
					return m.startAccountEnrollment()
				}
				if w.method == "token-manual" {
					w.editToken(k)
				} else {
					w.input.update(k)
				}
				return m, nil
			}
		}
		if k.Text == "r" || k.Code == tea.KeyEnter {
			if w.job.ID == "" {
				return m.startAccountEnrollment()
			}
			if w.job.State == "failed" || w.job.State == "cancelled" {
				w.job = protocol.AccountEnrollmentView{}
				w.scroll = 0
				w.err = ""
				if w.method != "token-manual" && w.method != "import-native" {
					return m.startAccountEnrollment()
				}
				return m, nil
			}
			if k.Code == tea.KeyEnter && accountCanAttach(w.job) && m.accountLoginRunner != nil {
				generation, clientGeneration, job, runner := a.generation, m.accountsClientGeneration, w.job, m.accountLoginRunner
				a.busy = true
				return m, func() tea.Msg {
					return accountLoginDoneMsg{generation: generation, clientGeneration: clientGeneration, err: runner(job)}
				}
			}
			return m.beginAccountsRequest(protocol.AccountsReq{Action: "status", JobID: w.job.ID})
		}
	case accountStepVerify:
		if k.Code == tea.KeyEnter {
			w.step = accountStepLabel
			w.scroll = 0
			if w.label.text == "" {
				w.label.set(accountProviderName(w.provider) + " account")
			}
		}
	case accountStepLabel:
		if k.Code == tea.KeyEnter {
			label := strings.TrimSpace(w.label.text)
			if label == "" || len(label) > 120 {
				w.err = "Enter a label of 1–120 bytes."
				return m, nil
			}
			w.label.set(label)
			w.step = accountStepReview
			w.scroll = 0
			w.err = ""
		} else {
			w.label.update(k)
		}
	case accountStepReview:
		if k.Code == tea.KeyEnter {
			return m.beginAccountsRequest(protocol.AccountsReq{Action: "admit", JobID: w.job.ID, AccountID: w.targetID, Label: w.label.text})
		}
	}
	return m, nil
}

func (m rootModel) startAccountEnrollment() (tea.Model, tea.Cmd) {
	w := &m.accounts.wizard
	w.scroll = 0
	req := protocol.AccountsReq{Action: "start", Provider: w.provider, Method: w.method, AccountID: w.targetID}
	switch w.method {
	case "token-manual":
		if w.input.text == "" {
			w.err = "Paste the subscription token first."
			return m, nil
		}
		req.Token = w.input.text
	case "import-native":
		if strings.TrimSpace(w.input.text) == "" {
			w.err = "Enter the native profile directory or login file path first."
			return m, nil
		}
		req.Action = "import"
		req.SourcePath = strings.TrimSpace(w.input.text)
	}
	w.input = lineEditor{} // only the one-shot RPC closure owns submitted credentials
	w.err = ""
	return m.beginAccountsRequest(req)
}

func (w *accountWizard) editToken(k tea.KeyPressMsg) {
	if k.Text != "" {
		if err := validAccountTokenText(k.Text, false); err != "" {
			w.err = err
			return
		}
		if len(w.input.text)+len(k.Text) > accountTokenMaxBytes {
			w.err = "Token input is limited to 32 KiB."
			return
		}
	}
	w.input.update(k)
	w.err = ""
}

func validAccountTokenText(text string, allowEmpty bool) string {
	if !utf8.ValidString(text) {
		return "Token input must be valid UTF-8."
	}
	if !allowEmpty && text == "" {
		return "Paste the subscription token first."
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return "Token input contains a control character; paste only the token."
		}
	}
	return ""
}

func (m *rootModel) pasteAccounts(text string) {
	if !m.accounts.wizardOpen || m.accounts.busy || m.connectionLost {
		return
	}
	w := &m.accounts.wizard
	switch {
	case w.step == accountStepAuthenticate && w.job.ID == "" && w.method == "token-manual":
		// setup-token output commonly carries a final newline. Only surrounding
		// ASCII space/tab/CR/LF is trimmed; embedded controls are rejected.
		text = strings.Trim(text, " \t\r\n")
		if err := validAccountTokenText(text, false); err != "" {
			w.err = err
			return
		}
		if len(w.input.text)+len(text) > accountTokenMaxBytes {
			w.err = "Token input is limited to 32 KiB."
			return
		}
		w.input.insert(text)
		w.err = ""
	case w.step == accountStepAuthenticate && w.job.ID == "" && w.method == "import-native":
		w.input.paste(text)
	case w.step == accountStepLabel:
		w.label.paste(text)
	}
}

func accountProviderName(provider string) string {
	if provider == "claude" {
		return "Claude"
	}
	if provider == "codex" {
		return "Codex"
	}
	return accountText(provider)
}

// Account presentation is plain owner-local data. Strip terminal controls and
// bound every external field before styling, including auth URLs and labels.
func accountText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsControl(r) {
			b.WriteRune(r)
		}
		if b.Len() >= 2048 {
			break
		}
	}
	return b.String()
}

func accountState(account protocol.AccountView) string {
	if account.Retiring {
		if account.CredentialsErased {
			return "Retired · account credentials removed"
		}
		return "Retiring · credentials retained"
	}
	if accountReady(account) {
		return "Ready"
	}
	s := strings.ReplaceAll(strings.ReplaceAll(account.State, "_", " "), "-", " ")
	if s == "" {
		return "unknown"
	}
	return accountText(s)
}

func accountReady(account protocol.AccountView) bool {
	return !account.Retiring && (account.State == "enabled" || account.State == "available")
}

func accountJobState(job protocol.AccountEnrollmentView) string {
	switch job.State {
	case "pending":
		return "Starting sign-in"
	case "authenticating":
		return "Waiting for sign-in"
	case "verifying":
		return "Checking sign-in"
	case "ready":
		return "Review needed"
	case "admitting":
		return "Adding account"
	case "admitted":
		return "Account added"
	case "failed":
		return "Sign-in failed"
	case "cancelling":
		return "Stopping sign-in"
	case "cancelled":
		return "Sign-in cancelled"
	default:
		return "Waiting for sign-in status"
	}
}

func accountCanAttach(job protocol.AccountEnrollmentView) bool {
	return job.LoginSocket != "" && (job.State == "pending" || job.State == "authenticating")
}

func accountAge(at time.Time) string {
	if at.IsZero() {
		return "unknown"
	}
	age := time.Since(at)
	if age < 0 {
		age = 0
	}
	if age < time.Minute {
		return "just now"
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(age.Hours()/24))
}

func accountQuotaLines(account protocol.AccountView) []string {
	var lines []string
	if len(account.Quota) == 0 {
		message := "Quota: unknown · not yet observed"
		switch account.QuotaFetchState {
		case "loading":
			message = "Quota: unknown · fetching…"
		case "ready":
			message = "Quota: unknown · provider reported no usage windows"
		case "idle":
			message = "Quota: unknown · waiting for first refresh"
		case "unsupported":
			message = "Quota: unknown · unavailable for this credential"
		}
		lines = append(lines, message)
	}
	for _, quota := range account.Quota {
		used := "unknown"
		if quota.UsedPercent != nil {
			used = fmt.Sprintf("%d%% used", *quota.UsedPercent)
			if *quota.UsedPercent >= 0 && *quota.UsedPercent <= 100 {
				used += fmt.Sprintf(" · %d%% remaining", 100-*quota.UsedPercent)
			}
		}
		line := accountQuotaLabel(quota.Label) + ": " + used
		if quota.ResetAt != nil {
			if !quota.ResetAt.After(time.Now()) {
				line += " · reset passed " + quota.ResetAt.Local().Format("02 Jan 15:04")
			} else {
				line += " · resets " + quota.ResetAt.Local().Format("02 Jan 15:04")
			}
		}
		line += " · last observed: " + accountAge(quota.ObservedAt)
		if accountQuotaStale(quota) {
			line += " · stale"
		}
		lines = append(lines, line)
	}
	if len(account.Quota) > 0 && account.QuotaFetchState == "loading" {
		lines = append(lines, "Quota: refreshing… · keeping last observation")
	}
	if len(account.Quota) > 0 && account.QuotaFetchState == "unsupported" {
		lines = append(lines, "Quota refresh unavailable for this credential")
	}
	if account.QuotaFetchState == "error" {
		message := "Quota refresh failed"
		if safe := []rune(accountText(account.QuotaFetchError)); len(safe) > 0 {
			message += ": " + string(safe[:min(len(safe), 160)])
		}
		if account.RefreshSupported {
			message += " · r to retry"
		}
		lines = append(lines, message)
	}
	if account.QuotaNextRefreshAt != nil {
		lines = append(lines, "Next quota refresh: "+account.QuotaNextRefreshAt.Local().Format("02 Jan 15:04"))
	}
	if account.QuotaLastAttemptAt != nil && account.QuotaFetchState == "error" {
		lines = append(lines, "Last refresh attempt: "+accountAge(*account.QuotaLastAttemptAt))
	}
	return lines
}

func accountQuotaLabel(label string) string {
	switch label {
	case "five_hour":
		return "5h"
	case "seven_day":
		return "weekly"
	case "seven_day_sonnet":
		return "Sonnet weekly"
	case "seven_day_opus":
		return "Opus weekly"
	default:
		if label == "" {
			return "Usage"
		}
		return accountText(label)
	}
}

func accountQuotaStale(quota protocol.AccountQuotaView) bool {
	return quota.ObservedAt.IsZero() || time.Since(quota.ObservedAt) > 5*time.Minute || quota.ResetAt != nil && !quota.ResetAt.After(time.Now())
}

func accountQuotaSummary(account protocol.AccountView) string {
	var parts []string
	stale := false
	for _, quota := range account.Quota {
		used := "unknown"
		if quota.UsedPercent != nil {
			used = fmt.Sprintf("%d%% used", *quota.UsedPercent)
		}
		parts = append(parts, accountQuotaLabel(quota.Label)+" "+used)
		stale = stale || accountQuotaStale(quota)
	}
	if len(parts) == 0 {
		parts = append(parts, "unknown")
	}
	switch account.QuotaFetchState {
	case "loading":
		parts = append(parts, "fetching…")
	case "error":
		parts = append(parts, "refresh failed")
	case "unsupported":
		parts = append(parts, "unavailable")
	}
	if stale {
		parts = append(parts, "stale")
	}
	return strings.Join(parts, " · ")
}

func (a accountsModel) accountCounts(provider string) (total, ready int) {
	for _, account := range a.reply.Accounts {
		if account.Provider == provider {
			total++
			if accountReady(account) {
				ready++
			}
		}
	}
	return total, ready
}

func (a accountsModel) nextStep(row accountRow) string {
	if row.kind == "job" {
		job := a.reply.Jobs[row.index]
		switch job.State {
		case "failed":
			return "Previous sign-in attempt failed.\nAdded accounts are unchanged.\nNext: Enter to review and retry."
		case "ready":
			return "Next: Enter to review and add this account."
		case "verifying", "admitting", "cancelling":
			return accountJobState(job) + "; no action needed.\nEnter reopens this attempt."
		default:
			return "Next: Enter to continue this sign-in."
		}
	}
	provider := accountProviderName(row.provider)
	_, ready := a.accountCounts(row.provider)
	if !a.reply.Enabled[row.provider] {
		return "Next: enable " + provider + " rotation (e).\nThis applies to new discussions."
	}
	if ready == 0 {
		return "Next: add a verified " + provider + " account (a).\nNo account is ready for new discussions."
	}
	lines := []string{"Next: start a new " + provider + " discussion."}
	if ready == 1 {
		lines = append(lines, "1 ready account; no same-provider backup.")
	} else {
		lines = append(lines, fmt.Sprintf("%d ready accounts configured for switching.", ready))
	}
	observed := false
	for _, account := range a.reply.Accounts {
		if account.Provider == row.provider && (row.kind != "account" || account.ID == row.id) {
			for _, quota := range account.Quota {
				observed = observed || quota.UsedPercent != nil
			}
		}
	}
	if row.kind == "account" && a.reply.Accounts[row.index].QuotaFetchState != "" {
		lines = append(lines, "Quota: "+accountQuotaSummary(a.reply.Accounts[row.index]))
	} else if !observed {
		lines = append(lines, "Quota: unknown · not yet observed")
	} else if row.kind == "account" {
		lines = append(lines, accountQuotaLines(a.reply.Accounts[row.index])[0])
	}
	if ready > 1 {
		lines = append(lines, "Backup capacity is unconfirmed.")
	}
	return strings.Join(lines, "\n")
}

func (a accountsModel) view(width, height int, lost bool, loginSupported bool) string {
	if width > 0 && width < 38 || height > 0 && height < 9 {
		return accountScrolled(accountWrap("Accounts\nResize to manage accounts.", width), height, 0)
	}
	if a.wizardOpen {
		return accountScrolled(a.wizard.view(width, lost, a.busy, loginSupported, a.reply.Methods), height, a.wizard.scroll)
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render("swarm") + styleDim.Render(" · accounts") + "\n\n")
	if lost {
		b.WriteString(styleError.Render("Daemon unavailable. Last known state; sign-in workers may continue.") + "\n")
	}
	if !a.available {
		b.WriteString("\nAccount management is unavailable on this daemon. Upgrade the daemon to use Accounts.\n")
		return accountScrolled(accountWrap(b.String(), width), height, a.scroll)
	}
	if a.busy && !a.loaded {
		b.WriteString(styleDim.Render("Loading account information…") + "\n")
	}
	if a.err != "" {
		b.WriteString(styleError.Render(accountRowLine(a.err, width)) + "\n")
	}
	if a.notice != "" {
		b.WriteString(styleDim.Render(accountRowLine(a.notice, width)) + "\n")
	}
	if a.moveOpen {
		b.WriteString("\nMove discussion here · " + accountText(a.moveLabel) + "\n")
		b.WriteString("Choose a discussion from this provider. The daemon checks history, writer ownership and configuration before moving it.\nUnmanaged discussions are unavailable until those migration checks are implemented.\n")
		if len(a.moveSessions) == 0 {
			b.WriteString("\nNo discussions from this provider are available on this daemon.\n")
		}
		if a.moveConfirm {
			name := a.moveSessions[a.moveFocus].Name
			if name == "" {
				name = a.moveSessions[a.moveFocus].ID
			}
			b.WriteString("\nMove " + accountText(name) + " to this account?\nThe requested model, permissions, project and conversation must be preserved.\ny/Enter request move · n/Esc cancel\n")
		} else {
			for i, session := range a.moveSessions {
				name := session.Name
				if name == "" {
					name = session.ID
				}
				b.WriteString(accountRowLine(accountChoice(accountText(name), i == a.moveFocus), width) + "\n")
			}
		}
		return accountScrolled(accountWrap(b.String(), width), height, -1)
	}
	if a.retireConfirm {
		b.WriteString("\nRetire this account? New discussions stop using it.\nExisting discussions continue. Local account credentials remain until Swarm verifies they can be removed.\nDiscussion history is kept.\n\ny confirm · n/Esc cancel\n")
		return accountScrolled(accountWrap(b.String(), width), height, a.scroll)
	}
	row := a.selected()
	if a.detail && row.kind == "account" {
		account := a.reply.Accounts[row.index]
		b.WriteString("\n" + styleTitle.Render(accountText(account.Label)) + " · " + accountProviderName(account.Provider) + "\n")
		b.WriteString("Status: " + accountState(account) + "\n")
		fmt.Fprintf(&b, "%d assigned discussions\n", account.Assigned)
		if account.Email != "" {
			b.WriteString("Email: " + accountText(account.Email) + "\n")
		}
		if account.Plan != "" {
			b.WriteString("Plan: " + accountText(account.Plan) + "\n")
		}
		b.WriteString("\n")
		for _, line := range accountQuotaLines(account) {
			b.WriteString(line + "\n")
		}
		if account.RefreshSupported && account.QuotaFetchState != "loading" {
			b.WriteString("r refreshes quota for this account.\n")
		}
		if account.NextRetryAt != nil {
			b.WriteString("Next retry: " + account.NextRetryAt.Local().Format("02 Jan 15:04") + "\n")
		}
		pause := "Pause future assignment"
		if account.State == "paused" {
			pause = "Resume future assignment"
		}
		refresh := "Refresh information"
		if !account.RefreshSupported {
			refresh += " (unavailable)"
		}
		retry := "Retry availability"
		if !account.RetrySupported {
			retry += " (unavailable)"
		}
		b.WriteString("\n")
		for i, label := range []string{pause, refresh, retry, "Sign in again", "Move discussion here", "Retire account", "Back"} {
			b.WriteString(accountChoice(label, i == a.detailFocus) + "\n")
		}
		b.WriteString("\nRefresh updates observations. Retry availability explicitly clears a retryable denial.\n")
		return accountScrolled(accountWrap(b.String(), width), height, a.scroll)
	}
	if a.loaded && len(a.reply.Accounts) == 0 {
		b.WriteString(styleDim.Render("No accounts added yet.") + "\n")
	}
	rows := a.rows()
	guidance := strings.Split(accountWrap(a.nextStep(row), width), "\n")
	header := strings.Split(strings.TrimSuffix(accountWrap(b.String(), width), "\n"), "\n")
	// Keep the current action and focused row together. Paging changes the
	// selection; list viewport position is derived from that single authority.
	budget := max(1, height-len(header)-len(guidance)-1)
	if height <= 0 {
		budget = len(rows)
	} else if len(rows) > budget {
		budget = max(1, budget-1) // one indicator for the hidden rows
	}
	start := 0
	if a.focus >= budget {
		start = a.focus - budget + 1
	}
	end := start + budget
	if end > len(rows) {
		end = len(rows)
	}
	list := make([]string, 0, end-start+1)
	for i := start; i < end; i++ {
		r := rows[i]
		switch r.kind {
		case "provider":
			enabled := "off"
			if a.reply.Enabled[r.provider] {
				enabled = "on"
			}
			count, _ := a.accountCounts(r.provider)
			label := fmt.Sprintf("%s · rotation %s · %d account", accountProviderName(r.provider), enabled, count)
			if count != 1 {
				label += "s"
			}
			list = append(list, accountRowLine(accountChoice(label, i == a.focus), width))
		case "account":
			account := a.reply.Accounts[r.index]
			label := accountText(account.Label)
			if label == "" {
				label = accountProviderName(r.provider) + " account"
			}
			label += " · " + accountState(account)
			if len(account.Quota) > 0 || account.QuotaFetchState != "" {
				label += " · " + accountQuotaSummary(account)
			}
			list = append(list, accountRowLine(accountChoice(label+fmt.Sprintf(" · %d assigned", account.Assigned), i == a.focus), width))
		case "job":
			job := a.reply.Jobs[r.index]
			label := accountJobState(job)
			if job.State == "failed" {
				label = "Previous sign-in · failed"
			}
			list = append(list, accountRowLine(accountChoice(label, i == a.focus), width))
		}
	}
	if start > 0 || end < len(rows) {
		list = append(list, styleDim.Render("↑↓ more · PgUp/PgDn page"))
	}
	if height > 0 && len(header)+len(list)+len(guidance)+1 > height {
		// Long notices or very narrow wrapping must never evict the focus.
		header = header[:max(0, min(len(header), height-len(list)-2))]
		guidance = guidance[:max(0, min(len(guidance), height-len(header)-len(list)-1))]
	}
	return strings.Join(append(append(append(header, list...), ""), guidance...), "\n")
}

func accountRowLine(text string, width int) string {
	if width <= 0 {
		return text
	}
	return ansi.Truncate(text, width, "…")
}

// Full details and long browser URLs remain readable with PageUp/PageDown.
// The title stays visible while the content scrolls; the router owns the footer.
func accountScrolled(text string, height, scroll int) string {
	if height <= 0 {
		return text
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= height {
		return text
	}
	if height == 1 {
		return lines[0]
	}
	budget := max(0, height-2) // pinned title and paging reminder
	if scroll < 0 {
		scroll = 0
		for i, line := range lines {
			if strings.Contains(line, "▌") {
				scroll = max(0, i-budget)
				break
			}
		}
	}
	scroll = min(max(0, scroll), max(0, len(lines)-1-budget))
	end := min(len(lines), 1+scroll+budget)
	visible := append([]string{lines[0]}, lines[1+scroll:end]...)
	return strings.Join(append(visible, styleDim.Render("PgUp/PgDn scroll")), "\n")
}

func accountChoice(label string, focused bool) string {
	if focused {
		return styleAccent.Render("▌ " + label)
	}
	return "  " + label
}

func accountWrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	return ansi.Hardwrap(text, width, true)
}

func (w accountWizard) view(width int, lost, busy, loginSupported bool, methods map[string][]protocol.AccountMethodView) string {
	w.step = w.displayStep()
	var b strings.Builder
	titles := []string{"Provider", "Sign-in method", "Sign in", "Review identity", "Account label", "Confirm"}
	b.WriteString(styleTitle.Render("Add account") + fmt.Sprintf(" · %d/6 · %s\n\n", w.step+1, titles[w.step]))
	if w.targetID != "" {
		b.WriteString("Replacing credentials for the selected account.\n")
	}
	if lost {
		b.WriteString(styleError.Render("Daemon unavailable; sign-in may continue. Press r to reconnect.") + "\n")
	}
	if busy && w.job.ID == "" {
		b.WriteString(styleDim.Render("Waiting for daemon…") + "\n")
	}
	if w.err != "" {
		b.WriteString(styleError.Render(w.err) + "\n")
	}
	switch w.step {
	case accountStepProvider:
		b.WriteString("Choose the provider for your personal subscription.\n\n")
		for i, p := range []string{"Claude", "Codex"} {
			b.WriteString(accountChoice(p, w.providerFocus == i) + "\n")
		}
	case accountStepMethod:
		b.WriteString(accountProviderName(w.provider) + " · choose how to sign in.\n\n")
		if len(methods[w.provider]) == 0 {
			b.WriteString("No supported authentication method is available on this daemon.\n")
		}
		for i, method := range methods[w.provider] {
			label := accountText(method.Label)
			if label == "" {
				label = accountText(method.ID)
			}
			if !method.Available || method.ID == "native-login" && !loginSupported {
				label += " (unavailable)"
			}
			b.WriteString(accountChoice(label, w.methodFocus == i) + "\n")
			if w.methodFocus == i && method.Reason != "" {
				b.WriteString("    " + accountText(method.Reason) + "\n")
			}
			if w.methodFocus == i && method.Available && method.ID == "native-login" && !loginSupported {
				b.WriteString("    Native terminal sign-in is unavailable in this client.\n")
			}
		}
	case accountStepAuthenticate:
		switch {
		case w.job.State == "failed" || w.job.State == "cancelled":
			b.WriteString(accountJobState(w.job) + ".\nAdded accounts are unchanged.\n\n")
			switch w.method {
			case "token-manual":
				b.WriteString("Press r or Enter to enter a new token.\n")
			case "import-native":
				b.WriteString("Press r or Enter to choose a profile again.\n")
			default:
				b.WriteString("Press r or Enter to start a new sign-in.\n")
			}
		case w.job.State == "verifying" || w.job.State == "admitting" || w.job.State == "cancelling":
			b.WriteString(accountJobState(w.job) + "…\nPlease wait; no next step is available yet.\n")
		case w.job.ID == "" && w.method == "token-manual":
			b.WriteString("Run claude setup-token, then paste only its subscription token here.\nSurrounding space/tab/newlines are trimmed. Maximum 32 KiB.\n\n")
			masked := strings.Repeat("•", min(24, utf8.RuneCountInString(w.input.text))) + "█"
			b.WriteString("Token: " + masked + fmt.Sprintf(" (%d bytes)\n", len(w.input.text)))
			b.WriteString("\nOpaque tokens have unverified identity and effective settings.\nThey are unavailable for discussions until both can be verified.\nAn expired or revoked token must be replaced.\n")
		case w.job.ID == "" && w.method == "import-native":
			b.WriteString("Enter an owner-local native profile directory or subscription login file.\nThe daemon copies and verifies it in a private profile.\nMoving a refreshable login is safer than using both copies.\n\n")
			b.WriteString("Profile or login file: " + w.input.cursorView() + "\n")
		default:
			b.WriteString(accountJobState(w.job) + "\n")
			if w.job.VerificationURL != "" {
				b.WriteString("\nOpen this address in your browser:\n" + accountText(w.job.VerificationURL) + "\n")
			}
			if w.job.UserCode != "" {
				b.WriteString("\nEnter this code: " + accountText(w.job.UserCode) + "\n")
			}
			if accountCanAttach(w.job) {
				if loginSupported {
					b.WriteString("\nNext: Enter opens " + accountProviderName(w.provider) + " sign-in.\nFinish signing in there, then return here.\n")
				} else {
					b.WriteString("\nNative terminal attachment is unavailable in this client.\n")
				}
			}
			if w.job.Deadline != nil {
				b.WriteString("\nAttempt deadline: " + w.job.Deadline.Local().Format("15:04") + ".\n")
			}
			if w.job.VerificationURL != "" {
				b.WriteString("\nFinish browser sign-in; Swarm checks the result.\n")
			} else if w.job.LoginSocket == "" {
				b.WriteString("\nWaiting for sign-in details from the daemon.\n")
			}
		}
	case accountStepVerify:
		b.WriteString("Sign-in checked. Review the account before adding it.\n\n")
		b.WriteString("After adding, check quota in account details.\n\n")
		b.WriteString("Provider: " + accountProviderName(w.provider) + "\n")
		if w.job.Email != "" {
			b.WriteString("Email: " + accountText(w.job.Email) + "\n")
		} else {
			b.WriteString("Identity: unverified\n")
		}
		if w.job.Plan != "" {
			b.WriteString("Plan: " + accountText(w.job.Plan) + "\n")
		}
		if w.method == "token-manual" {
			b.WriteString("\nIdentity and effective settings are unverified; unavailable for discussions.\n")
		}
	case accountStepLabel:
		b.WriteString("Give this account a recognizable local label.\n\nLabel: " + w.label.cursorView() + "\n")
	case accountStepReview:
		b.WriteString("Provider: " + accountProviderName(w.provider) + "\nLabel: " + accountText(w.label.text) + "\n")
		if w.job.Email != "" {
			b.WriteString("Email: " + accountText(w.job.Email) + "\n")
		}
		if w.method == "token-manual" {
			b.WriteString("Identity and effective settings unverified · unavailable for discussions\n")
		}
		b.WriteString("\nEnter adds this account immediately. Automatic rotation is a separate provider setting.\n")
	}
	return accountWrap(b.String(), width)
}

func (w accountWizard) displayStep() int {
	if w.step >= accountStepVerify && w.job.State != "ready" {
		return accountStepAuthenticate
	}
	return w.step
}

func (a accountsModel) hint(lost, loginSupported bool, width int) string {
	if width > 0 && width < 42 {
		if a.wizardOpen {
			return "esc · ctrl+x cancel"
		}
		return "esc back"
	}
	if a.moveOpen {
		if lost {
			return "esc back · r reconnect"
		}
		return "↑↓ choose discussion · enter review/request move · esc back"
	}
	if a.wizardOpen {
		if lost {
			return "r reconnect · esc leave · ctrl+x cancel"
		}
		w := a.wizard
		if a.busy {
			return "esc leave · ctrl+x cancel"
		}
		switch w.displayStep() {
		case accountStepProvider, accountStepMethod:
			return "↑↓ choose · enter · esc · ctrl+x cancel"
		case accountStepAuthenticate:
			if w.job.State == "failed" || w.job.State == "cancelled" {
				return "r retry · esc leave · ctrl+x cancel"
			}
			if w.job.ID == "" && (w.method == "token-manual" || w.method == "import-native") {
				return "enter submit · esc leave · ctrl+x cancel"
			}
			if accountCanAttach(w.job) && loginSupported {
				return "enter sign in · esc leave · ctrl+x cancel"
			}
			return "r status · esc leave · ctrl+x cancel"
		case accountStepVerify:
			return "enter label · esc leave · ctrl+x cancel"
		case accountStepLabel:
			return "enter review · esc leave · ctrl+x cancel"
		case accountStepReview:
			return "enter add · esc leave · ctrl+x cancel"
		}
	}
	if lost {
		return "esc back · r reconnect"
	}
	if a.retireConfirm {
		return "esc cancel · y confirm"
	}
	if !a.available {
		return "esc back"
	}
	if !a.loaded || a.err != "" {
		return "r reload · a add · esc back"
	}
	if a.detail {
		return "↑↓ action · enter choose · esc back"
	}
	switch a.selected().kind {
	case "provider":
		return "enter toggle · a add · esc back"
	case "job":
		return "enter reopen · a add · esc back"
	default:
		return "enter details · a add · e rotate · esc"
	}
}
