package tui

import (
	"fmt"
	"strings"

	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// formNavigation is reading position, never an editor snapshot. Reflow cannot
// replace the drafts or rune cursors owned by the existing form models.
type formNavigation struct {
	key  string
	stop int
}

type formContent struct {
	body, target, field, context, hint string
	decision                           bool
}

func formFocusedLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		plain := ansi.Strip(line)
		if strings.HasPrefix(plain, "▌ ") || strings.HasPrefix(plain, "> ") {
			return line
		}
	}
	return ""
}

func sessionFormContext(sID, name, agent, cwd string) string {
	return fmt.Sprintf("Session: %s\nAgent: %s\nID: %s\nDirectory: %s", name, agent, sID, cwd)
}

func (m rootModel) activeForm() (f formContent, ok bool) {
	switch {
	case m.pairing != nil:
		p := m.pairing
		f = formContent{body: p.view(), target: "Pair device: " + p.deviceName, context: "Device: " + p.deviceName + "\nVerify this code matches the one shown on the device:\n" + strings.Join(p.sas, " "), hint: "y/enter allow · n/esc deny", decision: true}
	case m.screen == screenLaunch:
		f = formContent{body: m.launch.view(), target: "New session · " + m.launch.currentAgentName(), context: m.launch.errMsg, hint: m.launch.hint()}
		if note := m.launch.agentNote(); note != "" {
			f.context = strings.TrimSpace(note) + "\n" + f.context
		}
	case m.screen == screenResume:
		r := m.resume
		f = formContent{body: r.view(), target: "Relocate: " + displayName(r.source), context: sessionFormContext(r.source.ID, displayName(r.source), r.source.Agent, r.source.Cwd) + "\n" + r.reason + "\n" + r.directory.errMsg, hint: r.hint()}
	case m.screen == screenHandoff:
		h := m.handoff
		name := h.sourceID
		if s, exists := m.general.sessionByID(h.sourceID); exists {
			name = displayName(s)
		}
		f = formContent{body: h.view(), target: "Handoff: " + name + " → " + h.targetName(), context: sessionFormContext(h.sourceID, name, h.sourceAgent, h.sourceCwd), hint: h.hint(), decision: h.confirmPending()}
		if h.method == handoffMethodHandsOff && h.sourceRunning {
			f.context += "\n" + handoffRunningWarning
		}
		if h.confirmPending() {
			f.context += "\n" + h.crossCLIQuestion()
		}
		if h.errMsg != "" {
			f.context += "\n" + h.errMsg
		}
	case m.screen == screenOptions:
		f = formContent{body: m.options.view(m.width), target: "Options", hint: m.options.hint(), context: m.options.contextGuard.err}
		if m.options.contextGuard.available {
			f.context = "Context guard: Codex only · compacts when quiet and unattended, then continues the task\n" + f.context
		}
		if m.options.focus == optionsFocusTouchBack || m.options.touchBack {
			f.context = touchBackSelectionNotice + "\n" + f.context
		}
	case m.screen == screenLogins:
		l := m.logins
		f = formContent{body: l.view(m.width, m.boardBodyRows(), m.connectionLost, m.loginRunner != nil), target: "Provider logins", hint: l.hint(m.connectionLost, m.loginRunner != nil, m.width), context: l.err}
		if l.mode == "label" {
			f.field = "Label  " + editViewport(l.label.text, l.label.cursor, max(1, m.width-7))
			f.target = "Sign in: " + providerLabel(l.provider)
		}
		if l.mode == "provider" {
			f.field = "Provider  " + picker([]string{"Claude Code", "Codex"}, boolIndex(l.provider == "codex"), m.width-10)
		}
		if i := renderedLoginIndex(l.reply.Logins, l.focus); i >= 0 && l.mode == "" && l.job.ID == "" {
			login := l.reply.Logins[i]
			f.target = providerLabel(login.Provider) + ": " + login.Label
			f.context = "Login: " + login.Label + "\nID: " + login.ID + "\nState: " + login.State + "\n" + f.context
		}
		if l.job.ID != "" {
			f.target = "Sign in: " + providerLabel(l.job.Provider)
			f.context = l.job.VerificationURL + "\n" + l.job.UserCode + "\n" + l.job.Message + "\n" + loginJobStatus(l.job.State) + "\n" + f.context
		}
	case m.screen == screenGeneral && (m.general.editing || m.general.confirm):
		id, action := m.general.editID, "Rename"
		if m.general.editTag {
			action = "Tag"
		}
		if m.general.confirm {
			id, action = m.general.confirmID, "Delete"
			if m.general.confirmKill {
				action = "Kill"
			}
		}
		s, exists := m.general.sessionByID(id)
		name := displayName(s)
		if !exists {
			name = "session no longer available"
		}
		f = formContent{target: action + ": " + name, context: sessionFormContext(id, name, s.Agent, s.Cwd), hint: "enter save · esc cancel", decision: m.general.confirm}
		if m.general.confirm {
			f.context += "\n" + action + " this session?"
			f.hint = "y / ctrl+x confirm · n / esc cancel"
		} else {
			f.field = "▌ " + action + "  " + editViewport(m.general.edit.text, m.general.edit.cursor, max(1, m.width-len(action)-4))
		}
		f.body = m.general.view() + "\n" + f.context
	default:
		return f, false
	}
	if f.field == "" {
		f.field = formFocusedLine(f.body)
	}
	f.context = strings.TrimSpace(f.context)
	return f, true
}

