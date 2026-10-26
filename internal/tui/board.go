package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/charmbracelet/x/ansi"
)

type foldKey struct {
	mode    groupingMode
	section string
}

func (m generalModel) isCollapsed(section string) bool {
	return m.collapsed[foldKey{m.grouping, section}]
}
func (m generalModel) content(visit func(boardLine)) {
	started := false
	m.layout(0, func(l boardLine) {
		if l.kind == lineSection {
			started = true
		}
		if started && (l.kind == lineSection || l.kind == lineSession || l.kind == lineSpacer && !m.compactRows()) {
			visit(l)
		}
	})
}
func (m generalModel) navigation(visit func(boardLine, int)) {
	i := 0
	m.content(func(l boardLine) {
		if l.kind == lineSession || l.kind == lineSection && m.isCollapsed(l.section) {
			visit(l, i)
			i++
		}
	})
}
func (m generalModel) navigationCount() int {
	n := 0
	m.navigation(func(boardLine, int) { n++ })
	return n
}
func (m *generalModel) syncHeaderFocus() {
	m.focusSection = ""
	m.navigation(func(l boardLine, i int) {
		if i == m.sel && l.kind == lineSection {
			m.focusSection = l.section
		}
	})
}
func (m generalModel) contentLength() int { n := 0; m.content(func(boardLine) { n++ }); return n }
func (m *generalModel) clampTop() {
	m.top = max(0, min(m.top, max(0, m.contentLength()-max(1, m.contentRows))))
}
func (m generalModel) focusLine() int {
	result := -1
	y := 0
	id := m.selectedID()
	m.content(func(l boardLine) {
		if l.kind == lineSession && l.session.ID == id || l.kind == lineSection && l.section == m.focusSection {
			result = y
		}
		y++
	})
	return result
}
func (m *generalModel) revealFocus() {
	y := m.focusLine()
	if y < 0 {
		return
	}
	if y < m.top {
		m.top = y
	}
	if y >= m.top+max(1, m.contentRows) {
		m.top = y - max(1, m.contentRows) + 1
	}
	m.clampTop()
}
func (m generalModel) focusVisible() bool {
	if m.contentRows <= 0 {
		return m.navigationCount() > 0
	}
	y := m.focusLine()
	return y >= m.top && y < m.top+max(1, m.contentRows)
}
func (m *generalModel) actionVisible() bool {
	if !m.focusVisible() {
		m.revealFocus()
		return false
	}
	return true
}
func (m *generalModel) fold(section string, collapsed bool) {
	oldY := -1
	i := 0
	m.content(func(l boardLine) {
		if l.kind == lineSection && l.section == section {
			oldY = i - m.top
		}
		i++
	})
	if m.collapsed == nil {
		m.collapsed = make(map[foldKey]bool)
	}
	m.collapsed[foldKey{m.grouping, section}] = collapsed
	m.focusSection = section
	if !collapsed {
		for _, s := range m.sessions {
			if m.groupKey(s) == section {
				m.focusSection = ""
				m.restoreSel(s.ID)
				break
			}
		}
	} else {
		m.restoreSel("")
	}
	i = 0
	m.content(func(l boardLine) {
		if l.kind == lineSection && l.section == section && oldY >= 0 {
			m.top = i - oldY
		}
		i++
	})
	m.clampTop()
}
func (m *generalModel) revealAttention() {
	current := m.selectedID()
	next, first := "", ""
	passed := current == ""
	for _, section := range m.sectionOrder() {
		for _, s := range m.sessions {
			if m.groupKey(s) != section {
				continue
			}
			if bannerGroup(s.Group) {
				if first == "" {
					first = s.ID
				}
				if passed && next == "" && s.ID != current {
					next = s.ID
				}
			}
			if s.ID == current {
				passed = true
			}
		}
	}
	if next == "" {
		next = first
	}
	if next == "" {
		return
	}
	s, _ := m.sessionByID(next)
	if m.isCollapsed(m.groupKey(s)) {
		m.collapsed[foldKey{m.grouping, m.groupKey(s)}] = false
	}
	m.focusSection = ""
	m.restoreSel(next)
	m.revealFocus()
}
func boardSessionKey(k tea.KeyPressMsg) bool {
	return k.Code == tea.KeyEnter || k.Text == "r" || k.Text == "e" || k.Text == "t" || k.Text == "h" || isCtrlX(k)
}
func (m *rootModel) boardNavigation(k tea.KeyPressMsg) bool {
	switch {
	case k.Code == tea.KeyDown || k.Text == "j":
		m.general.move(1)
	case k.Code == tea.KeyUp || k.Text == "k":
		m.general.move(-1)
	case k.Code == tea.KeyPgDown || k.Code == tea.KeyPgUp:
		delta := max(1, m.general.contentRows)
		if k.Code == tea.KeyPgUp {
			delta = -delta
		}
		m.general.sel = max(0, min(m.general.sel+delta, m.general.navigationCount()-1))
		m.general.syncHeaderFocus()
		m.general.revealFocus()
	case k.Code == tea.KeyHome || k.Code == tea.KeyEnd:
		m.general.sel = 0
		if k.Code == tea.KeyEnd {
			m.general.sel = max(0, m.general.navigationCount()-1)
		}
		m.general.syncHeaderFocus()
		m.general.revealFocus()
	case k.Code == tea.KeyLeft:
		if s, ok := m.general.selected(); ok {
			m.general.fold(m.general.groupKey(s), true)
			m.general.revealFocus()
		}
	case k.Code == tea.KeyRight || k.Code == tea.KeyEnter && m.general.focusSection != "":
		if m.general.focusSection != "" {
			m.general.fold(m.general.focusSection, false)
			m.general.revealFocus()
		}
	default:
		return false
	}
	return true
}
func (m generalModel) compactRows() bool {
	c := rowColumnsFor(m.width, 2)
	return c.name < 16 || c.cwd < 12 || c.summary < 12
}
func ellipsis(s string, n int) string { return ansi.Truncate(s, max(0, n), "…") }
func (m generalModel) compactRow(s protocol.SessionView, selected bool) string {
	prefix := "  "
	if selected {
		prefix = styleAccent.Render("▌") + " "
	}
	state := attentionToken(s)
	switch state {
	case "needs input":
		state = "input"
	case "ready for review":
		state = "review"
	case "completed":
		state = "ended"
	}
	markers := ""
	if s.BackendPlanError != "" {
		markers += " !backend"
	}
	if s.ResumeError != "" {
		markers += " !resume"
	}
	if s.SupervisionPending {
		markers += " !supervision"
	}
	if markers != "" {
		// Keep a failure indication even when several diagnostics compete for one row.
		state += " " + ellipsis(strings.TrimSpace(markers), max(1, m.width-8-ansi.StringWidth(state)))
	}
	// State remains readable; diagnostic details live in the existing doctor/ls surfaces.
	budget := max(0, m.width-4)
	state = ellipsis(state, max(0, budget-5))
	nameWidth := max(0, budget-ansi.StringWidth(state)-1)
	name := displayName(s)
	if name == "" {
		name = s.ID
	}
	return prefix + groupStyle(s.Group).Render(groupIcon(s.Group, m.spinnerFrame)) + " " + styleAgent.Render(padRight(ellipsis(name, nameWidth), nameWidth)) + " " + groupStyle(s.Group).Render(state)
}

