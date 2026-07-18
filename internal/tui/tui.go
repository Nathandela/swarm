// Package tui is the Epic 7 client-side terminal UI: a screen router that hosts
// the general view (grouped session board), the launch form, and an attach
// placeholder (attach passthrough is Epic 8). It talks to the daemon only through
// the narrow Client interface, so every unit test drives it against an in-memory
// stub — no live daemon, no socket (E7.7).
//
// The whole program is a single tea.Model (the router). The general/launch/attach
// sub-models are plain structs the router dispatches to; the router is the only
// shared shell. New eagerly performs the initial List so the first paint already
// lists every session (N-1, the eager-load pin).
package tui

import (
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// Client is the narrow, stub-friendly daemon surface the TUI needs. Attach is
// deliberately absent here — it is Epic 8. Every method is safe to back with an
// in-memory fake.
type Client interface {
	List() ([]protocol.SessionView, error)
	Launch(protocol.LaunchReq) (string, error)
	Kill(id string) error
	Delete(id string) error
	Subscribe() (<-chan protocol.Event, error)
}

// AgentInfo describes one detected agent CLI for the launch-form picker: whether
// it is installed and within the supported version range, the install/upgrade
// hint shown when it is not usable (L-2), and its declarative option schema.
type AgentInfo struct {
	Name        string
	Installed   bool
	InRange     bool
	InstallHint string
	Reason      string // human-readable cause when unusable (e.g. "unsupported version 3.0.0"); falls back to InstallHint
	Options     []adapter.OptionSpec
}

// usable reports whether the agent can actually be launched (installed and in a
// supported version range). Unusable agents are greyed and carry a hint.
func (a AgentInfo) usable() bool { return a.Installed && a.InRange }

// DetectFunc probes the machine for available agent CLIs. It is called each time
// the launch form opens so the picker reflects the current install state.
type DetectFunc func() []AgentInfo

// screen identifies which sub-model the router is currently showing.
type screen int

const (
	screenGeneral screen = iota
	screenLaunch
	screenAttach
)

// eventMsg carries one Subscribe status-change into the update loop.
type eventMsg struct{ ev protocol.Event }

// repaintMsg drives the periodic re-render that refreshes the live elapsed-time
// column (see repaintTick).
type repaintMsg struct{}

// launchResultMsg carries the outcome of an async launch/resume so a FAILURE is
// surfaced to the user (the transient banner) instead of silently discarded (B1).
type launchResultMsg struct{ err error }

// detectMsg carries the result of an async agent-detection probe. Detection runs
// off the Update hot path (a Cmd from Init and again on each form open) and is
// cached on the model, so opening the launch form never blocks on a slow prober
// (the P0 field-test freeze). It updates a live form's picker in place.
//
// gen is the dispatch generation the probe was stamped with (see detectGen): an
// older slow probe landing after a newer dispatch carries a stale generation and is
// dropped, so it cannot restore stale availability (the Init/form-open ordering race).
type detectMsg struct {
	gen    uint64
	agents []AgentInfo
}

// repaintInterval is how often the general view re-emits its frame to refresh the
// live elapsed-time column. Elapsed granularity is seconds and coarser, so a
// one-second cadence suffices (down from the former 200 ms, cutting the idle
// redraw rate 5x per N-3), and the tick only runs on the general view — never on
// the launch/attach screens.
//
// F2 note: a repaint MUST re-emit the whole frame so a static board refreshes its
// elapsed column (and so the frozen TestLiveness_EventMovesRowGroup, which drains
// the teatest output between its two pre-event waits, sees the board on the second
// wait). In charm.land/bubbletea v2's renderer this needs BOTH: the SGR nonce
// (bumped each tick, consumed in View) to make the frame content differ so the
// renderer does not early-return on an unchanged View, AND tea.ClearScreen to
// force a full redraw rather than an empty cell diff. They are complementary, not
// redundant — dropping either yields zero re-emission and regresses that frozen
// test. The achievable N-3 wins here are the slower cadence and general-view-only
// scope, not removing the periodic clear.
const repaintInterval = time.Second

func repaintTick() tea.Cmd {
	return tea.Tick(repaintInterval, func(time.Time) tea.Msg { return repaintMsg{} })
}

// rootModel is the screen router: the only tea.Model, holding the three
// sub-models and the shared client/size state.
type rootModel struct {
	client Client
	detect DetectFunc

	width  int
	height int

	screen  screen
	general generalModel
	launch  launchModel
	attach  attachModel

	attachRunner AttachRunner // injected passthrough (nil -> Epic 7 placeholder)

	agents    []AgentInfo // cached agent detection (async; empty until the first detectMsg)
	detected  bool        // whether a detectMsg has landed (else the form shows "checking...")
	detectGen uint64      // latest dispatched detection generation; a detectMsg with an older gen is stale

	events   <-chan protocol.Event
	repaintN int  // repaint nonce: bumped each tick to force a full re-emit (see View)
	ticking  bool // whether a repaint tick is in flight (only on the general view)
}

// listDialTimeout bounds New's eager List so a wedged daemon cannot stall the first
// paint. A live local daemon answers in single-digit milliseconds; this only bites
// when the daemon is hung, in which case New comes up with an empty (still usable)
// board and the Subscribe stream fills it in once the daemon responds.
const listDialTimeout = time.Second

// New builds the router. It eagerly lists sessions (N-1: the first paint must
// already show them) under a bounded dial (a hung daemon cannot stall first paint)
// and opens the subscribe stream so Init can wire live updates. Errors from the
// client are non-fatal — the model still renders. Options (WithAttachRunner) are
// applied last; the 2-arg form used across the suite is unaffected.
func New(c Client, detect DetectFunc, opts ...Option) tea.Model {
	events, _ := c.Subscribe()
	m := rootModel{
		client:  c,
		detect:  detect,
		screen:  screenGeneral,
		general: newGeneralModel(boundedList(c)),
		events:  events,
		ticking: true, // Init arms the first repaint tick
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// boundedList performs the eager List under listDialTimeout so a hung daemon cannot
// stall first paint. On timeout it returns an empty board; the blocked List
// goroutine drains into the buffered channel and is collected when it finally
// returns.
func boundedList(c Client) []protocol.SessionView {
	ch := make(chan []protocol.SessionView, 1)
	go func() {
		s, _ := c.List()
		ch <- s
	}()
	select {
	case s := <-ch:
		return s
	case <-time.After(listDialTimeout):
		return nil
	}
}

// Init wires the Subscribe stream and kicks the first agent-detection probe. The
// probe runs asynchronously (detectCmd) so a slow prober never delays first paint
// or the launch form; its result is cached via detectMsg (V-2/L1).
func (m rootModel) Init() tea.Cmd {
	return tea.Batch(waitForEvent(m.events), repaintTick(), detectCmd(m.detect, m.detectGen))
}

// detectCmd probes for agent CLIs off the Update hot path and delivers the result
// as a detectMsg stamped with gen (the generation captured at dispatch), so a stale
// probe can be recognized and dropped when it resolves after a newer one. A nil
// detector yields no command.
func detectCmd(detect DetectFunc, gen uint64) tea.Cmd {
	if detect == nil {
		return nil
	}
	return func() tea.Msg { return detectMsg{gen: gen, agents: detect()} }
}

// waitForEvent reads one event from the stream. A nil channel (no subscription)
// yields no command.
func waitForEvent(ch <-chan protocol.Event) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg{ev}
	}
}

func (m rootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.general.width = msg.Width
		m.launch.width = msg.Width
		m.attach.width = msg.Width
		return m, nil

	case eventMsg:
		// A status change updates the affected row in place and, on a transition
		// into needs_input/ready_for_review, prints a notification banner (V-5).
		// Re-arm the stream so the next event is delivered too.
		banner := m.general.apply(msg.ev.Session)
		return m, tea.Batch(banner, waitForEvent(m.events))

	case launchResultMsg:
		// A launch/resume failed: surface the reason on the general board's banner
		// (B1 — never a silent failure). A success is a no-op; the new session arrives
		// via the subscribe stream.
		if msg.err != nil {
			return m, m.general.setBanner("launch failed: " + msg.err.Error())
		}
		return m, nil

	case detectMsg:
		// Drop a stale probe: one dispatched before the latest generation that only
		// now resolved. Applying it would restore stale availability over the newer
		// result (the Init/form-open ordering race — item 6).
		if msg.gen != m.detectGen {
			return m, nil
		}
		// Cache the probe result. If the launch form is open, refresh its picker in
		// place so agent availability greys/ungreys live without discarding the form.
		m.agents = msg.agents
		m.detected = true
		if m.screen == screenLaunch {
			m.launch.refreshAgents(msg.agents)
		}
		return m, nil

	case tea.PasteMsg:
		// Bracketed paste routes into the launch form's focused text field (newlines
		// stripped for the single-line fields); elsewhere it is ignored.
		if m.screen == screenLaunch {
			m.launch.paste(msg.Content)
		}
		return m, nil

	case attachDoneMsg:
		// The passthrough returned (detached / session ended / error); come back to
		// the general board and re-arm its repaint tick.
		return m, m.enterGeneral()

	case bannerExpireMsg:
		// The transient banner reached its expiry; re-emit the general frame so the
		// (now wall-clock-expired) banner disappears. Mirrors repaintMsg's full
		// re-emit (SGR nonce + ClearScreen); a no-op off the general view.
		if m.screen != screenGeneral {
			return m, nil
		}
		m.repaintN++
		return m, tea.ClearScreen

	case repaintMsg:
		if m.screen != screenGeneral {
			// The live elapsed column only shows on the general view, so let the
			// timer lapse elsewhere (N-3: no idle repaints on the launch/attach
			// screens). It restarts on return to the general view (enterGeneral).
			m.ticking = false
			return m, nil
		}
		// The elapsed-time column is relative to now, so the board is redrawn on
		// the timer. Bumping the nonce (consumed in View) makes the frame differ
		// so the renderer does not skip it, and tea.ClearScreen forces a full
		// re-emit rather than an empty cell diff — both are needed (see the F2 note
		// on repaintInterval). Runs at repaintInterval and only on the general view.
		m.repaintN++
		return m, tea.Batch(repaintTick(), tea.ClearScreen)

	case tea.KeyPressMsg:
		switch m.screen {
		case screenGeneral:
			return m.updateGeneral(msg)
		case screenLaunch:
			return m.updateLaunch(msg)
		case screenAttach:
			return m.updateAttach(msg)
		}
	}
	return m, nil
}

