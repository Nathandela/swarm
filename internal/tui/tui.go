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
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/version"
)

// Client is the narrow, stub-friendly daemon surface the TUI needs. Attach is
// deliberately absent here — it is Epic 8. Every method is safe to back with an
// in-memory fake.
type Client interface {
	List() ([]protocol.SessionView, error)
	// Launch returns the new session's namespaced id and the daemon's CANONICAL
	// (sanitized/truncated) name for it; an older daemon whose reply predates naming
	// returns an empty name, and the producer falls back to the request name.
	Launch(protocol.LaunchReq) (id, name string, err error)
	Kill(id string) error
	Delete(id string) error
	// Rename changes a session's display label (v0.5). An older daemon without the op
	// returns an error, which the caller banners (skew-safe).
	Rename(id, name string) error
	Subscribe() (<-chan protocol.Event, error)
}

// AgentInfo describes one detected agent CLI for the launch-form picker: whether
// it is installed and within the supported version range, the reason shown when
// it is not usable (L-2), and its declarative option schema.
type AgentInfo struct {
	Name      string
	Installed bool
	InRange   bool
	Reason    string // human-readable cause when unusable (e.g. "unsupported version 3.0.0")
	Options   []adapter.OptionSpec
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

// connectionLostMsg signals the Subscribe stream's channel closed: the daemon
// connection is gone for good (pump eviction, daemon crash/restart all look the
// same from here — protocol.Client closes eventsCh once its read loop dies).
// waitForEvent deliberately does not re-arm after this (agents-tracker-1uq).
type connectionLostMsg struct{}

// repaintMsg drives the periodic re-render that refreshes the live elapsed-time
// column (see repaintTick).
type repaintMsg struct{}

// launchResultMsg carries the outcome of an async launch/resume so a FAILURE is
// surfaced to the user (the transient banner) instead of silently discarded (B1),
// and a SUCCESS carries the daemon-returned session id + agent so the router can
// auto-attach straight into the new session (bd agents-tracker-stc).
type launchResultMsg struct {
	id    string // namespaced id of the new session on success ("" if the producer omits it)
	agent string // the new session's agent, for the attach chrome label
	name  string // the new session's label (P2); carried into the auto-attach chrome hint
	err   error
}

// beginUpgradeMsg kicks the daemon auto-restart from Init through Update (which owns
// model mutation), so the once-per-process guard and the upgrade banner are set in one
// place (bd agents-tracker-5jl).
type beginUpgradeMsg struct{}

// daemonRestartedMsg carries the result of an auto-restart + reconnect: the fresh
// client on success, or the error to surface.
type daemonRestartedMsg struct {
	client Client
	err    error
}

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

	// connectionLost is PERSISTENT (unlike the transient V-5 banner): once the
	// daemon connection is gone for good, the roster is frozen forever for the
	// life of this process, so the indicator must survive past bannerDuration
	// instead of fading while the freeze remains (agents-tracker-1uq).
	connectionLost bool

	// Build identities for the version-skew notice (E13.2): the daemon's build,
	// reported on the hello (via the optional Client.BuildVersion surface), and this
	// client's own build. A persistent notice shows while they differ (see skewNotice).
	daemonVersion string
	clientVersion string

	// Auto-restart of an outdated daemon (bd agents-tracker-5jl): when this client is
	// newer than the daemon it reached, restarter restarts the daemon and reconnects.
	// restartAttempted is the once-per-process guard against restart loops.
	restarter        DaemonRestarter
	restartAttempted bool
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
		client:        c,
		detect:        detect,
		screen:        screenGeneral,
		general:       newGeneralModel(boundedList(c)),
		events:        events,
		ticking:       true, // Init arms the first repaint tick
		clientVersion: version.Version,
	}
	// The daemon's build version rides the hello handshake. The narrow tui.Client
	// interface stays free of it (Attach-style optional surface): the production
	// *protocol.Client reports it; a fake that does not simply yields no notice.
	if bv, ok := c.(interface{ BuildVersion() string }); ok {
		m.daemonVersion = bv.BuildVersion()
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
	cmds := []tea.Cmd{waitForEvent(m.events), repaintTick(), detectCmd(m.detect, m.detectGen)}
	if m.shouldAutoUpgrade() {
		// An older daemon than this client: kick the auto-restart through Update, which
		// owns model mutation (the banner + once-per-process guard live there).
		cmds = append(cmds, func() tea.Msg { return beginUpgradeMsg{} })
	}
	return tea.Batch(cmds...)
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
// yields no command. A closed channel (the connection is gone for good) yields
// connectionLostMsg instead of a bare nil, so Update can surface it (agents-
// tracker-1uq) rather than the caller silently hanging or looping forever.
func waitForEvent(ch <-chan protocol.Event) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return connectionLostMsg{}
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

	case connectionLostMsg:
		// The daemon connection is gone for good: set the PERSISTENT indicator (see
		// generalStatus) so it survives past the transient banner's bannerDuration —
		// a 4s banner would fade while the roster stays frozen, looking like false
		// liveness. The transient banner still fires too, for immediacy. waitForEvent
		// is deliberately NOT re-armed here, and the next repaintMsg halts its own
		// tick (see repaintMsg case) — there is nothing left to wait on or refresh
		// (agents-tracker-1uq).
		m.connectionLost = true
		return m, m.general.setBanner("connection to daemon lost - restart swarm to reconnect")

	case launchResultMsg:
		// A launch/resume failed: surface the reason on the general board's banner
		// (B1 — never a silent failure) and stay on the board.
		if msg.err != nil {
			return m, m.general.setBanner("launch failed: " + msg.err.Error())
		}
		// Success: auto-attach straight into the new session (bd agents-tracker-stc),
		// reusing the exact attach path Enter uses on a running row (the session is
		// freshly launched, so read-write). A result with no id (a producer that does
		// not carry it) stays on the board — the pre-stc behavior.
		if msg.id != "" {
			s := protocol.SessionView{ID: msg.id, Agent: msg.agent, Name: msg.name}
			if m.attachRunner != nil {
				return m, runAttach(m.attachRunner, s, false)
			}
			m.attach = attachModel{session: s, hasSession: true, width: m.width}
			m.screen = screenAttach
		}
		return m, nil

	case beginUpgradeMsg:
		// The hello revealed an older daemon than this client (bd agents-tracker-5jl):
		// auto-restart it instead of asking the user. Guard once per process against
		// restart loops. Banner the upgrade, then fire the restart+reconnect command.
		if m.restartAttempted || m.restarter == nil {
			return m, nil
		}
		m.restartAttempted = true
		banner := m.general.setBanner("upgrading daemon " + m.daemonVersion + " -> " + m.clientVersion + "...")
		return m, tea.Batch(banner, restartDaemonCmd(m.restarter))

	case daemonRestartedMsg:
		// The restart+reconnect resolved. A failure banners the reason (never silent).
		if msg.err != nil {
			return m, m.general.setBanner("daemon upgrade failed: " + msg.err.Error())
		}
		// Swap to the fresh client and re-read its (now matching) build version, which
		// clears the skew. Sessions survive the restart (shims own the PTYs), so the
		// board stays accurate; we only re-subscribe for future events. The old client's
		// pending waitForEvent is left blocked on its dead stream — a one-time,
		// process-lifetime cost of the once-per-process upgrade.
		m.client = msg.client
		if bv, ok := msg.client.(interface{ BuildVersion() string }); ok {
			m.daemonVersion = bv.BuildVersion()
		}
		events, serr := msg.client.Subscribe()
		if serr != nil {
			// The reconnect succeeded but re-subscribing did not: surface it instead of
			// silently leaving m.events nil (a live-looking board that never updates —
			// codex+Fable item 3). Events stay nil, but the user is told, not left dark.
			return m, m.general.setBanner("daemon upgraded but event stream failed: " + serr.Error())
		}
		m.events = events
		return m, tea.Batch(m.general.setBanner("daemon upgraded"), waitForEvent(m.events))

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
		// Bracketed paste routes into the launch form's focused text field, or into the
		// general board's inline rename buffer when a rename is open (newlines stripped
		// for these single-line fields); elsewhere it is ignored.
		switch {
		case m.screen == screenLaunch:
			m.launch.paste(msg.Content)
		case m.screen == screenGeneral && m.general.editing:
			m.general.pasteEdit(msg.Content)
		}
		return m, nil

	case attachDoneMsg:
		// The passthrough returned (detached / session ended / error); come back to
		// the general board and re-arm its repaint tick. A FAILURE (e.g. the attach
		// dial failing) is surfaced on the banner rather than silently dropping the
		// user back on the board (agents-tracker-1uq; both branches implemented the
		// identical behavior independently).
		cmd := m.enterGeneral()
		if msg.err != nil {
			return m, tea.Batch(cmd, m.general.setBanner("attach failed: "+msg.err.Error()))
		}
		return m, cmd

	case deleteDoneMsg:
		// A delete succeeded: drop the row immediately (optimistic removal — the later
		// daemon event is then a no-op) and acknowledge it, so the board never looks
		// stale. A failure is surfaced instead of silently discarded.
		if msg.err != nil {
			return m, m.general.setBanner("delete failed: " + msg.err.Error())
		}
		m.general.remove(msg.id)
		m.general.tombstone(msg.id) // a late buffered event must not resurrect the row (item 6)
		return m, m.general.setBanner("session deleted")

	case killDoneMsg:
		// A kill failure is surfaced; a success is a no-op here — the daemon event
		// transitions the (still-listed) row to completed.
		if msg.err != nil {
			return m, m.general.setBanner("kill failed: " + msg.err.Error())
		}
		return m, nil

	case renameDoneMsg:
		// A rename failed (an older daemon's skew refusal, or a rejected id): surface
		// it on the banner rather than silently swallowing it. On success, update the
		// row's label immediately (optimistic); the daemon's roster event re-applies
		// the same name so all clients converge through the normal event path.
		if msg.err != nil {
			return m, m.general.setBanner("rename failed: " + msg.err.Error())
		}
		m.general.applyName(msg.id, msg.name)
		return m, nil

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
		if m.connectionLost {
			// The roster is frozen forever once the connection is lost — halt the
			// timer for good so the elapsed-time column stops advancing over data
			// that will never update again (false liveness, agents-tracker-1uq).
			m.ticking = false
			return m, nil
		}
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
// (general.view) to the persistent bottom bar (A-5, ADR-006). A lost connection
// (agents-tracker-1uq) takes priority over both: the roster is frozen and staying
// frozen, which outranks a mid-confirm prompt or the normal keymap.
func (m rootModel) generalStatus() string {
	if m.connectionLost {
		return "daemon connection lost - restart swarm"
	}
	if m.general.editing {
		return "type new name   ⏎ save   esc cancel"
	}
	if m.general.confirm {
		return "y confirm   n cancel"
	}
	// The attach hint teaches the detach key inline (ctrl+q returns), since the attach
	// chrome now defaults off (ADR-006, item 5) and no longer carries the hint itself.
	return "↑↓ navigate   ⏎ attach (ctrl+q returns)   e rename   n new   ctrl+x kill   esc quit"
}

// DaemonRestarter restarts the daemon and returns a freshly-connected client to the
// replacement (which reports its new build version via BuildVersion). It is the
// client-side reuse of `swarm daemon restart` (cmd/swarm wires daemon.Restart +
// protocol.Dial). A nil restarter disables auto-upgrade — the notice/no-op path.
type DaemonRestarter func() (Client, error)

// WithDaemonRestarter injects the auto-restart seam used when this client is newer
// than the daemon it reached (bd agents-tracker-5jl). Without it the client only shows
// the passive notice (and only for the direction it can self-heal — see classifySkew).
func WithDaemonRestarter(r DaemonRestarter) Option {
	return func(m *rootModel) { m.restarter = r }
}

// restartDaemonCmd runs the injected restart+reconnect off the update loop and reports
// its outcome as a daemonRestartedMsg.
func restartDaemonCmd(r DaemonRestarter) tea.Cmd {
	return func() tea.Msg {
		c, err := r()
		return daemonRestartedMsg{client: c, err: err}
	}
}

// shouldAutoUpgrade reports whether Init should auto-restart the daemon: a restarter is
// injected, no restart has been attempted this process (the loop guard), and the daemon
// is OLDER than this client (the direction the client can self-heal — see classifySkew).
func (m rootModel) shouldAutoUpgrade() bool {
	return m.restarter != nil && !m.restartAttempted && classifySkew(m.daemonVersion, m.clientVersion) == skewUpgrade
}

// skew classifies a daemon/client build-version pair into the reconciliation action.
type skew int

const (
	skewNone    skew = iota // builds match, or either is a dev/empty build (always mismatch)
	skewNotify              // daemon NEWER than client: passive notice (the client cannot self-heal)
	skewUpgrade             // daemon OLDER than client: auto-restart to reconcile (bd agents-tracker-5jl)
)

// classifySkew compares the daemon and client build versions. It suppresses when either
// side is a dev/unstamped build or no daemon version was reported (those always
// mismatch). Otherwise the direction decides: a client newer than the daemon can restart
// the daemon into its own (newer) build, so it auto-upgrades; a daemon newer than the
// client is a state the client cannot fix by restarting, so it only warns.
func classifySkew(daemonVer, clientVer string) skew {
	if daemonVer == "" || clientVer == "" || daemonVer == "dev" || clientVer == "dev" {
		return skewNone
	}
	switch compareSemver(daemonVer, clientVer) {
	case 0:
		return skewNone
	case 1:
		return skewNotify // daemon newer
	default:
		return skewUpgrade // daemon older
	}
}

// skewNotice returns the persistent version-skew notice line, or "" when there is
// nothing to warn about. It is now kept ONLY for the downgrade direction (daemon NEWER
// than client): the client-newer direction auto-restarts the daemon (bd
// agents-tracker-5jl), so it no longer nudges the manual restart there.
func skewNotice(daemonVer, clientVer string) string {
	if classifySkew(daemonVer, clientVer) != skewNotify {
		return ""
	}
	return "daemon " + daemonVer + " differs from swarm " + clientVer + " - run: swarm daemon restart"
}

// compareSemver compares two version strings by SemVer precedence, returning -1, 0, or
// +1. A leading "v" and any build ("+...") metadata are ignored. The dotted-numeric core
// is compared first; when the cores are equal a version WITH a pre-release ("-...") sorts
// BEFORE the same version without one (0.4.0-rc.1 < 0.4.0), and two pre-releases compare
// by their suffix string (rc.1 < rc.2). This is what lets a client on the final build
// recognize an rc daemon as OLDER and auto-upgrade it. A non-numeric core component
// compares as 0. Pure and total.
func compareSemver(a, b string) int {
	aCore, aPre := splitSemver(a)
	bCore, bPre := splitSemver(b)
	an := semverParts(aCore)
	bn := semverParts(bCore)
	for i := 0; i < len(an) || i < len(bn); i++ {
		var av, bv int
		if i < len(an) {
			av = an[i]
		}
		if i < len(bn) {
			bv = bn[i]
		}
		switch {
		case av < bv:
			return -1
		case av > bv:
			return 1
		}
	}
	// Numeric cores are equal: apply SemVer pre-release precedence.
	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "": // a is the release, b a pre-release of it -> a is greater
		return 1
	case bPre == "":
		return -1
	default:
		return strings.Compare(aPre, bPre)
	}
}

