package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// groupOrder is the fixed display order of the four groups (V-1). Empty groups
// are omitted at render time.
var groupOrder = []status.Group{
	status.GroupNeedsInput,
	status.GroupWorking,
	status.GroupReadyForReview,
	status.GroupCompleted,
}

// Row column widths for the general view (display cells).
const (
	colAgent   = 9
	colCwd     = 24
	colStatus  = 17
	colElapsed = 6
)

// generalModel is the grouped session board: the general view.
type generalModel struct {
	sessions []protocol.SessionView // in arrival order; grouped at render time
	sel      int                    // flat selection index across visible rows
	confirm  bool                   // a kill/delete confirm is pending on the selection
	width    int
}

func newGeneralModel(sessions []protocol.SessionView) generalModel {
	return generalModel{sessions: sessions}
}

// flat returns the sessions in display order: by group (fixed order), then by
// arrival order within each group. The selection index is a position in this
// list.
func (m generalModel) flat() []protocol.SessionView {
	out := make([]protocol.SessionView, 0, len(m.sessions))
	for _, g := range groupOrder {
		for _, s := range m.sessions {
			if s.Group == g {
				out = append(out, s)
			}
		}
	}
	return out
}

// selected returns the currently-selected session, or (zero, false) when the
// board is empty.
func (m generalModel) selected() (protocol.SessionView, bool) {
	flat := m.flat()
	if len(flat) == 0 || m.sel < 0 || m.sel >= len(flat) {
		return protocol.SessionView{}, false
	}
	return flat[m.sel], true
}

// clampSel keeps the selection within the visible rows.
func (m *generalModel) clampSel() {
	n := len(m.sessions)
	if n == 0 {
		m.sel = 0
		return
	}
	if m.sel < 0 {
		m.sel = 0
	}
	if m.sel >= n {
		m.sel = n - 1
	}
}

// move shifts the selection by delta with wrapping across all groups (V-3).
func (m *generalModel) move(delta int) {
	n := len(m.sessions)
	if n == 0 {
		return
	}
	m.sel = ((m.sel+delta)%n + n) % n
}

// apply folds one status-change event into the board: it updates the matching
// row in place (moving its group, never duplicating), or appends a new one. It
// returns a command that prints the notification banner when the session
// transitions INTO needs_input or ready_for_review (V-5), else nil.
func (m *generalModel) apply(s protocol.SessionView) tea.Cmd {
	var oldGroup status.Group
	found := false
	for i := range m.sessions {
		if m.sessions[i].ID == s.ID {
			oldGroup = m.sessions[i].Group
			m.sessions[i] = s
			found = true
			break
		}
	}
	if !found {
		m.sessions = append(m.sessions, s)
	}
	m.clampSel()

	if bannerGroup(s.Group) && (!found || oldGroup != s.Group) {
		// tea.Printf writes above the program (never into View content), so the
		// notification is observable in the output stream yet leaves the
		// persistent render — and its row count — untouched.
		return tea.Printf("%s %s", s.Agent, statusToken(s.Group))
	}
	return nil
}

// bannerGroup reports whether a transition into g raises a notification banner.
func bannerGroup(g status.Group) bool {
	return g == status.GroupNeedsInput || g == status.GroupReadyForReview
}

// ---------------------------------------------------------------------------
// Router glue: keyboard handling for the general screen.
// ---------------------------------------------------------------------------

func (m rootModel) updateGeneral(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.general.confirm {
		return m.updateConfirm(k)
	}

	switch {
	case k.Code == tea.KeyDown || (k.Text == "j"):
		m.general.move(1)
	case k.Code == tea.KeyUp || (k.Text == "k"):
		m.general.move(-1)
	case k.Code == tea.KeyEnter:
		if s, ok := m.general.selected(); ok {
			m.attach = attachModel{session: s, hasSession: true, width: m.width}
			m.screen = screenAttach
		}
	case k.Text == "n":
		m.launch = newLaunchModel(m.detect(), m.width)
		m.screen = screenLaunch
	case isCtrlX(k):
		if _, ok := m.general.selected(); ok {
			m.general.confirm = true
		}
	case k.Code == tea.KeyEsc:
		return m, tea.Quit
	}
	return m, nil
}

