package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/charmbracelet/x/ansi"
)

type boardMenu struct {
	target   string
	running  bool
	sel, top int
}
type menuLine struct{ text, key string }

func (m *rootModel) openMore() {
	target := ""
	if m.general.focusVisible() {
		target = m.general.selectedID()
	}
	m.general.more = &boardMenu{target: target}
	if s, ok := m.general.sessionByID(target); ok {
		m.general.more.running = s.Status.Process == status.ProcessRunning
	}
	// Start at the first command; preceding identity remains reachable with Up/Home/wheel.
	lines := m.moreLines()
	for i, l := range lines {
		if l.key != "" {
			m.general.more.sel = i
			break
		}
	}
	m.revealMore()
}
func (m rootModel) moreLines() []menuLine {
	var lines []menuLine
	if s, ok := m.general.sessionByID(m.general.more.target); ok {
		context := []string{"Name: " + displayName(s), "State: " + attentionToken(s), "Agent: " + s.Agent, "Repo: " + s.Cwd, "ID: " + s.ID}
		if s.BackendPlanError != "" {
			context = append(context, "No backend: "+s.BackendPlanError)
		}
		if s.ResumeError != "" {
			context = append(context, "Resume failed: "+s.ResumeError)
		}
		if s.SupervisionPending {
			context = append(context, "Supervision: "+supervisionMarker(s, m.general.sessions))
		}
		for _, text := range context {
			for _, part := range strings.Split(ansi.Hardwrap(text, max(1, m.width-2), true), "\n") {
				lines = append(lines, menuLine{text: part})
			}
		}
		lines = append(lines, menuLine{"Rename", "e"}, menuLine{"Tag", "t"})
		if !m.general.more.running {
			key := "r"
			if s.Status.Process == status.ProcessRunning {
				key = ""
			}
			lines = append(lines, menuLine{"Resume", key})
		}
		lines = append(lines, menuLine{"Handoff", "h"})
		label := "Delete"
		if s.Status.Process == status.ProcessRunning {
			label = "Kill"
		}
		if (s.Status.Process == status.ProcessRunning) == m.general.more.running {
			lines = append(lines, menuLine{label, "x"})
		} else {
			lines = append(lines, menuLine{text: "Target state changed; reopen More"})
		}
	} else if m.general.more.target != "" {
		lines = append(lines, menuLine{text: "Target disappeared"})
	}
	return append(lines, menuLine{"Options", "o"}, menuLine{"Quit", "q"})
}
func (m rootModel) moreBudget() int {
	if m.height <= 0 {
		return 20
	}
	return max(1, m.height-2)
}
func (m *rootModel) revealMore() {
	menu := m.general.more
	lines := m.moreLines()
	menu.sel = max(0, min(menu.sel, len(lines)-1))
	if menu.sel < menu.top {
		menu.top = menu.sel
	}
	if menu.sel >= menu.top+m.moreBudget() {
		menu.top = menu.sel - m.moreBudget() + 1
	}
	menu.top = max(0, min(menu.top, max(0, len(lines)-m.moreBudget())))
}
func (m rootModel) moreView() string {
	if m.width > 0 && (m.width < 24 || m.height > 0 && m.height < 4) {
		return boundedBoard("Need 24×4 · esc back", m.width, m.height)
	}
	menu := m.general.more
	label := "More"
	if s, ok := m.general.sessionByID(menu.target); ok {
		label += " · " + displayName(s) + " · " + attentionToken(s)
	} else if menu.target != "" {
		label += " · target disappeared"
	}
	lines := []string{ellipsis(label, m.width)}
	items := m.moreLines()
	for i := menu.top; i < len(items) && i < menu.top+m.moreBudget(); i++ {
		prefix := "  "
		if i == menu.sel {
			prefix = "▌ "
		}
		lines = append(lines, prefix+ellipsis(items[i].text, m.width-2))
	}
	if m.height > 0 {
		for len(lines) < m.height-1 {
			lines = append(lines, "")
		}
	}
	lines = append(lines, "↑↓ scroll · esc back")
	return boundedBoard(strings.Join(lines, "\n"), m.width, m.height)
}
func (m rootModel) updateMore(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	menu := m.general.more
	switch {
	case k.Code == tea.KeyEsc:
		m.general.more = nil
	case k.Code == tea.KeyDown || k.Text == "j":
		menu.sel++
		m.revealMore()
	case k.Code == tea.KeyUp || k.Text == "k":
		menu.sel--
		m.revealMore()
	case k.Code == tea.KeyPgDown:
		menu.sel += m.moreBudget()
		m.revealMore()
	case k.Code == tea.KeyPgUp:
		menu.sel -= m.moreBudget()
		m.revealMore()
	case k.Code == tea.KeyHome:
		menu.sel = 0
		m.revealMore()
	case k.Code == tea.KeyEnd:
		menu.sel = len(m.moreLines()) - 1
		m.revealMore()
	case k.Code == tea.KeyEnter:
		if m.width < 24 || m.height > 0 && m.height < 4 {
			return m, nil
		}
		lines := m.moreLines()
		if menu.sel < menu.top || menu.sel >= menu.top+m.moreBudget() {
			m.revealMore()
			return m, nil
		}
		if menu.sel >= 0 && menu.sel < len(lines) {
			return m.activateMore(lines[menu.sel].key)
		}
	}
	return m, nil
}
func (m rootModel) activateMore(key string) (tea.Model, tea.Cmd) {
	if key == "" || m.general.more == nil {
		return m, nil
	}
	target := m.general.more.target
	eligible := false
	for _, l := range m.moreLines() {
		if l.key == key {
			eligible = true
		}
	}
	if !eligible {
		return m, nil
	}
	if key == "q" {
		return m, tea.Quit
	}
	if m.connectionLost {
		return m, nil
	}
	m.general.more = nil
	if key == "o" {
		return m.updateGeneral(tea.KeyPressMsg{Text: "o"})
	}
	if _, ok := m.general.sessionByID(target); !ok {
		return m, m.general.setBanner("menu target disappeared")
	}
	// The menu's named context is visible even when its captured row moves off-board.
	m.general.commandID = target
	var next tea.Model
	var cmd tea.Cmd
	if key == "x" {
		next, cmd = m.updateGeneral(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	} else {
		next, cmd = m.updateGeneral(tea.KeyPressMsg{Text: key})
	}
	updated := next.(rootModel)
	updated.general.commandID = ""
	return updated, cmd
}
func (m rootModel) boardCommandSession() (protocol.SessionView, bool) {
	if m.general.commandID != "" {
		return m.general.sessionByID(m.general.commandID)
	}
	return m.general.selected()
}
func (m rootModel) moreTarget(x, y int) boardGesture {
	menu := m.general.more
	g := boardGesture{}
	if y < 1 || y > m.moreBudget() {
		return g
	}
	i := menu.top + y - 1
	lines := m.moreLines()
	if i >= len(lines) || lines[i].key == "" {
		return g
	}
	return boardGesture{kind: actionMenu, id: lines[i].key, x0: 0, x1: m.width, y: y, generation: m.general.geometry, pressX: x, pressY: y}
}
func (m *rootModel) scrollMore(mouse tea.Mouse) {
	if mouse.Y < 1 || mouse.Y > m.moreBudget() || mouse.X < 0 || mouse.X >= m.width {
		return
	}
	delta := 0
	if mouse.Button == tea.MouseWheelDown {
		delta = 1
	}
	if mouse.Button == tea.MouseWheelUp {
		delta = -1
	}
	menu := m.general.more
	menu.top = max(0, min(menu.top+delta, max(0, len(m.moreLines())-m.moreBudget())))
}

func (m rootModel) moreSelectionKey() string {
	if m.general.more == nil {
		return ""
	}
	lines := m.moreLines()
	i := m.general.more.sel
	if i >= 0 && i < len(lines) {
		return lines[i].key
	}
	return ""
}
func (m *rootModel) restoreMoreSelection(key string) {
	if key == "" || m.general.more == nil {
		return
	}
	for i, line := range m.moreLines() {
		if line.key == key {
			m.general.more.sel = i
			return
		}
	}
}