func (m rootModel) formTooSmall() bool {
	return m.width > 0 && m.width < 32 || m.height > 0 && m.height < 8
}

func (m rootModel) formReading(f formContent) (lines []string, budget int, key string) {
	width := m.width
	if width <= 0 {
		width = 80
	}
	for _, line := range strings.Split(f.context, "\n") {
		lines = append(lines, strings.Split(ansi.Wrap(line, width, ""), "\n")...)
	}
	if f.context == "" {
		lines = nil
	}
	budget = m.boardBodyRows() - 1 // pinned target/action
	if f.field != "" {
		budget--
	}
	budget = max(1, budget)
	key = f.target + "\x00" + f.context
	return
}

func (m rootModel) formStop(key string, count int) int {
	if m.formNavigation.key != key {
		return 0
	}
	return min(m.formNavigation.stop, count)
}

// guardFormInput covers all initiating keys in one place; polling and accepted
// asynchronous results do not pass through it. Escape/deny remains unconditional.
func (m *rootModel) guardFormInput(k tea.KeyPressMsg) bool {
	f, ok := m.activeForm()
	if !ok {
		return false
	}
	if k.Code == tea.KeyEsc || f.decision && k.Text == "n" {
		m.formNavigation = formNavigation{}
		return false
	}
	mutation := k.Code == tea.KeyEnter || f.decision && (k.Text == "y" || isCtrlX(k))
	if m.pairing != nil {
		mutation = pairingApprovalKey(k)
	}
	if m.screen == screenLogins && m.logins.mode == "" {
		mutation = mutation || k.Text == "c" || k.Text == "d" || k.Text == "s"
	}
	if mutation && m.screen == screenGeneral && m.general.editing {
		if _, exists := m.general.sessionByID(m.general.editID); !exists {
			return true
		}
	}
	if m.formTooSmall() {
		return mutation
	}
	if m.height <= 0 {
		return false
	}
	lines, budget, key := m.formReading(f)
	if len(lines) <= budget {
		m.formNavigation = formNavigation{}
		return false
	}
	stop := m.formStop(key, len(lines))
	switch k.Code {
	case tea.KeyDown, tea.KeyTab:
		if !f.decision {
			return false
		}
		stop = min(len(lines), stop+1)
	case tea.KeyUp:
		if !f.decision {
			return false
		}
		stop = max(0, stop-1)
	case tea.KeyPgDown:
		stop = min(len(lines), stop+budget)
	case tea.KeyPgUp:
		stop = max(0, stop-budget)
	case tea.KeyHome:
		if !f.decision {
			return false
		}
		stop = 0
	case tea.KeyEnd:
		if !f.decision {
			return false
		}
		stop = len(lines)
	default:
		if mutation && stop == len(lines) {
			m.formNavigation = formNavigation{}
			return false
		}
		return mutation && stop != len(lines)
	}
	m.formNavigation = formNavigation{key: key, stop: stop}
	return true
}