// boardGeometry reserves pinned chrome before traversing the shared grouped content.
func (m rootModel) boardGeometry() (start, rows int, banner []string, notice bool) {
	height := m.height
	if height <= 0 {
		height = 1000000
	}
	start = 1
	if height >= 12 {
		start++
	}
	banner = m.boardBannerLines()
	available := max(0, height-start-2) // footer and at least one content line
	if len(banner) > available {
		banner = banner[:available]
	}
	start += len(banner)
	notice = skewNotice(m.daemonVersion, m.clientVersion) != "" && height-start-2 >= 1
	rows = max(0, height-start-1)
	if notice {
		rows--
	}
	return
}
func (m rootModel) boardBannerLines() []string {
	if m.general.bannerMandatory {
		return m.general.renderBanner(0)
	}
	return m.general.bannerLines()
}
func (m rootModel) boardUsable() bool {
	if m.width > 0 && (m.width < 24 || m.height > 0 && m.height < 4) {
		return false
	}
	if m.general.more != nil || m.height <= 0 {
		return true
	}
	_, _, banner, _ := m.boardGeometry()
	return !m.general.bannerMandatory || len(banner) == len(m.boardBannerLines())
}
func (m rootModel) boardView() string {
	if m.general.editing || m.general.confirm {
		return m.composeBoard(m.general.view(), m.generalStatus())
	}
	if m.general.more != nil {
		return m.moreView()
	}
	if m.width > 0 && (m.width < 24 || m.height > 0 && m.height < 4) {
		return boundedBoard("Need 24×4 · esc quit", m.width, m.height)
	}
	if !m.boardUsable() {
		lines := []string{m.boardHeader()}
		banner := m.boardBannerLines()
		for i := 0; i < len(banner) && len(lines) < m.height-1; i++ {
			lines = append(lines, banner[i])
		}
		footer := "Resize to read · esc quit"
		if m.connectionLost {
			footer = m.generalStatus()
		}
		return boundedBoard(strings.Join(append(lines, footer), "\n"), m.width, m.height)
	}
	start, rows, banner, notice := m.boardGeometry()
	lines := []string{m.boardHeader()}
	if m.height >= 12 {
		lines = append(lines, "")
	}
	lines = append(lines, banner...)
	n := 0
	m.general.content(func(l boardLine) {
		if n >= m.general.top && n < m.general.top+rows {
			text := ""
			switch l.kind {
			case lineSection:
				text = m.boardSection(l.section)
			case lineSession:
				text = m.general.renderRow(l.session, l.session.Group, l.idx == m.general.sel && m.general.focusSection == "")
			}
			lines = append(lines, text)
		}
		n++
	})
	if m.height <= 0 {
		return strings.Join(append(lines, m.boardStrip()), "\n")
	}
	for len(lines) < start+rows {
		lines = append(lines, "")
	}
	if notice {
		lines = append(lines, ellipsis(skewNotice(m.daemonVersion, m.clientVersion), m.width))
	}
	lines = append(lines, m.boardStrip())
	return boundedBoard(strings.Join(lines, "\n"), m.width, m.height)
}
func boundedBoard(body string, width, height int) string {
	lines := strings.Split(body, "\n")
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		if width > 0 {
			lines[i] = ansi.Truncate(lines[i], width, "")
		}
	}
	return strings.Join(lines, "\n")
}
func (m rootModel) boardHeader() string {
	needs, review := 0, 0
	for _, s := range m.general.sessions {
		if s.Group == status.GroupNeedsInput {
			needs++
		}
		if s.Group == status.GroupReadyForReview {
			review++
		}
	}
	counts := itoa(needs) + " input · " + itoa(review) + " review"
	title := "swarm · "
	if m.width > 0 && ansi.StringWidth(title+counts) > m.width {
		title = ""
	}
	text := styleTitle.Render(title) + styleDim.Render(counts)
	if m.width >= 72 {
		text += styleDim.Render(" · " + m.general.layoutLabel())
	}
	return ellipsis(text, m.width)
}
func (m rootModel) boardSection(section string) string {
	count, needs, review := 0, 0, 0
	for _, s := range m.general.sessions {
		if m.general.groupKey(s) == section {
			count++
			if s.Group == status.GroupNeedsInput {
				needs++
			}
			if s.Group == status.GroupReadyForReview {
				review++
			}
		}
	}
	marker := "▾ "
	if m.general.isCollapsed(section) {
		marker = "▸ "
	}
	if m.general.focusSection == section {
		marker = "▌ "
	}
	label := strings.ToUpper(section)
	if m.general.grouping == groupByStatus {
		label = groupHeader(status.Group(section))
	}
	counts := " (" + itoa(count) + " · " + itoa(needs) + " input · " + itoa(review) + " review)"
	if ansi.StringWidth(marker+label+counts) > m.width {
		counts = " (" + itoa(count) + ") " + itoa(needs) + "i " + itoa(review) + "r"
		label = ellipsis(label, m.width-ansi.StringWidth(marker+counts))
	}
	text := marker + label + counts
	if m.general.grouping == groupByStatus {
		return groupHeaderStyle(status.Group(section)).Render(text)
	}
	return styleTitle.Render(text)
}
func (m rootModel) boardStrip() string {
	if m.connectionLost {
		return ellipsis(m.generalStatus(), m.width)
	}
	text := "[New] [" + m.boardOpenLabel() + "] [More]"
	hint := " · n new · ⏎ attach (ctrl+q returns) · m more · o options"
	if m.width >= ansi.StringWidth(text+hint) {
		text += hint
	}
	if m.clientVersion != "" && m.width >= 72 && m.width-ansi.StringWidth(text)-ansi.StringWidth(m.clientVersion) >= 3 {
		text += strings.Repeat(" ", m.width-ansi.StringWidth(text)-ansi.StringWidth(m.clientVersion)) + m.clientVersion
	}
	return text
}
func (m rootModel) boardOpenLabel() string {
	if m.general.focusSection != "" {
		return "Expand"
	}
	if _, ok := m.general.selected(); !ok {
		return "—"
	}
	if !m.general.focusVisible() {
		return "Show"
	}
	return "Open"
}
func (m rootModel) boardLineAt(y int) (found boardLine, ok bool) {
	start, rows, _, _ := m.boardGeometry()
	if y < start || y >= start+rows {
		return
	}
	n := 0
	m.general.content(func(l boardLine) {
		if n == m.general.top+y-start {
			found = l
			ok = true
		}
		n++
	})
	return
}

