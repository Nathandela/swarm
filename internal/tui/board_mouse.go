package tui

import tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"

type boardAction uint8

const (
	actionNone boardAction = iota
	actionOpen
	actionFold
	actionAttention
	actionMenu
	actionCommand
)

type boardGesture struct {
	kind           boardAction
	id             string
	key            string
	label          string
	x0, x1, y      int
	generation     uint64
	pressX, pressY int
}

func (m rootModel) boardTarget(x, y int) boardGesture {
	if x < 0 || x >= m.width || y < 0 || y >= m.height {
		return boardGesture{}
	}
	g := boardGesture{x0: 0, x1: m.width, y: y, generation: m.general.geometry, pressX: x, pressY: y}
	if m.general.more != nil {
		return m.moreTarget(x, y)
	}
	if y == 0 {
		g.kind = actionAttention
		return g
	}
	if y == m.height-1 {
		for _, b := range m.boardButtons() {
			if x >= b.x && x < b.x+b.width {
				g.kind, g.key, g.label = actionCommand, b.key, b.text()
				g.x0, g.x1 = b.x, b.x+b.width
				g.id = m.general.selectedID()
				return g
			}
		}
		return g
	}
	l, ok := m.boardLineAt(y)
	if !ok {
		return boardGesture{}
	}
	switch l.kind {
	case lineSession:
		g.kind = actionOpen
		g.id = l.session.ID
	case lineSection:
		g.kind = actionFold
		g.id = l.section
	}
	return g
}
func (m rootModel) updateBoardMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !m.boardTakesMouse() || !m.boardUsable() {
		m.general.gesture = boardGesture{}
		m.general.openID = ""
		return m, nil
	}
	mouse := msg.Mouse()
	switch msg.(type) {
	case tea.MouseWheelMsg:
		m.general.gesture = boardGesture{}
		m.general.openID = ""
		if m.general.more != nil {
			m.scrollMore(mouse)
			return m, nil
		}
		start, rows, _, _ := m.boardGeometry()
		if mouse.X < 0 || mouse.X >= m.width || mouse.Y < start || mouse.Y >= start+rows {
			return m, nil
		}
		if mouse.Button == tea.MouseWheelUp {
			m.general.scroll(-1)
		}
		if mouse.Button == tea.MouseWheelDown {
			m.general.scroll(1)
		}
	case tea.MouseMotionMsg:
		if mouse.X != m.general.gesture.pressX || mouse.Y != m.general.gesture.pressY {
			m.general.gesture = boardGesture{}
			m.general.openID = ""
		}
	case tea.MouseClickMsg:
		m.general.gesture = boardGesture{}
		m.general.openID = ""
		if mouse.Button != tea.MouseLeft {
			return m, nil
		}
		g := m.boardTarget(mouse.X, mouse.Y)
		if g.kind == actionOpen && g.id != "" && m.general.selectedID() != g.id {
			m.general.focusSection = ""
			m.general.restoreSel(g.id)
			return m, nil
		}
		m.general.gesture = g
		if g.kind == actionOpen {
			m.general.openID = g.id
		}
	case tea.MouseReleaseMsg:
		g := m.general.gesture
		m.general.gesture = boardGesture{}
		m.general.openID = ""
		if mouse.Button != tea.MouseLeft && mouse.Button != tea.MouseNone {
			return m, nil
		}
		if g.kind == actionNone || g.generation != m.general.geometry || mouse.X < g.x0 || mouse.X >= g.x1 || mouse.Y != g.y {
			return m, nil
		}
		target := m.boardTarget(mouse.X, mouse.Y)
		if target.kind != g.kind || target.id != g.id || target.key != g.key || target.label != g.label {
			return m, nil
		}
		switch g.kind {
		case actionOpen:
			return m.updateGeneral(tea.KeyPressMsg{Code: tea.KeyEnter})
		case actionFold:
			m.general.fold(g.id, !m.general.isCollapsed(g.id))
		case actionAttention:
			m.general.revealAttention()
		case actionMenu:
			return m.activateMore(g.id)
		case actionCommand:
			return m.updateGeneral(actionKey(g.key))
		}
	}
	return m, nil
}

// Mouse reports lack displayed-frame identity: an old-frame press can resolve
// against current geometry. This generation only binds a completed model gesture.
func (m rootModel) boardShape() uint64 {
	h := uint64(1469598103934665603)
	add := func(n int) { h ^= uint64(n); h *= 1099511628211 }
	str := func(s string) {
		for i := range s {
			h ^= uint64(s[i])
			h *= 1099511628211
		}
	}
	add(m.width)
	add(m.height)
	add(int(m.screen))
	add(m.general.top)
	str(m.general.selectedID())
	str(m.general.focusSection)
	if s, ok := m.general.selected(); ok {
		str(string(s.Status.Process))
	}
	for _, b := range m.boardButtons() {
		str(b.text())
		add(b.x)
		add(b.width)
	}
	if m.connectionLost {
		add(101)
	}
	if m.pairing != nil {
		add(102)
	}
	if m.general.editing {
		add(103)
	}
	if m.general.confirm {
		add(104)
	}
	start, rows, _, _ := m.boardGeometry()
	add(start)
	add(rows)
	if m.general.more != nil {
		add(105)
		str(m.general.more.target)
		if s, ok := m.general.sessionByID(m.general.more.target); ok {
			str(string(s.Status.Process))
		} else {
			add(106)
		}
		add(m.general.more.top)
		for _, l := range m.moreLines() {
			str(l.text)
			str(l.key)
		}
	}
	m.general.content(func(l boardLine) { add(int(l.kind)); str(l.section); str(l.session.ID) })
	return h
}
func (m *rootModel) syncBoardGeometry(previous uint64) {
	start, rows, _, _ := m.boardGeometry()
	m.general.contentStart = start
	m.general.contentRows = rows
	m.general.clampTop()
	// A one-line content budget must show a navigation stop, rather than its expanded heading.
	if rows == 1 {
		l, ok := m.boardLineAt(start)
		if ok && (l.kind == lineSpacer || l.kind == lineSection && !m.general.isCollapsed(l.section)) {
			m.general.top++
			m.general.clampTop()
		}
	}
	if previous != m.boardShape() {
		m.general.geometry++
		m.general.gesture = boardGesture{}
		m.general.openID = ""
	}
}