// splitSemver trims a leading "v" and any build metadata, then splits a version into its
// numeric core and its pre-release suffix (everything after the first "-").
func splitSemver(v string) (core, pre string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i] // drop build metadata (never part of precedence)
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// semverParts splits a version core (already stripped of its "v" prefix and any
// pre-release/build suffix by splitSemver) into its dotted numeric components, stopping
// at the first non-numeric component.
func semverParts(core string) []int {
	var parts []int
	for _, seg := range strings.Split(core, ".") {
		n, err := strconv.Atoi(seg)
		if err != nil {
			break
		}
		parts = append(parts, n)
	}
	return parts
}

// composeBoard anchors a one-line status bar on the bottom row of a full-screen
// board: the body fills the rows above it — padded with blank rows, or clipped if it
// would overflow — and the dim bar occupies the final row. A version-skew notice, when
// present, takes the row directly above the bar (persistent, distinct from a session
// row). Before the first WindowSizeMsg (height unknown) the tail simply follows the body.
func (m rootModel) composeBoard(body, status string) string {
	// Clamp the status text to the terminal width (less its 2-cell indent) so the bar
	// can never wrap onto a second row and break the fixed-height board contract. The
	// PLAIN text is clamped before styling, so an ANSI escape is never cut mid-sequence.
	if m.width > 2 {
		status = clampCells(status, m.width-2)
	}
	bar := "  " + styleDim.Render(status)

	// The version-skew notice reserves its own row above the bar, clamped and styled the
	// same way so it likewise cannot wrap.
	notice := skewNotice(m.daemonVersion, m.clientVersion)
	var noticeRow string
	if notice != "" {
		if m.width > 2 {
			notice = clampCells(notice, m.width-2)
		}
		noticeRow = "  " + styleTitle.Render(notice)
	}

	if m.height <= 0 {
		// Height unknown (before the first WindowSizeMsg): the tail simply follows the body.
		out := body
		if noticeRow != "" {
			out += "\n" + noticeRow
		}
		return out + "\n" + bar
	}

	// Build the tail within the height budget, dropping the notice FIRST when there is not
	// room for both it and the bar, so the composed board never exceeds m.height. The
	// status bar is the last-resort row (kept whenever height >= 1); the body fills what
	// remains above it.
	tail := []string{bar}
	// The notice needs height >= 3 so it never evicts the entire body: below that the
	// notice is dropped FIRST (policy: notice, then body; the bar is the last resort).
	if noticeRow != "" && m.height >= 3 {
		tail = []string{noticeRow, bar}
	}
	target := m.height - len(tail)
	if target < 0 {
		target = 0
	}
	lines := strings.Split(body, "\n")
	if len(lines) > target {
		lines = lines[:target]
	} else {
		lines = append(lines, make([]string, target-len(lines))...)
	}
	return strings.Join(append(lines, tail...), "\n")
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
// displayName is the identity shown for a session: its user-provided label when set,
// else the bare agent name. The fallback keeps the identity column non-blank when a
// session carries no name — an older, name-unaware daemon, or one launched before the
// field existed (P2 / version-skew safety).
func displayName(s protocol.SessionView) string {
	if s.Name != "" {
		return s.Name
	}
	return s.Agent
}

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
