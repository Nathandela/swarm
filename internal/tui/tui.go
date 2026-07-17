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
	"image/color"
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

// repaintMsg drives the periodic full-frame repaint (see repaintTick).
type repaintMsg struct{}

// repaintInterval is how often the general view refreshes its live elapsed-time
// column. It also keeps the render stream flowing so an observer always sees the
// current frame.
const repaintInterval = 200 * time.Millisecond

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

	events   <-chan protocol.Event
	repaintN int
}

// New builds the router. It eagerly lists sessions (N-1: the first paint must
// already show them) and opens the subscribe stream so Init can wire live
// updates. Errors from the stub are non-fatal — the model still renders.
func New(c Client, detect DetectFunc) tea.Model {
	sessions, _ := c.List()
	events, _ := c.Subscribe()
	return rootModel{
		client:  c,
		detect:  detect,
		screen:  screenGeneral,
		general: newGeneralModel(sessions),
		events:  events,
	}
}

// Init wires the Subscribe stream. It returns a command that blocks on the event
// channel and re-arms itself after each event, so status changes flow in without
// polling (V-2/L1).
func (m rootModel) Init() tea.Cmd { return tea.Batch(waitForEvent(m.events), repaintTick()) }

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
		// into needs_input/ready_for_review, prints a notification banner. Re-arm
		// the stream so the next event is delivered too.
		banner := m.general.apply(msg.ev.Session)
		return m, tea.Batch(banner, waitForEvent(m.events))

	case repaintMsg:
		// Periodic repaint: the elapsed-time column is relative to now, so the
		// board is redrawn on a timer. Bumping the nonce (consumed in View) forces
		// the renderer to treat the frame as changed and ClearScreen makes it
		// re-emit the whole frame rather than a cell diff — both together, the
		// render stream always carries the current frame.
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
		content = m.launch.view()
	case screenAttach:
		content = m.attach.view()
	default:
		content = m.general.view()
	}
	// A trailing nonce of reset-SGR sequences makes each repaint's content
	// distinct so the renderer re-emits the frame (see repaintMsg). It is a
	// no-op for the terminal and is stripped by every ANSI-stripping reader, so
	// it never reaches the visible screen or the golden files.
	content += strings.Repeat("\x1b[m", m.repaintN%8+1)
	return tea.NewView(content)
}

// ---------------------------------------------------------------------------
// Shared styling + formatting helpers.
// ---------------------------------------------------------------------------

// Group palette (matches docs/design/ui-preview.html). Colors degrade to plain
// text without a TTY, so unit tests — which strip ANSI — never see them.
var (
	colNeedsInput = lipgloss.Color("#ff5f5f")
	colWorking    = lipgloss.Color("#5fafff")
	colReview     = lipgloss.Color("#5fd75f")
	colCompleted  = lipgloss.Color("#8a8a8a")
	colAmber      = lipgloss.Color("#ffcf5f")

	styleTitle = lipgloss.NewStyle().Foreground(colAmber).Bold(true)
	styleDim   = lipgloss.NewStyle().Foreground(colCompleted)
	styleAgent = lipgloss.NewStyle().Bold(true)
)

func groupColor(g status.Group) color.Color {
	switch g {
	case status.GroupNeedsInput:
		return colNeedsInput
	case status.GroupWorking:
		return colWorking
	case status.GroupReadyForReview:
		return colReview
	default:
		return colCompleted
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
