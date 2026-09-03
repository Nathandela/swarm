package tui

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
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

type groupingMode int

const (
	groupByStatus groupingMode = iota
	groupByRepo
	groupByTag
)

type orderingMode int

const (
	orderByArrival orderingMode = iota
	orderByActivity
	orderByCreated
	orderByName
)

const (
	noRepoGroup   = "(no repo)"
	untaggedGroup = "(untagged)"
)

// groupingLabels and orderingLabels name the modes for the options window's
// pickers and the header's layout label (index = mode).
var (
	groupingLabels = [...]string{"status", "repo", "tag"}
	orderingLabels = [...]string{"arrival", "activity", "created", "name"}
)

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
	sessions []protocol.SessionView // newest category entry first; grouped at render time
	sel      int                    // flat selection index across visible rows
	grouping groupingMode
	ordering orderingMode
	sections []string // cached display-order group keys for the active grouping

	confirm     bool   // a kill/delete confirm is pending
	confirmID   string // session the confirm targets, captured by identity when it opened
	confirmKill bool   // whether that target was running (kill) vs. completed (delete) at open

	// Inline rename edit (v0.5): 'e' opens a single-line edit of the selected row's
	// name. editID captures the target by identity so a concurrent regroup cannot move
	// the edit onto a neighbor; edit is the working buffer; editing gates the mode.
	editing bool
	editID  string
	edit    lineEditor
	editTag bool // the shared inline editor targets Tag instead of Name

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
	m := generalModel{
		sessions: append([]protocol.SessionView(nil), sessions...),
		grouping: groupByStatus,
		ordering: orderByArrival,
	}
	m.refreshLayout("")
	return m
}

func categoryEnteredAt(s protocol.SessionView) time.Time {
	if !s.GroupEnteredAt.IsZero() {
		return s.GroupEnteredAt
	}
	if !s.LastActivity.IsZero() {
		return s.LastActivity
	}
	return s.CreatedAt
}

// groupRank is a session's position in the fixed group display order, so a cell
// that mixes statuses can be blocked by the same order the status sections use.
func groupRank(g status.Group) int {
	if i := slices.Index(groupOrder, g); i >= 0 {
		return i
	}
	return len(groupOrder)
}

