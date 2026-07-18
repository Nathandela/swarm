package tui

import (
	"os"
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

	confirm     bool   // a kill/delete confirm is pending
	confirmID   string // session the confirm targets, captured by identity when it opened
	confirmKill bool   // whether that target was running (kill) vs. completed (delete) at open

	bannerText   string    // transient V-5 notification ("<agent> needs input"), "" when none
	bannerExpiry time.Time // when the banner stops rendering (auto-expiry)

	width int
}

// bannerDuration is how long the transient V-5 banner stays on screen before it
// auto-expires. Long enough to be read (and to still be present for the coordinated
// TestLiveness in-view assertion), short enough to stay transient.
const bannerDuration = 4 * time.Second

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

// selectedID is the id of the selected session, or "" when the board is empty.
func (m generalModel) selectedID() string {
	if s, ok := m.selected(); ok {
		return s.ID
	}
	return ""
}

// sessionByID returns the session with the given id, or (zero, false) if none
// matches. Used to resolve a pending confirm against the captured target rather
// than a possibly-shifted selection index.
func (m generalModel) sessionByID(id string) (protocol.SessionView, bool) {
	for _, s := range m.sessions {
		if s.ID == id {
			return s, true
		}
	}
	return protocol.SessionView{}, false
}

// restoreSel re-points the selection at the row whose session id is id, so the
// same session stays selected by identity across a regroup (apply reorders the
// flat list on every event). If that session is gone, the index is clamped to
// stay in range.
func (m *generalModel) restoreSel(id string) {
	if id != "" {
		for i, s := range m.flat() {
			if s.ID == id {
				m.sel = i
				return
			}
		}
	}
	m.clampSel()
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
// row in place (moving its group, never duplicating), or appends a new one. The
// selection is preserved by session identity across the regroup (not by index).
// It returns a command that prints the notification banner when the session
// transitions INTO needs_input or ready_for_review (V-5), else nil.
func (m *generalModel) apply(s protocol.SessionView) tea.Cmd {
	// Remember what is selected before the regroup shifts the flat indices.
	selID := m.selectedID()

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
	m.restoreSel(selID)

	if bannerGroup(s.Group) && (!found || oldGroup != s.Group) {
		// A transition INTO needs_input/ready_for_review raises a transient banner
		// (V-5) rendered IN View().Content, so it is visible under the alt-screen —
		// where the former tea.Printf (which writes to scrollback above the program)
		// was a no-op. It auto-expires after bannerDuration; the tick re-emits the
		// frame at expiry so the banner disappears on time.
		m.bannerText = s.Agent + " " + statusToken(s.Group)
		m.bannerExpiry = time.Now().Add(bannerDuration)
		return bannerTick()
	}
	return nil
}

// bannerExpireMsg fires when the transient banner reaches its expiry, prompting a
// frame re-emit so the (wall-clock-expired) banner is cleared from the render.
type bannerExpireMsg struct{}

// bannerTick schedules the banner's auto-expiry re-emit.
func bannerTick() tea.Cmd {
	return tea.Tick(bannerDuration, func(time.Time) tea.Msg { return bannerExpireMsg{} })
}

// bannerLine renders the transient banner, or "" once it has expired or is unset.
func (m generalModel) bannerLine() string {
	if m.bannerText == "" || !time.Now().Before(m.bannerExpiry) {
		return ""
	}
	return "  " + styleTitle.Render("● "+m.bannerText)
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
		// Route the attach at the selection captured now (this keypress). With an
		// injected runner this is the raw passthrough (completed/lost rows go
		// read-only, G3); without one it is the Epic 7 identify-only placeholder.
		if s, ok := m.general.selected(); ok {
			if m.attachRunner != nil {
				readOnly := s.Group == status.GroupCompleted
				return m, runAttach(m.attachRunner, s, readOnly)
			}
			m.attach = attachModel{session: s, hasSession: true, width: m.width}
			m.screen = screenAttach
		}
	case k.Text == "n":
		// Open INSTANTLY against cached detection (never call the prober on the Update
		// hot path — the P0 freeze), and kick an async refresh so availability updates
		// live while the form is open. Stamp the refresh with a newer generation so a
		// slow Init-era probe landing afterwards is recognized as stale and dropped.
		m.launch = newLaunchModel(m.agents, m.detected, m.width)
		m.screen = screenLaunch
		m.detectGen++
		return m, detectCmd(m.detect, m.detectGen)
	case k.Text == "r":
		// Resume an ended/lost session as a NEW linked session (R-2): offered only on
		// a non-running row (a running session has nothing to resume). The daemon
		// validates the source and composes the adapter's resume argv from the source's
		// captured conversation id.
		if s, ok := m.general.selected(); ok && s.Status.Process != status.ProcessRunning {
			return m, resumeCmd(m.client, s, m.width, m.height)
		}
	case isCtrlX(k):
		// Capture the confirm target by identity (and its kill-vs-delete state)
		// so a concurrent status event cannot shift a different row under it.
		if s, ok := m.general.selected(); ok {
			m.general.confirm = true
			m.general.confirmID = s.ID
			m.general.confirmKill = s.Status.Process == status.ProcessRunning
		}
	case k.Code == tea.KeyEsc:
		return m, tea.Quit
	}
	return m, nil
}