// updateConfirm handles the pending kill/delete confirm on the selection (R-3):
// `y` or a second Ctrl+X resolves it, `n` or Esc cancels it.
func (m rootModel) updateConfirm(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s, ok := m.general.selected()
	switch {
	case k.Text == "y" || isCtrlX(k):
		m.general.confirm = false
		if !ok {
			return m, nil
		}
		if s.Status.Process == status.ProcessRunning {
			return m, killCmd(m.client, s.ID)
		}
		return m, deleteCmd(m.client, s.ID)
	case k.Text == "n" || k.Code == tea.KeyEsc:
		m.general.confirm = false
	}
	return m, nil
}

func isCtrlX(k tea.KeyPressMsg) bool {
	return k.Code == 'x' && k.Mod == tea.ModCtrl
}

func killCmd(c Client, id string) tea.Cmd {
	return func() tea.Msg { _ = c.Kill(id); return nil }
}

func deleteCmd(c Client, id string) tea.Cmd {
	return func() tea.Msg { _ = c.Delete(id); return nil }
}

// ---------------------------------------------------------------------------
// Rendering.
// ---------------------------------------------------------------------------

func (m generalModel) view() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")

	// idx walks the flat display order so it lines up with the selection index.
	idx := 0
	for _, g := range groupOrder {
		rows := sessionsInGroup(m.sessions, g)
		if len(rows) == 0 {
			continue // empty groups are omitted (V-1)
		}
		hdr := lipgloss.NewStyle().Foreground(groupColor(g)).Bold(true).Render(groupHeader(g))
		b.WriteString("  " + hdr + "\n")
		for _, s := range rows {
			b.WriteString(m.renderRow(s, g, idx == m.sel) + "\n")
			idx++
		}
		b.WriteString("\n")
	}

	b.WriteString("  " + styleDim.Render("↑↓ navigate   ⏎ attach   n new   ctrl+x kill   esc quit"))
	return b.String()
}

func (m generalModel) header() string {
	running, needs := 0, 0
	for _, s := range m.sessions {
		if s.Group != status.GroupCompleted {
			running++
		}
		if s.Group == status.GroupNeedsInput {
			needs++
		}
	}
	left := styleTitle.Render("swarm")
	right := styleDim.Render(itoa(running) + " running · " + itoa(needs) + " needs you")
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 3 {
		gap = 3
	}
	return left + strings.Repeat(" ", gap) + right
}

// renderRow renders one session row: a 2-cell selection prefix (or the confirm
// prompt), the group icon, then the five V-4 fields on one line.
func (m generalModel) renderRow(s protocol.SessionView, g status.Group, selected bool) string {
	gc := groupColor(g)
	icon := lipgloss.NewStyle().Foreground(gc).Render(groupIcon(g))
	fields := icon + " " +
		styleAgent.Render(padRight(s.Agent, colAgent)) +
		styleDim.Render(padRight(shortenCwd(s.Cwd), colCwd)) +
		lipgloss.NewStyle().Foreground(gc).Render(padRight(statusToken(g), colStatus)) +
		styleDim.Render(padRight(compactElapsed(elapsedOf(s)), colElapsed)+s.Summary)

	var prefix string
	switch {
	case selected && m.confirm:
		prefix = lipgloss.NewStyle().Foreground(colNeedsInput).Render(confirmPrompt(s)) + " "
	case selected:
		prefix = lipgloss.NewStyle().Foreground(colAmber).Render("▌") + " "
	default:
		prefix = "  "
	}
	return prefix + fields
}

// confirmPrompt is the confirm-specific token shown on the selected row: "kill?"
// for a running session, "delete?" for a completed/lost one (R-3).
func confirmPrompt(s protocol.SessionView) string {
	if s.Status.Process == status.ProcessRunning {
		return "kill? y/n"
	}
	return "delete? y/n"
}

// elapsedOf is the time since the session was last active.
func elapsedOf(s protocol.SessionView) time.Duration {
	return time.Since(s.LastActivity)
}

func sessionsInGroup(sessions []protocol.SessionView, g status.Group) []protocol.SessionView {
	var out []protocol.SessionView
	for _, s := range sessions {
		if s.Group == g {
			out = append(out, s)
		}
	}
	return out
}
