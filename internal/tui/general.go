package tui

import (
	"os"
	"strings"
	"time"
	"unicode/utf8"

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

// Bounded row column widths for the general view (display cells). Agent, status,
// and elapsed are semantic fields with known maximum values, so they never grow.
// Name and cwd are the two flexible identity fields; rowColumnsFor gives them any
// extra terminal width while keeping the 120-column layout as the baseline.
const (
	// colName is the session NAME column (the editable discussion name, v0.5). It is
	// blank when a session carries no name — the separate agent column still
	// identifies the row. Longer names are clamped so the columns stay aligned.
	colName = 20
	// colAgent is the agent-CLI column (claude/codex/gemini/opencode), split out from
	// the name so the two are distinct fields (field test 4). Wide enough for the
	// longest agent name.
	colAgent   = 9
	colCwd     = 24
	colStatus  = 17
	colElapsed = 6

	colSummaryBaseline = 40
	colSummaryMin      = 12
	colNameMin         = 8
	colCwdMin          = 10
	colNameMax         = 54
	colCwdMax          = 58
)

type rowColumns struct {
	name    int
	cwd     int
	summary int
}

// generalModel is the grouped session board: the general view.
type generalModel struct {
	sessions []protocol.SessionView // in arrival order; grouped at render time
	sel      int                    // flat selection index across visible rows

	confirm     bool   // a kill/delete confirm is pending
	confirmID   string // session the confirm targets, captured by identity when it opened
	confirmKill bool   // whether that target was running (kill) vs. completed (delete) at open

	// Inline rename edit (v0.5): 'e' opens a single-line edit of the selected row's
	// name. editID captures the target by identity so a concurrent regroup cannot move
	// the edit onto a neighbor; editBuf is the working buffer; editing gates the mode.
	editing bool
	editID  string
	editBuf string
	// editCursor is a rune index into editBuf. A rune index keeps arrow movement,
	// insertion, and deletion safe for non-ASCII discussion names.
	editCursor int

	// spinnerFrame advances on the dedicated 90 ms Working animation tick. The
	// router runs that tick only while this board is visible and has a Working row.
	spinnerFrame uint64

	bannerText   string    // transient V-5 notification ("<agent> needs input"), "" when none
	bannerExpiry time.Time // when the banner stops rendering (auto-expiry)

	// tombstones records ids removed by a client-side delete, each with an expiry, so a
	// late buffered status event (one already queued on the subscribe stream before the
	// delete landed) cannot re-append the row. The daemon tombstones server-side too; this
	// closes the client-side remainder. Expiry is checked in apply (the Update path), never
	// in a view, so no wall-clock read reaches rendering.
	tombstones map[string]time.Time

	width int
}

// bannerDuration is how long the transient V-5 banner stays on screen before it
// auto-expires. Long enough to be read (and to still be present for the coordinated
// TestLiveness in-view assertion), short enough to stay transient.
const bannerDuration = 4 * time.Second

// tombstoneTTL is how long a client-deleted id is remembered so a late buffered event
// for it is dropped. It only needs to outlast the drain of events already queued on the
// subscribe stream at delete time; a few seconds is ample.
const tombstoneTTL = 10 * time.Second

func newGeneralModel(sessions []protocol.SessionView) generalModel {
	return generalModel{sessions: sessions}
}

// hasWorking reports whether the board currently has anything to animate. Keeping
// this check allocation-free lets the router avoid an 11 Hz timer on idle boards.
func (m generalModel) hasWorking() bool {
	for _, s := range m.sessions {
		if s.Group == status.GroupWorking {
			return true
		}
	}
	return false
}

// selected returns the currently-selected session, or (zero, false) when the
// board is empty. It walks sessions in display order (by group, fixed order,
// then arrival order within each group — the same order restoreSel searches)
// without building a full copy of the board (R4.1.2): m.sel is a position in
// that order, and finding one element at a position needs no allocation.
func (m generalModel) selected() (protocol.SessionView, bool) {
	i := 0
	for _, g := range groupOrder {
		for _, s := range m.sessions {
			if s.Group != g {
				continue
			}
			if i == m.sel {
				return s, true
			}
			i++
		}
	}
	return protocol.SessionView{}, false
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
// display order on every event). If that session is gone, the index is clamped
// to stay in range. Walks in the same display order as selected (R4.1.2): no
// full-board copy, and it stops as soon as id is found.
func (m *generalModel) restoreSel(id string) {
	if id != "" {
		i := 0
		for _, g := range groupOrder {
			for _, s := range m.sessions {
				if s.Group != g {
					continue
				}
				if s.ID == id {
					m.sel = i
					return
				}
				i++
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
	// A client-deleted session must not reappear from a late buffered event that was
	// already in flight when the delete landed (item 6). The tombstone is checked here on
	// the Update path, so no wall-clock read reaches a view.
	if m.isTombstoned(s.ID) {
		return nil
	}
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
		m.bannerText = displayName(s) + " " + statusToken(s.Group)
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
	text := "● " + m.bannerText
	if m.width > 2 {
		text = clampCells(text, m.width-2)
	}
	return "  " + styleTitle.Render(text)
}

// bannerGroup reports whether a transition into g raises a notification banner.
func bannerGroup(g status.Group) bool {
	return g == status.GroupNeedsInput || g == status.GroupReadyForReview
}

// ---------------------------------------------------------------------------
// Router glue: keyboard handling for the general screen.
// ---------------------------------------------------------------------------

func (m rootModel) updateGeneral(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.general.editing {
		return m.updateRename(k)
	}
	if m.general.confirm {
		return m.updateConfirm(k)
	}

	switch {
	case k.Code == tea.KeyDown || (k.Text == "j"):
		m.general.move(1)
	case k.Code == tea.KeyUp || (k.Text == "k"):
		m.general.move(-1)
	case k.Code == tea.KeyEnter:
		// Route the attach at the selection captured now (this keypress).
		if s, ok := m.general.selected(); ok {
			// An ended/lost row cannot be attached: the daemon refuses any attach to a
			// non-running session (internal/daemon attach.go). Rather than dial a doomed
			// attach and swallow the error (the field-test silent no-op), surface an
			// actionable banner and never attempt it.
			if s.Status.Process != status.ProcessRunning {
				return m, m.general.setBanner("session has ended - r resume, ctrl+x delete")
			}
			if m.attachRunner != nil {
				m.screen = screenAttach
				// Only running rows reach here, so the passthrough is always read-write.
				return m, runAttach(m.attachRunner, s, false)
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
	case k.Text == "e":
		// Open an inline rename of the selected row (v0.5). Capture the target by
		// identity so a concurrent regroup cannot move the edit onto a neighbor, and
		// seed the buffer with the current name (editing an existing label, not
		// starting blank).
		if s, ok := m.general.selected(); ok {
			m.general.editing = true
			m.general.editID = s.ID
			m.general.editBuf = s.Name
			m.general.editCursor = utf8.RuneCountInString(s.Name)
		}
	case k.Text == "h":
		// Open the two-field supervised-handoff form against the selected source. Raw
		// status gates this before any input is sent: the display group intentionally
		// combines ordinary prompts with permission requests, but those are not equally
		// safe places to submit an instruction.
		if s, ok := m.general.selected(); ok {
			if allowed, why := handoffSourceEligibility(s); !allowed {
				m.general.setBanner(why)
				return m, nil
			}
			m.handoff = newHandoffModel(s, m.agents, m.detected, m.width)
			m.screen = screenHandoff
			m.detectGen++
			return m, detectCmd(m.detect, m.detectGen)
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

// updateRename handles the inline single-line name edit (v0.5): Enter commits the
// rename op, Esc cancels, Left/Right move a rune-aware insertion cursor, Backspace
// deletes before that cursor, and printable text inserts there. The target is the
// session captured when the edit opened (editID), not the live selection.
func (m rootModel) updateRename(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case k.Code == tea.KeyEnter:
		id, name := m.general.editID, m.general.editBuf
		m.general.closeEdit()
		return m, renameCmd(m.client, id, name)
	case k.Code == tea.KeyEsc:
		m.general.closeEdit()
	case k.Code == tea.KeyLeft:
		if m.general.editCursor > 0 {
			m.general.editCursor--
		}
	case k.Code == tea.KeyRight:
		if m.general.editCursor < utf8.RuneCountInString(m.general.editBuf) {
			m.general.editCursor++
		}
	case k.Code == tea.KeyBackspace:
		m.general.deleteBeforeCursor()
	case k.Text != "":
		m.general.insertAtCursor(k.Text)
	}
	return m, nil
}

// closeEdit exits the inline rename mode and clears its buffer/target.
func (m *generalModel) closeEdit() {
	m.editing = false
	m.editID = ""
	m.editBuf = ""
	m.editCursor = 0
}

// pasteEdit inserts bracketed-paste content at the cursor, stripping the CR/LF a
// single-line name must never carry (mirrors the launch form's paste).
func (m *generalModel) pasteEdit(s string) {
	if !m.editing {
		return
	}
	m.insertAtCursor(strings.NewReplacer("\r", "", "\n", "").Replace(s))
}

// insertAtCursor inserts text at editCursor and leaves the cursor after the new
// runes. Converting to []rune is appropriate for a short, human-edited label and
// makes it impossible to split UTF-8 while navigating.
func (m *generalModel) insertAtCursor(text string) {
	if text == "" {
		return
	}
	runes := []rune(m.editBuf)
	if m.editCursor < 0 {
		m.editCursor = 0
	}
	if m.editCursor > len(runes) {
		m.editCursor = len(runes)
	}
	inserted := []rune(text)
	out := make([]rune, 0, len(runes)+len(inserted))
	out = append(out, runes[:m.editCursor]...)
	out = append(out, inserted...)
	out = append(out, runes[m.editCursor:]...)
	m.editBuf = string(out)
	m.editCursor += len(inserted)
}

// deleteBeforeCursor removes the rune immediately left of the insertion cursor.
func (m *generalModel) deleteBeforeCursor() {
	runes := []rune(m.editBuf)
	if m.editCursor <= 0 || len(runes) == 0 {
		return
	}
	if m.editCursor > len(runes) {
		m.editCursor = len(runes)
	}
	i := m.editCursor - 1
	m.editBuf = string(append(runes[:i], runes[m.editCursor:]...))
	m.editCursor = i
}

func isCtrlX(k tea.KeyPressMsg) bool {
	return k.Code == 'x' && k.Mod == tea.ModCtrl
}

// deleteDoneMsg carries a delete's outcome so a success can optimistically drop the
// row (and acknowledge it) and a failure can be surfaced, instead of relying on the
// eventual daemon event (the field-test "nothing happens - looks stale").
type deleteDoneMsg struct {
	id  string
	err error
}

// killDoneMsg carries a kill's outcome so a failure is surfaced rather than silently
// discarded. A success is a no-op on the board: the daemon event transitions the row
// to completed (it is not removed).
type killDoneMsg struct {
	id  string
	err error
}

func killCmd(c Client, id string) tea.Cmd {
	return func() tea.Msg { return killDoneMsg{id: id, err: c.Kill(id)} }
}

// renameDoneMsg carries a rename's outcome so a failure (including an older daemon's
// skew refusal) is surfaced on the banner, and a success updates the row's label
// optimistically — the daemon's roster event then re-applies the same name (F-safe).
type renameDoneMsg struct {
	id   string
	name string
	err  error
}

func renameCmd(c Client, id, name string) tea.Cmd {
	return func() tea.Msg { return renameDoneMsg{id: id, name: name, err: c.Rename(id, name)} }
}

// tombstone records id as client-deleted (with an expiry) so a late buffered event for
// it is dropped by apply. Called alongside remove on a successful delete.
func (m *generalModel) tombstone(id string) {
	if m.tombstones == nil {
		m.tombstones = make(map[string]time.Time)
	}
	m.tombstones[id] = time.Now().Add(tombstoneTTL)
}

// isTombstoned reports whether id was recently client-deleted, purging the entry once it
// expires (so a later, legitimately new session reusing the id is not blocked forever).
func (m *generalModel) isTombstoned(id string) bool {
	exp, ok := m.tombstones[id]
	if !ok {
		return false
	}
	if !time.Now().Before(exp) {
		delete(m.tombstones, id)
		return false
	}
	return true
}

// remove drops the session with id from the board (the optimistic removal on a
// successful delete), keeping the selection pinned to its session by identity.
func (m *generalModel) remove(id string) {
	selID := m.selectedID()
	kept := make([]protocol.SessionView, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.ID != id {
			kept = append(kept, s)
		}
	}
	m.sessions = kept
	if selID == id {
		selID = "" // the selected row is gone; restoreSel clamps into range
	}
	m.restoreSel(selID)
}

// applyName optimistically updates a renamed session's label on the board so the
// change shows immediately; the daemon's roster event later re-applies it (a no-op
// once the names already match).
func (m *generalModel) applyName(id, name string) {
	for i := range m.sessions {
		if m.sessions[i].ID == id {
			m.sessions[i].Name = name
			return
		}
	}
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
		Name:    s.Name, // a resumed session keeps its label (P2); "" degrades to the agent name
		Cwd:     s.Cwd,
		Options: map[string]string{protocol.OptionResumeFrom: s.ID},
		Env:     os.Environ(),
		Cols:    cols,
		Rows:    rows,
	}
	return func() tea.Msg {
		id, name, err := c.Launch(req)
		if name == "" {
			name = s.Name // skew fallback: an older daemon's reply carries no canonical name
		}
		// Carry the new session id + agent + name so a successful resume auto-attaches into
		// it (bd agents-tracker-stc) — the user pressed 'r' precisely to interact with it.
		return launchResultMsg{id: id, agent: s.Agent, name: name, err: err}
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
	return func() tea.Msg { return deleteDoneMsg{id: id, err: c.Delete(id)} }
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

// rowColumnsFor allocates the available row cells. At 120 columns the existing
// 20/24/40 name/path/summary proportions are byte-for-byte stable. Wider windows
// distribute their extra cells 55/45 to name and cwd (up to generous caps); narrow
// windows consume summary room first, then contract name and cwd to safe minima.
// prefixWidth is normally two cells, but expands for an inline kill/delete prompt,
// which must be included in the same no-wrap budget.
func rowColumnsFor(width, prefixWidth int) rowColumns {
	if width <= 0 {
		width = 120
	}
	if prefixWidth < 0 {
		prefixWidth = 0
	}
	fixed := prefixWidth + 2 + colAgent + colStatus + colElapsed // icon+space + bounded fields
	available := width - fixed
	if available <= 0 {
		return rowColumns{}
	}

	cols := rowColumns{name: colName, cwd: colCwd, summary: available - colName - colCwd}
	if cols.summary >= colSummaryBaseline {
		extra := cols.summary - colSummaryBaseline
		cols.summary = colSummaryBaseline
		growName := (extra*55 + 50) / 100
		growCwd := extra - growName
		if room := colNameMax - cols.name; growName > room {
			growCwd += growName - room
			growName = room
		}
		if room := colCwdMax - cols.cwd; growCwd > room {
			cols.summary += growCwd - room
			growCwd = room
		}
		cols.name += growName
		cols.cwd += growCwd
		return cols
	}

	if cols.summary >= colSummaryMin {
		return cols
	}
	deficit := colSummaryMin - cols.summary
	cols.summary = colSummaryMin
	for deficit > 0 && (cols.name > colNameMin || cols.cwd > colCwdMin) {
		if cols.name > colNameMin {
			cols.name--
			deficit--
		}
		if deficit > 0 && cols.cwd > colCwdMin {
			cols.cwd--
			deficit--
		}
	}
	if deficit > 0 {
		shrink := minInt(deficit, cols.summary)
		cols.summary -= shrink
		deficit -= shrink
	}
	for deficit > 0 && (cols.name > 0 || cols.cwd > 0) {
		if cols.name > 0 {
			cols.name--
			deficit--
		}
		if deficit > 0 && cols.cwd > 0 {
			cols.cwd--
			deficit--
		}
	}
	return cols
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// renderRow renders one session row: a 2-cell selection prefix (or the confirm
// prompt), the group icon, then the five V-4 fields on one line.
func (m generalModel) renderRow(s protocol.SessionView, g status.Group, selected bool) string {
	gs := groupStyle(g)
	icon := gs.Render(groupIcon(g, m.spinnerFrame))

	// The confirm token is wider than the ordinary two-cell selection prefix. Resolve
	// it first so responsive columns include its real width in the terminal budget.
	var prefix string
	var prefixWidth int
	switch {
	case m.confirm && s.ID == m.confirmID:
		prompt := confirmPrompt(s)
		// Preserve the bounded agent/status/elapsed fields on very narrow terminals.
		// The bottom bar still says "y confirm   n cancel", so a one-cell question
		// marker remains unambiguous when the full inline prompt cannot fit.
		fullPrefixWidth := lipgloss.Width(prompt) + 1
		minimumFullWidth := fullPrefixWidth + 2 + colAgent + colStatus + colElapsed
		if m.width > 0 && m.width < minimumFullWidth {
			prompt = "?"
		}
		prefix = styleError.Render(prompt) + " "
		prefixWidth = lipgloss.Width(prompt) + 1
	case selected:
		prefix = styleAmber.Render("▌") + " "
		prefixWidth = 2
	default:
		prefix = "  "
		prefixWidth = 2
	}
	cols := rowColumnsFor(m.width, prefixWidth)

	// Two identity columns (field test 4): the session NAME (bold, editable) then the
	// agent CLI (dim) as its own column. Each is clamped one cell short of its column
	// so padRight always leaves a separating space (no jamming, width discipline).
	tailText := s.Summary
	if badge := lineageBadge(s, m.sessions); badge != "" {
		tailText += " " + badge
	}
	tailText = clampCells(tailText, cols.summary)
	tail := styleDim.Render(padRight(compactElapsed(elapsedOf(s)), colElapsed) + tailText)
	// Amber markers share one cell budget with the summary and take precedence over
	// it: the summary yields, then the markers themselves clamp, so the row never
	// exceeds the terminal.
	var markers []string
	if s.RemoteControlled {
		markers = append(markers, remoteControlMarker)
	}
	if s.SupervisionPending {
		markers = append(markers, supervisionMarker(s, m.sessions))
	}
	if len(markers) > 0 {
		marker := " " + strings.Join(markers, " ")
		markerWidth := lipgloss.Width(marker)
		if markerWidth > cols.summary {
			marker = clampCells(marker, cols.summary)
			tailText = ""
		} else {
			tailText = clampCells(tailText, cols.summary-markerWidth)
		}
		tail = styleDim.Render(padRight(compactElapsed(elapsedOf(s)), colElapsed)+tailText) +
			styleAmber.Render(marker)
	}
	fields := icon + " " +
		styleAgent.Render(padRight(m.nameCell(s, cols.name), cols.name)) +
		styleDim.Render(padRight(clampCells(s.Agent, colAgent-1), colAgent)) +
		styleDim.Render(padRight(clampCells(shortenCwd(s.Cwd), cols.cwd-1), cols.cwd)) +
		gs.Render(padRight(statusToken(g), colStatus)) +
		tail
	return prefix + fields
}

// nameCell is the text shown in the responsive NAME column. The inline editor keeps
// the cursor visible even when a long name needs a viewport; ordinary names keep one
// trailing separator cell before the agent column.
func (m generalModel) nameCell(s protocol.SessionView, width int) string {
	contentWidth := width - 1
	if contentWidth <= 0 {
		return ""
	}
	if m.editing && s.ID == m.editID {
		return editViewport(m.editBuf, m.editCursor, contentWidth)
	}
	return clampCells(s.Name, contentWidth)
}

// editViewport renders text plus its insertion cursor within width display cells.
// It shows the full prefix while it fits; once it does not, a rune-safe suffix keeps
// the cursor pinned at the right edge. Text after the cursor fills any remaining room.
func editViewport(text string, cursor, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	before := string(runes[:cursor])
	after := string(runes[cursor:])
	leftBudget := width - 1 // the block cursor itself occupies one cell
	if lipgloss.Width(before) > leftBudget {
		before = suffixCells(before, leftBudget)
		after = ""
	} else {
		after = clampCells(after, leftBudget-lipgloss.Width(before))
	}
	return before + "█" + after
}

// suffixCells returns the longest rune-aligned suffix that fits within n display
// cells. It complements clampCells for keeping an insertion cursor visible.
func suffixCells(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	start, width := len(runes), 0
	for start > 0 {
		rw := lipgloss.Width(string(runes[start-1]))
		if width+rw > n {
			break
		}
		start--
		width += rw
	}
	return string(runes[start:])
}

// confirmPrompt is the confirm-specific token shown on the selected row: "kill?"
// for a running session, "delete?" for a completed/lost one (R-3).
func confirmPrompt(s protocol.SessionView) string {
	if s.Status.Process == status.ProcessRunning {
		return "kill? y/n"
	}
	return "delete? y/n"
}

// remoteControlMarker names the surface that currently holds the session's
// controller lease. A bare word, not a glyph: the roster is read at a glance and a
// decorative symbol here would be one more thing to learn. Device NAME display is
// deliberately out of scope -- the daemon answers a bare bool, and naming the device
// needs a deviceID accessor plus a registry lookup that does not exist yet.
const remoteControlMarker = "phone control"

// supervisionPendingMarker and supervisionGoneMarker are the row's passive-supervision
// words (ADR-010 Amendment 3 C3/C4): an attention event awaits delivery to the source,
// or nobody will be woken because the source has left the roster or stopped running.
const (
	supervisionPendingMarker = "supervisor pending"
	supervisionGoneMarker    = "supervisor gone"
)

// supervisionMarker picks the pending or gone word for a row whose supervision event
// is undelivered. The source is matched the way lineageBadge matches parents:
// SpawnedFrom carries the LOCAL id.
func supervisionMarker(s protocol.SessionView, sessions []protocol.SessionView) string {
	for _, o := range sessions {
		if localID(o.ID) == s.SpawnedFrom && o.Status.Process == status.ProcessRunning {
			return supervisionPendingMarker
		}
	}
	return supervisionGoneMarker
}

// lineageBadge is the row's spawn-lineage decoration (ADR-010 D4): where a spawned
// session came from, and whether it spawned any session currently on the roster.
// Both are derived from the roster slice the model already holds — no extra state
// and no extra RPC. SpawnedFrom carries the parent's LOCAL id, so the match is
// against the local half of each row's namespaced id.
func lineageBadge(s protocol.SessionView, sessions []protocol.SessionView) string {
	var badge string
	if s.SpawnedFrom != "" {
		badge = "from " + lineageLabel(s.SpawnedFrom, sessions)
	}
	local := localID(s.ID)
	children := 0
	for _, o := range sessions {
		if o.SpawnedFrom == local {
			children++
		}
	}
	if children > 0 {
		if badge != "" {
			badge += " "
		}
		badge += "spawned " + itoa(children)
	}
	return badge
}

// lineageLabel names a parent session: its display label while it is still on the
// roster, else the raw local id — the source session stays alive only until the user
// closes it (D4), and a child must still say where it came from afterwards.
func lineageLabel(parent string, sessions []protocol.SessionView) string {
	for _, s := range sessions {
		if localID(s.ID) == parent && s.Name != "" {
			return s.Name
		}
	}
	return parent
}

// localID is the local half of a namespaced row id, or the id itself when it is not
// namespaced.
func localID(id string) string {
	if _, local, ok := protocol.ParseID(id); ok {
		return local
	}
	return id
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