// updateConfirm handles the pending kill/delete confirm (R-3): `y` or a second
// Ctrl+X resolves it, `n` or Esc cancels it. Resolution targets the session
// captured when the confirm opened, looked up fresh by identity — never the
// current selection index, which a concurrent event may have shifted.
func (m rootModel) updateConfirm(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case k.Text == "y" || isCtrlX(k):
		id, wantKill := m.general.confirmID, m.general.confirmKill
		m.general.confirm = false
		m.general.confirmID = ""
		s, ok := m.general.sessionByID(id)
		if !ok {
			return m, nil // target vanished — do nothing
		}
		if running := s.Status.Process == status.ProcessRunning; running != wantKill {
			return m, nil // target flipped kill<->delete state — do nothing
		}
		if wantKill {
			return m, killCmd(m.client, s.ID)
		}
		return m, deleteCmd(m.client, s.ID)
	case k.Text == "n" || k.Code == tea.KeyEsc:
		m.general.confirm = false
		m.general.confirmID = ""
	}
	return m, nil
}

func isCtrlX(k tea.KeyPressMsg) bool {
	return k.Code == 'x' && k.Mod == tea.ModCtrl
}

func killCmd(c Client, id string) tea.Cmd {
	return func() tea.Msg { _ = c.Kill(id); return nil }
}

// defaultResumeCols/Rows size a resume launch when the window size is not yet known.
const (
	defaultResumeCols = 80
	defaultResumeRows = 24
)

// resumeCmd issues a resume-as-new-session launch for an ended/lost row (R-2): it
// carries the source session's id under the reserved resume_from option so the
// daemon validates the source, composes the adapter's resume argv from the source's
// captured conversation id, and links the new session's ResumedFrom. The source's
// agent + cwd carry over. It passes Env (so the daemon can resolve the real agent
// binary on PATH — B1) and surfaces a launch failure via launchResultMsg instead of
// discarding it, so a rejected resume (e.g. no captured conversation id) is visible.
func resumeCmd(c Client, s protocol.SessionView, cols, rows int) tea.Cmd {
	if cols <= 0 {
		cols = defaultResumeCols
	}
	if rows <= 0 {
		rows = defaultResumeRows
	}
	req := protocol.LaunchReq{
		Agent:   s.Agent,
		Cwd:     s.Cwd,
		Options: map[string]string{protocol.OptionResumeFrom: s.ID},
		Env:     os.Environ(),
		Cols:    cols,
		Rows:    rows,
	}
	return func() tea.Msg {
		_, err := c.Launch(req)
		return launchResultMsg{err: err}
	}
}

// setBanner shows a transient banner (reusing the V-5 notification surface) — used
// to surface a launch/resume failure to the user rather than discarding it.
func (m *generalModel) setBanner(text string) tea.Cmd {
	m.bannerText = text
	m.bannerExpiry = time.Now().Add(bannerDuration)
	return bannerTick()
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

	if bn := m.bannerLine(); bn != "" {
		b.WriteString(bn + "\n\n")
	}

	// idx walks the flat display order so it lines up with the selection index.
	idx := 0
	for _, g := range groupOrder {
		rows := sessionsInGroup(m.sessions, g)
		if len(rows) == 0 {
			continue // empty groups are omitted (V-1)
		}
		hdr := groupHeaderStyle(g).Render(groupHeader(g))
		b.WriteString("  " + hdr + "\n")
		for _, s := range rows {
			b.WriteString(m.renderRow(s, g, idx == m.sel) + "\n")
			idx++
		}
		b.WriteString("\n")
	}

	// The context-key footer is promoted to the router's persistent bottom bar
	// (generalStatus / composeBoard), so it is no longer rendered inline here.
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
	gs := groupStyle(g)
	icon := gs.Render(groupIcon(g))
	fields := icon + " " +
		styleAgent.Render(padRight(s.Agent, colAgent)) +
		styleDim.Render(padRight(shortenCwd(s.Cwd), colCwd)) +
		gs.Render(padRight(statusToken(g), colStatus)) +
		styleDim.Render(padRight(compactElapsed(elapsedOf(s)), colElapsed)+s.Summary)

	// The confirm prompt renders on the confirmID row (captured by identity), NOT the
	// selected row, so a mid-confirm regroup/removal cannot paint the prompt onto a
	// neighbor. When the target has been removed (confirmID matches no row) no row
	// shows it.
	var prefix string
	switch {
	case m.confirm && s.ID == m.confirmID:
		prefix = styleError.Render(confirmPrompt(s)) + " "
	case selected:
		prefix = styleAmber.Render("▌") + " "
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