func sortSessionsBy(sessions []protocol.SessionView, ordering orderingMode, grouping groupingMode) {
	slices.SortFunc(sessions, func(a, b protocol.SessionView) int {
		// Status is always the first key, so a repo or tag cell — which mixes statuses —
		// reads needs input first and completed last, with the chosen ordering as the key
		// within each status block. Under status grouping this is a no-op rather than a
		// special case: display order is only ever read after filtering by section, and
		// every row in a status section shares its group, so the ranks always tie.
		if c := cmp.Compare(groupRank(a.Group), groupRank(b.Group)); c != 0 {
			return c
		}
		var c int
		switch ordering {
		case orderByActivity:
			c = b.LastActivity.Compare(a.LastActivity)
		case orderByCreated:
			c = b.CreatedAt.Compare(a.CreatedAt)
		case orderByName:
			c = strings.Compare(strings.ToLower(displayName(a)), strings.ToLower(displayName(b)))
		default:
			// Category-entry time is meaningful for status groups. Repository and
			// tag membership are stable metadata, so their arrival is creation time.
			if grouping == groupByStatus {
				c = categoryEnteredAt(b).Compare(categoryEnteredAt(a))
			} else {
				c = b.CreatedAt.Compare(a.CreatedAt)
			}
		}
		if c != 0 {
			return c
		}
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

func (m generalModel) groupKey(s protocol.SessionView) string {
	switch m.grouping {
	case groupByRepo:
		cwd := strings.TrimSpace(s.Cwd)
		if cwd == "" {
			return noRepoGroup
		}
		name := filepath.Base(filepath.Clean(cwd))
		if name == "." || name == string(filepath.Separator) || name == "" {
			return noRepoGroup
		}
		return name
	case groupByTag:
		if tag := strings.TrimSpace(s.Tag); tag != "" {
			return tag
		}
		return untaggedGroup
	default:
		return string(s.Group)
	}
}

func (m *generalModel) rebuildSections() {
	if m.grouping == groupByStatus {
		m.sections = m.sections[:0]
		for _, g := range groupOrder {
			for _, s := range m.sessions {
				if s.Group == g {
					m.sections = append(m.sections, string(g))
					break
				}
			}
		}
		return
	}
	seen := make(map[string]struct{}, len(m.sessions))
	m.sections = m.sections[:0]
	for _, s := range m.sessions {
		key := m.groupKey(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		m.sections = append(m.sections, key)
	}
	slices.SortFunc(m.sections, func(a, b string) int {
		if a == untaggedGroup || a == noRepoGroup {
			return 1
		}
		if b == untaggedGroup || b == noRepoGroup {
			return -1
		}
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	})
}

// sectionOrder preserves compatibility with small value-constructed models used
// by helpers and tests. Production models eagerly cache sections in refreshLayout.
func (m generalModel) sectionOrder() []string {
	if len(m.sections) > 0 || len(m.sessions) == 0 {
		return m.sections
	}
	m.rebuildSections()
	return m.sections
}

func (m *generalModel) refreshLayout(selectedID string) {
	sortSessionsBy(m.sessions, m.ordering, m.grouping)
	m.rebuildSections()
	m.restoreSel(selectedID)
}

// setLayout applies a grouping and ordering pair, regrouping the board around
// the same selected session.
func (m *generalModel) setLayout(g groupingMode, o orderingMode) {
	id := m.selectedID()
	m.grouping, m.ordering = g, o
	m.refreshLayout(id)
}

func (m generalModel) layoutLabel() string {
	return "group: " + groupingLabels[m.grouping] + " · order: " + orderingLabels[m.ordering]
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
// then newest category entry within each group — the same order restoreSel searches)
// without building a full copy of the board (R4.1.2): m.sel is a position in
// that order, and finding one element at a position needs no allocation.
func (m generalModel) selected() (protocol.SessionView, bool) {
	i := 0
	for _, section := range m.sectionOrder() {
		for _, s := range m.sessions {
			if m.groupKey(s) != section {
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
		for _, section := range m.sectionOrder() {
			for _, s := range m.sessions {
				if m.groupKey(s) != section {
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
	m.refreshLayout(selID)

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
			m.general.edit = newLineEditor(s.Name)
		}
	case k.Text == "t":
		if s, ok := m.general.selected(); ok {
			m.general.editing = true
			m.general.editTag = true
			m.general.editID = s.ID
			m.general.edit = newLineEditor(s.Tag)
		}
	case k.Text == "o":
		// Open the options window seeded with the live layout (owner decision
		// 2026-08-27: one form with arrow navigation, not one key per setting).
		m.optionsGeneration++
		var cmd tea.Cmd
		m.options, cmd = newOptionsModel(m.general.grouping, m.general.ordering, m.client, m.optionsGeneration)
		m.screen = screenOptions
		return m, cmd
	case k.Text == "h":
		// Open the handoff form against the selected source. ADR-010 Amendment 4 E2:
		// this opens on ANY row -- ended, lost, busy and permission-blocked included.
		// Raw status used to REFUSE here, which was inverted: it refused at precisely
		// the moment a source cannot cooperate, the only moment a hands-off handoff is
		// needed. Status now suggests the form's default method and never decides;
		// the supervised method still revalidates the row at submit.
		if s, ok := m.general.selected(); ok {
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
// deletes before that cursor, and printable text inserts there. Exact fast-editing
// gestures are matched before their exact unmodified counterparts so an unsupported
// modified key cannot silently degrade to a one-rune edit. The target is the session
// captured when the edit opened (editID), not the live selection.
func (m rootModel) updateRename(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.Code {
	case tea.KeyEnter:
		id, value, editingTag := m.general.editID, m.general.edit.text, m.general.editTag
		m.general.closeEdit()
		if editingTag {
			// Trim here too (the daemon trims as well) so the optimistic apply and
			// the banner see the cleared tag a blank edit means.
			return m, setTagCmd(m.client, id, strings.TrimSpace(value))
		}
		return m, renameCmd(m.client, id, value)
	case tea.KeyEsc:
		m.general.closeEdit()
	default:
		m.general.edit.update(k)
	}
	return m, nil
}

// closeEdit exits the inline rename mode and clears its buffer/target.
func (m *generalModel) closeEdit() {
	m.editing = false
	m.editID = ""
	m.edit = lineEditor{}
	m.editTag = false
}

// pasteEdit inserts bracketed-paste content at the cursor, stripping the CR/LF a
// single-line name must never carry (mirrors the launch form's paste).
func (m *generalModel) pasteEdit(s string) {
	if !m.editing {
		return
	}
	m.edit.paste(s)
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

type tagDoneMsg struct {
	id  string
	tag string
	err error
}

func setTagCmd(c Client, id, tag string) tea.Cmd {
	return func() tea.Msg { return tagDoneMsg{id: id, tag: tag, err: c.SetTag(id, tag)} }
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
	m.refreshLayout(selID) // a now-empty section must leave the cached order (V-1)
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

func (m *generalModel) applyTag(id, tag string) {
	selectedID := m.selectedID()
	for i := range m.sessions {
		if m.sessions[i].ID == id {
			m.sessions[i].Tag = tag
			m.refreshLayout(selectedID)
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
	for _, section := range m.sectionOrder() {
		var hdr string
		if m.grouping == groupByStatus {
			g := status.Group(section)
			hdr = groupHeaderStyle(g).Render(groupHeader(g))
		} else {
			hdr = styleTitle.Render(strings.ToUpper(section))
		}
		b.WriteString("  " + hdr + "\n")
		for _, s := range m.sessions {
			if m.groupKey(s) != section {
				continue
			}
			b.WriteString(m.renderRow(s, s.Group, idx == m.sel) + "\n")
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
	rightText := itoa(running) + " running · " + itoa(needs) + " needs you"
	if m.grouping != groupByStatus || m.ordering != orderByArrival {
		rightText = m.layoutLabel() + " · " + rightText
	}
	if m.width > 0 {
		rightText = clampCells(rightText, m.width-lipgloss.Width(left)-3)
	}
	right := styleDim.Render(rightText)
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
		prefix = styleAccent.Render("▌") + " "
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
	// Accent markers share one cell budget with the summary and take precedence over
	// it: the summary yields, then the markers themselves clamp, so the row never
	// exceeds the terminal.
	var markers []string
	// THE INSTANT, NOT THE FLAG. RemoteControlled is still the daemon's answer to "is somebody
	// driving this" -- it gates the supervisor and the roster poller's diff key -- but a row
	// cannot be WORDED from a boolean, and a lease carries no instant to word it with. A row
	// with a lease and no delivered message therefore says nothing, which is correct: there is
	// no event to report, and no shipped client has taken a lease since R6 replaced
	// take_control with composer_send.
	if s.RemoteActivityAt != nil {
		markers = append(markers, phoneSentMarker(*s.RemoteActivityAt))
	}
	if s.SupervisionPending {
		markers = append(markers, supervisionMarker(s, m.sessions))
	}
	if s.BackendPlanError != "" {
		// The degraded-backend marker (lifecycle R1): the PTY runs but nothing
		// serves the attach channel. A row has room only for the fact; the full
		// reason is on `swarm ls` and `swarm doctor`. PLAIN text like its two
		// siblings: the joined marker is clamped (clampCells requires unstyled
		// input) and styled ONCE below (R1 audit: codex finding 4).
		markers = append(markers, "no backend")
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
			styleAccent.Render(marker)
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
		return editViewport(m.edit.text, m.edit.cursor, contentWidth)
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

// phoneSentPrefix and phoneSentClock are the marker's two halves: what the phone DID, and
// WHEN. Words, not a glyph: the roster is read at a glance and a decorative symbol here would
// be one more thing to learn. Device NAME display is deliberately out of scope -- the daemon
// reports the session's activity, and naming the device needs a deviceID accessor plus a
// registry lookup that does not exist yet.
//
// IT NO LONGER SAYS "CONTROL" (conversation surface, Wave G item G.2). Control was a lease the
// phone took, and R1 removes take-control from the product; what the daemon actually observes
// is a MESSAGE ARRIVING, at an instant, and it ages out (skeleton.phoneActiveHorizon).
//
// AND IT NO LONGER SAYS JUST "phone", which is the correction this pair exists for. The short
// form was defended on the ground that the marker's own presence carries the recency claim --
// it appears on a send and vanishes a couple of minutes later -- and that is true of the
// mechanism and invisible to the reader, who sees one word and no horizon. Worse, a bare noun
// sitting beside "supervisor pending" and "supervisor gone" reads as a CONDITION: a phone is
// on this session. That is the presence claim plan G.5 rules out in as many words, and
// nothing on this wire measures it. An event, with its time, says only what was seen.
//
// The clock is 24-hour and zero-padded so the column never ragged-edges and never needs an
// am/pm a marker has no room for.
const (
	phoneSentPrefix = "phone sent "
	phoneSentClock  = "15:04"
)

// phoneSentMarker is the row's words for a session a phone has messaged. The instant is
// rendered in the READER'S zone: the daemon may be elsewhere, and the person reading the row
// is comparing it against their own clock.
func phoneSentMarker(at time.Time) string {
	return phoneSentPrefix + at.Local().Format(phoneSentClock)
}

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