type boardAnchor struct {
	kind        boardLineKind
	id, section string
	index       int
}

func (m generalModel) anchor() (a boardAnchor) {
	a.index = m.top
	n := 0
	m.content(func(l boardLine) {
		if n == m.top {
			a.kind = l.kind
			a.id = l.session.ID
			a.section = l.section
		}
		n++
	})
	return
}
func (m *generalModel) restoreAnchor(a boardAnchor) {
	found := false
	n := 0
	m.content(func(l boardLine) {
		if !found && (a.id != "" && l.kind == lineSession && l.session.ID == a.id || a.section != "" && l.kind == lineSection && l.section == a.section) {
			m.top = n
			found = true
		}
		n++
	})
	if !found {
		m.top = a.index
	}
	m.clampTop()
}
func (m generalModel) visibleWorking() bool {
	visible := false
	i := 0
	m.content(func(l boardLine) {
		if l.kind == lineSession && l.session.Group == status.GroupWorking && (m.contentRows <= 0 || i >= m.top && i < m.top+m.contentRows) {
			visible = true
		}
		i++
	})
	return visible && m.more == nil
}

// survivingAnchor chooses a nearby identity before a destructive roster change.
// At equal distance prefer the following item, matching forward reading order.
func (m generalModel) survivingAnchor(keep func(boardLine) bool) boardAnchor {
	anchor := m.anchor()
	best := -1
	line := 0
	m.content(func(l boardLine) {
		if keep(l) {
			distance := line - m.top
			if distance < 0 {
				distance = -distance
			}
			if best < 0 || distance < best || distance == best && line >= m.top {
				anchor.kind = l.kind
				anchor.id = l.session.ID
				anchor.section = l.section
				best = distance
			}
		}
		line++
	})
	return anchor
}