func (m rootModel) composeActiveForm() string {
	f, _ := m.activeForm()
	if m.formTooSmall() {
		body := "Resize to 32×8"
		if f.field != "" {
			body += "\n" + f.field
		}
		return m.boundForm(m.composeBoard(body+"\nEsc back / n deny", "esc back"))
	}
	if m.height <= 0 {
		return m.composeBoard(f.body, m.formHint(f.hint))
	}
	lines, budget, key := m.formReading(f)
	if len(lines) > budget {
		stop := m.formStop(key, len(lines))
		confirmation := "Confirm · enter"
		if f.decision {
			confirmation = "Confirm · y"
		}
		if m.pairing != nil {
			confirmation = "Allow · y / enter"
		}
		if m.screen == screenGeneral && m.general.confirm {
			confirmation = "Confirm · y / ctrl+x"
		}
		if m.screen == screenLogins && m.logins.mode == "" && m.logins.job.ID == "" {
			confirmation = "Actions · enter / c/d/s"
		}
		if stop == len(lines) {
			confirmation = "▌ " + confirmation
		} else {
			confirmation = "  " + confirmation
		}
		all := append(append([]string{}, lines...), confirmation)
		start := min(stop, max(0, len(all)-budget))
		body := ansi.Truncate(f.target, m.width, "")
		if f.field != "" {
			body += "\n" + ansi.Truncate(f.field, m.width, "")
		}
		body += "\n" + strings.Join(all[start:min(len(all), start+budget)], "\n")
		hint := "pgup/down read · esc back"
		if f.decision {
			hint = "↑↓/tab read · end confirm · esc back"
		}
		return m.boundForm(m.composeBoard(body, m.formHint(hint)))
	}
	// Preserve the established wide form when it already fits. On a short screen,
	// pin the active field and required context before spending rows on neighbors.
	raw := strings.Split(strings.TrimRight(f.body, "\n"), "\n")
	fitsWidth := true
	for _, line := range raw {
		if m.width > 0 && ansi.StringWidth(line) > m.width {
			fitsWidth = false
			break
		}
	}
	if len(raw) <= m.boardBodyRows() && fitsWidth && m.screen != screenHandoff {
		return m.boundForm(m.composeBoard(f.body, m.formHint(f.hint)))
	}
	body := []string{ansi.Truncate(f.target, m.width, "")}
	if f.field != "" {
		body = append(body, ansi.Truncate(f.field, m.width, ""))
	}
	body = append(body, lines...)
	remaining := m.boardBodyRows() - len(body)
	for i, line := range raw {
		if f.decision || remaining <= 0 {
			break
		}
		if i == 0 || strings.TrimSpace(ansi.Strip(line)) == "" || line == f.field {
			continue
		}
		body = append(body, ansi.Truncate(line, m.width, ""))
		remaining--
	}
	return m.boundForm(m.composeBoard(strings.Join(body, "\n"), m.formHint(f.hint)))
}

// Keep submit/cancel visible before editing shortcuts consume a narrow footer.
func (m rootModel) formHint(hint string) string {
	if m.width <= 0 || ansi.StringWidth(hint) <= m.width-2 {
		return hint
	}
	parts := strings.Split(hint, " · ")
	back := "esc back"
	var actions, other []string
	for _, part := range parts {
		switch {
		case strings.Contains(part, "esc"):
			if strings.Contains(part, "deny") {
				back = "n/esc deny"
			} else if strings.Contains(part, "cancel") {
				back = "esc cancel"
			}
		case strings.Contains(part, "enter"), strings.Contains(part, "confirm"):
			actions = append(actions, part)
		default:
			other = append(other, part)
		}
	}
	ordered := append([]string{back}, actions...)
	return ansi.Truncate(strings.Join(append(ordered, other...), " · "), max(0, m.width-2), "…")
}

func (m rootModel) boundForm(body string) string {
	if m.width <= 0 {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, m.width, "")
	}
	return strings.Join(lines, "\n")
}

func (m rootModel) hasActiveForm() bool {
	return m.pairing != nil || m.screen == screenLaunch || m.screen == screenResume || m.screen == screenHandoff || m.screen == screenOptions || m.screen == screenLogins || m.screen == screenGeneral && (m.general.editing || m.general.confirm)
}
