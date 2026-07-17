package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
)

// attachModel is the Epic 7 attach placeholder. The router carries the selected
// session into it and it identifies that session (agent + cwd) on the thin top
// line, but the raw full-screen passthrough itself is Epic 8. Esc backs out to
// the general view.
type attachModel struct {
	session    protocol.SessionView
	hasSession bool
	width      int
}

func (m rootModel) updateAttach(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if k.Code == tea.KeyEsc {
		m.screen = screenGeneral
	}
	return m, nil
}

func (m attachModel) view() string {
	agent := m.session.Agent
	cwd := shortenCwd(m.session.Cwd)

	top := styleDim.Render("[ ") + styleTitle.Render(agent) +
		styleDim.Render(" · "+cwd) + m.rule(agent, cwd) + styleDim.Render(" ctrl+\\ detach ]")

	var b strings.Builder
	b.WriteString(top + "\n\n")
	b.WriteString(styleDim.Render("attach passthrough is Epic 8 — the agent CLI's own screen renders here."))
	return b.String()
}

// rule pads the top line out toward the detach hint with a horizontal rule.
func (m attachModel) rule(agent, cwd string) string {
	used := len("[ ") + len(agent) + len(" · ") + lenRunes(cwd) + len(" ctrl+\\ detach ]")
	n := m.width - used - 1
	if n < 1 {
		n = 1
	}
	return " " + styleDim.Render(strings.Repeat("─", n))
}

func lenRunes(s string) int { return len([]rune(s)) }