func (m rootModel) View() tea.View {
	var content string
	switch m.screen {
	case screenLaunch:
		content = m.composeBoard(m.launch.view(), m.launch.hint())
	case screenAttach:
		// The attach placeholder keeps its own minimal body; the real passthrough
		// owns the terminal (internal/attach) and draws its own chrome bar (A-5).
		content = m.attach.view()
	default:
		content = m.composeBoard(m.general.view(), m.generalStatus())
	}
	// A trailing nonce of reset-SGR sequences makes each repaint's content distinct
	// so the renderer re-emits the frame (see repaintMsg). It is a no-op for the
	// terminal and is stripped by every ANSI-stripping reader, so it never reaches
	// the visible screen or the golden files.
	content += strings.Repeat("\x1b[m", m.repaintN%8+1)
	// The board is a full-screen alternate-screen app (ADR-006): it stays off the
	// terminal scrollback and gives the status bar a fixed bottom row. In
	// charm.land/bubbletea/v2 the alternate screen is requested through the View
	// (there is no WithAltScreen program option), so it is set here, not in cmd.
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// generalStatus is the context-key line for the general board — or, during a pending
// kill/delete confirm, that sub-state's keys. Promoted from the old inline footer
// (general.view) to the persistent bottom bar (A-5, ADR-006).
func (m rootModel) generalStatus() string {
	if m.general.confirm {
		return "y confirm   n cancel"
	}
	// The attach hint teaches the detach key inline (ctrl+q returns), since the attach
	// chrome now defaults off (ADR-006, item 5) and no longer carries the hint itself.
	return "↑↓ navigate   ⏎ attach (ctrl+q returns)   n new   ctrl+x kill   esc quit"
}

// composeBoard anchors a one-line status bar on the bottom row of a full-screen
// board: the body fills the rows above it — padded with blank rows, or clipped if it
// would overflow — and the dim bar occupies the final row. Before the first
// WindowSizeMsg (height unknown) the bar simply follows the body.
func (m rootModel) composeBoard(body, status string) string {
	// Clamp the status text to the terminal width (less its 2-cell indent) so the bar
	// can never wrap onto a second row and break the fixed-height board contract. The
	// PLAIN text is clamped before styling, so an ANSI escape is never cut mid-sequence.
	if m.width > 2 {
		status = clampCells(status, m.width-2)
	}
	bar := "  " + styleDim.Render(status)
	if m.height <= 0 {
		return body + "\n" + bar
	}
	lines := strings.Split(body, "\n")
	if target := m.height - 1; len(lines) > target {
		lines = lines[:target]
	} else {
		lines = append(lines, make([]string, target-len(lines))...)
	}
	return strings.Join(lines, "\n") + "\n" + bar
}

// enterGeneral switches to the general view and restarts the repaint timer if it
// has lapsed. The timer runs only on the general view (N-3); guarding on ticking
// keeps at most one tick in flight across repeated screen switches.
func (m *rootModel) enterGeneral() tea.Cmd {
	m.screen = screenGeneral
	if m.ticking {
		return nil
	}
	m.ticking = true
	return repaintTick()
}

// ---------------------------------------------------------------------------
// Shared styling + formatting helpers.
// ---------------------------------------------------------------------------

// Group palette (matches docs/design/ui-preview.html). Colors degrade to plain
// text without a TTY, so unit tests — which strip ANSI — never see them.
//
// Every Style below is a package-level var (R4.1.1): general.go/launch.go's
// render paths reuse these instead of constructing a fresh lipgloss.NewStyle()
// on every render. styleGroup*/styleGroupHeader* mirror groupStyle/
// groupHeaderStyle's switch so a per-group color+bold combination is built once
// at init, not once per row per frame.
var (
	colNeedsInput = lipgloss.Color("#ff5f5f")
	colWorking    = lipgloss.Color("#5fafff")
	colReview     = lipgloss.Color("#5fd75f")
	colCompleted  = lipgloss.Color("#8a8a8a")
	colAmber      = lipgloss.Color("#ffcf5f")

	styleTitle = lipgloss.NewStyle().Foreground(colAmber).Bold(true)
	styleDim   = lipgloss.NewStyle().Foreground(colCompleted)
	styleAgent = lipgloss.NewStyle().Bold(true)
	styleAmber = lipgloss.NewStyle().Foreground(colAmber)
	styleError = lipgloss.NewStyle().Foreground(colNeedsInput)

	styleGroupNeedsInput = styleError
	styleGroupWorking    = lipgloss.NewStyle().Foreground(colWorking)
	styleGroupReview     = lipgloss.NewStyle().Foreground(colReview)
	styleGroupCompleted  = styleDim

	styleGroupHeaderNeedsInput = styleGroupNeedsInput.Bold(true)
	styleGroupHeaderWorking    = styleGroupWorking.Bold(true)
	styleGroupHeaderReview     = styleGroupReview.Bold(true)
	styleGroupHeaderCompleted  = styleGroupCompleted.Bold(true)
)

// groupStyle returns the pre-built plain per-group color style (mirrors the
// group->color mapping formerly in groupColor, now folded in here directly).
func groupStyle(g status.Group) lipgloss.Style {
	switch g {
	case status.GroupNeedsInput:
		return styleGroupNeedsInput
	case status.GroupWorking:
		return styleGroupWorking
	case status.GroupReadyForReview:
		return styleGroupReview
	default:
		return styleGroupCompleted
	}
}

// groupHeaderStyle returns the pre-built bold per-group header style.
func groupHeaderStyle(g status.Group) lipgloss.Style {
	switch g {
	case status.GroupNeedsInput:
		return styleGroupHeaderNeedsInput
	case status.GroupWorking:
		return styleGroupHeaderWorking
	case status.GroupReadyForReview:
		return styleGroupHeaderReview
	default:
		return styleGroupHeaderCompleted
	}
}

// groupHeader is the UPPERCASE section label for a group (fixed vocabulary).
func groupHeader(g status.Group) string {
	switch g {
	case status.GroupNeedsInput:
		return "NEEDS INPUT"
	case status.GroupWorking:
		return "WORKING"
	case status.GroupReadyForReview:
		return "READY FOR REVIEW"
	default:
		return "COMPLETED"
	}
}

// groupIcon is the leading glyph shown on each row of a group.
func groupIcon(g status.Group) string {
	switch g {
	case status.GroupNeedsInput:
		return "●"
	case status.GroupWorking:
		return "◐"
	case status.GroupReadyForReview:
		return "✓"
	default:
		return "─"
	}
}

// statusToken is the per-row status word: the group name, lowercased with the
// underscore rendered as a space ("needs_input" -> "needs input").
func statusToken(g status.Group) string {
	return strings.ReplaceAll(string(g), "_", " ")
}

// padRight pads s with spaces to a minimum display width; it never truncates, so
// callers can rely on the original text surviving intact.
func padRight(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// clampCells truncates s to at most n display cells (rune/width-aware), never
// splitting a wide rune. It operates on plain (un-styled) text, so callers must clamp
// before applying ANSI styling to avoid cutting an escape sequence mid-stream.
func clampCells(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > n {
			break
		}
		b.WriteString(string(r))
		w += rw
	}
	return b.String()
}

// userHome resolves the current user's home directory, or "" if it cannot be
// determined. Used for the "~" cwd column and for launch-form tilde expansion.
func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// shortenCwd replaces a leading home directory with "~" (V-4 cwd column).
func shortenCwd(cwd string) string {
	home := userHome()
	if home == "" {
		return cwd
	}
	if cwd == home {
		return "~"
	}
	if strings.HasPrefix(cwd, home+"/") {
		return "~" + cwd[len(home):]
	}
	return cwd
}

// compactElapsed renders a duration as a single compact token: seconds, minutes,
// hours, or days ("41s", "12m", "1h", "3d").
func compactElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + "h"
	default:
		return itoa(int(d.Hours()/24)) + "d"
	}
}

// itoa is a tiny non-negative int formatter (avoids importing strconv widely).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