// updatePositions resolves old identities before a session changes group or
// replaces sources. Metadata callers supply no lifecycle removals.
func (m generalModel) updatePositions(updated protocol.SessionView, removed []string) (anchor, focus boardAnchor) {
	anchor = m.anchor()
	changed := len(removed) > 0
	for _, old := range m.sessions {
		if old.ID == updated.ID {
			changed = changed || m.groupKey(old) != m.groupKey(updated)
			break
		}
	}
	if !changed {
		return
	}
	survives := func(id string) bool { return id == updated.ID || !slices.Contains(removed, id) }
	keep := func(l boardLine) bool {
		if l.kind == lineSession {
			return survives(l.session.ID) && (l.session.ID != updated.ID || !m.isCollapsed(m.groupKey(updated)))
		}
		if l.kind == lineSection {
			if l.section == m.groupKey(updated) {
				return true
			}
			for _, old := range m.sessions {
				if old.ID != updated.ID && survives(old.ID) && m.groupKey(old) == l.section {
					return true
				}
			}
		}
		return false
	}
	anchor = m.survivingAnchor(keep)
	if m.focusSection != "" && !keep(boardLine{kind: lineSection, section: m.focusSection}) {
		focus = m.survivingFocus(keep)
	}
	return
}

func (m generalModel) survivingFocus(keep func(boardLine) bool) (focus boardAnchor) {
	best := -1
	m.navigation(func(l boardLine, i int) {
		if !keep(l) {
			return
		}
		distance := i - m.sel
		if distance < 0 {
			distance = -distance
		}
		if best < 0 || distance < best || distance == best && i >= m.sel {
			focus = boardAnchor{kind: l.kind, id: l.session.ID, section: l.section}
			best = distance
		}
	})
	return
}

func (m *generalModel) restoreUpdateFocus(target boardAnchor) {
	if target.id != "" {
		m.focusSection = ""
		m.restoreSel(target.id)
	} else if target.section != "" {
		m.focusSection = target.section
		m.restoreSel("")
	}
}
