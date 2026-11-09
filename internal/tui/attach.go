package tui

import (
	"os"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/attach"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// AttachRunner drives one attach for a selected session. Production releases the
// hosting Bubble Tea terminal, dials Client.Attach, and runs internal/attach over
// the real controlling terminal; unit tests inject a fake that records the call. A
// completed/lost row attaches READ-ONLY (G3). ring answers the session-cycling keys
// (ADR-006 Amendment: session cycling keys): the runner may move to the session it
// names without returning to the board.
type AttachRunner func(session protocol.SessionView, readOnly bool, touchBack bool, ring SessionRing) error

// SessionRing names the running session step rows away from fromID (+1 next, -1
// previous) in the board's display order, wrapping at both ends. ok is false when no
// other running session exists.
type SessionRing func(fromID string, step int) (next protocol.SessionView, ok bool)

// The session-cycling keys while attached: Ctrl+P (0x10) and Ctrl+N (0x0e), the
// readline previous/next pair.
const (
	cyclePrevKey = 0x10
	cycleNextKey = 0x0e
)

// Option customizes the router at construction (see New).
type Option func(*rootModel)

// WithAttachRunner injects the attach runner invoked when Enter attaches to a row.
// Without it, Enter falls back to the Epic 7 placeholder (which only identifies the
// selected session), so the 2-arg New used across the existing suite is unaffected.
func WithAttachRunner(r AttachRunner) Option {
	return func(m *rootModel) { m.attachRunner = r }
}

// attachDoneMsg signals the passthrough returned (detached / session ended /
// error), so the router returns to the general board. last is the session the user
// cycled to last, if they cycled at all, so the board can select it.
type attachDoneMsg struct {
	err  error
	last string
}

// runAttach invokes the runner off the update loop and reports completion. The
// session, read-only flag and ring are captured at the keypress, so a concurrent
// regroup cannot retarget the attach.
func runAttach(r AttachRunner, s protocol.SessionView, readOnly, touchBack bool, ring SessionRing) tea.Cmd {
	return func() tea.Msg {
		var last string
		track := func(fromID string, step int) (protocol.SessionView, bool) {
			if ring == nil {
				return protocol.SessionView{}, false
			}
			next, ok := ring(fromID, step)
			if ok {
				last = next.ID
			}
			return next, ok
		}
		err := r(s, readOnly, touchBack, track)
		return attachDoneMsg{err: err, last: last}
	}
}

// sessionRing snapshots the board's running rows in display order -- the order Down
// walks, so rows inside a collapsed group are not part of it -- for the attach about to
// start. A snapshot, not a live view: the runner works off the update loop.
func (m generalModel) sessionRing() SessionRing {
	var rows []protocol.SessionView
	m.navigation(func(l boardLine, _ int) {
		if l.kind == lineSession && l.session.Status.Process == status.ProcessRunning {
			rows = append(rows, l.session)
		}
	})
	return func(fromID string, step int) (protocol.SessionView, bool) {
		n := len(rows)
		at := slices.IndexFunc(rows, func(s protocol.SessionView) bool { return s.ID == fromID })
		switch {
		case at < 0 && n > 0 && step > 0:
			return rows[0], true // a session not on the board yet (a fresh launch)
		case at < 0 && n > 0 && step < 0:
			return rows[n-1], true
		case at < 0 || n < 2 || step == 0:
			return protocol.SessionView{}, false
		}
		return rows[((at+step)%n+n)%n], true
	}
}

// attachChain attaches first and then follows the session-cycling keys: each
// ReasonNext / ReasonPrev attaches the ring's neighbour (or the same session again when
// there is none); any other reason, or an error, ends the chain.
func attachChain(first protocol.SessionView, ring SessionRing, attachOne func(protocol.SessionView) (attach.Reason, error)) error {
	cur := first
	for {
		reason, err := attachOne(cur)
		if err != nil {
			return err
		}
		var step int
		switch reason {
		case attach.ReasonNext:
			step = +1
		case attach.ReasonPrev:
			step = -1
		default:
			return nil
		}
		if ring != nil {
			if next, ok := ring(cur.ID, step); ok {
				cur = next
			}
		}
	}
}

// AttachDialer opens a controller attach for a session id, yielding the attach Session
// the passthrough drives AND a cleanup that releases the per-attach resources. Production
// (cmd's attachDialer) dials a FRESH connection per attach and returns its Close as the
// cleanup, so an attach never multiplexes on a long-lived client conn that a daemon
// auto-upgrade may have replaced (item 1, the blocker). The tui.Client interface omits
// Attach on purpose (it is the Epic 8 surface), so the dialer is injected alongside the
// runner.
type AttachDialer func(sessionID string) (sess attach.Session, cleanup func(), err error)

// TerminalHandoff releases the hosting program's hold on the terminal for the
// duration of the raw passthrough and restores it after. Production passes the
// *tea.Program's ReleaseTerminal / RestoreTerminal.
type TerminalHandoff struct {
	Release func() error
	Restore func() error
}

// NewAttachRunner builds the production AttachRunner: it dials the session, releases
// the tea terminal, drives internal/attach over the real controlling terminal, and
// restores the terminal on return regardless of attach.Run's Reason. It is the thin
// bubbletea-side adapter; cmd wires the dialer and terminal handoff.
func NewAttachRunner(dial AttachDialer, hand TerminalHandoff) AttachRunner {
	return func(s protocol.SessionView, readOnly, touchBack bool, ring SessionRing) error {
		// NO TAKEOVER NOTE IS SAMPLED HERE ANY MORE (conversation surface, Wave G;
		// agents-tracker-dwwv.3.1). What stood here read s.RemoteControlled once, before
		// dialing, and painted "took over from phone" on the reserved row for the life of the
		// attach. Both halves were wrong. The dial evicts nothing: production runs TWO
		// protocol Servers over ONE coreAPI, each with its own lease map, and coreAPI.Attach
		// subscribes to the SHARED per-session tap rather than re-dialing the shim, so the
		// shim's single-subscriber slot is claimed once by the tap and both live streams
		// survive -- proved in BOTH orders by
		// skeleton.TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive
		// (docs/verification/mirror-m0.md). And a value sampled before the dial could not have
		// stayed true afterwards even if it had been.
		//
		// THE HONEST REPLACEMENT IS NOT A REWORDING. A live indicator inside a running attach
		// needs a side channel into the passthrough loop, which attach.Session does not have
		// (Snapshot/Frames/Input/Resize/Detach/Generation and nothing else). So the lie is
		// deleted and nothing takes its place yet: the board row carries the phone marker,
		// where the state can be shown at all today.
		//
		// The first session is dialed BEFORE the terminal is released, so a refused dial
		// leaves the board on screen with its banner. Sessions reached with the cycling
		// keys are dialed inside the same handoff: the board is never repainted between
		// them.
		firstSess, firstCleanup, err := dial(s.ID)
		if err != nil {
			return err
		}
		firstPending := true
		defer func() {
			if firstPending && firstCleanup != nil {
				firstCleanup() // the chain never reached the first attach
			}
		}()
		if hand.Release != nil {
			_ = hand.Release()
		}
		if hand.Restore != nil {
			defer func() { _ = hand.Restore() }()
		}
		return attachChain(s, ring, func(cur protocol.SessionView) (attach.Reason, error) {
			sess, cleanup := firstSess, firstCleanup
			if firstPending {
				firstPending = false
			} else {
				var err error
				if sess, cleanup, err = dial(cur.ID); err != nil {
					return attach.ReasonError, err
				}
			}
			if cleanup != nil {
				// Release the per-attach connection once the passthrough returns, on every
				// exit path (detach, session end, or a recovered fault — attach.Run does not
				// propagate panics), so a fresh per-attach conn is never leaked (item 1).
				defer cleanup()
			}
			return runPassthrough(sess, cur, readOnly, touchBack)
		})
	}
}

// runPassthrough drives one attach.Run over the real controlling terminal.
func runPassthrough(sess attach.Session, s protocol.SessionView, readOnly, touchBack bool) (attach.Reason, error) {
	tc, err := attach.NewTermControl(os.Stdin, os.Stdout)
	if err != nil {
		return attach.ReasonError, err
	}
	return attach.Run(attach.Config{
		Term:      tc,
		Session:   sess,
		ReadOnly:  readOnly,
		TouchBack: touchBack,
		// Chrome ON (ADR-006 v0.3): the return hint gets its OWN reserved bottom row
		// — the PTY is sized to rows-1 and a DECSTBM region keeps the agent off that
		// row — so it can no longer overdraw snapshot/agent content the way the v0.2
		// top-row banner did. The output pump self-heals full-screen damage.
		Chrome: true,
		Name:   displayName(s), // P2: identify the session by its label (falls back to the agent)
		// Ctrl+P / Ctrl+N move to the previous / next running session without the
		// board in between (ADR-006 Amendment: session cycling keys).
		PrevKey: cyclePrevKey,
		NextKey: cycleNextKey,
	})
}

// ---------------------------------------------------------------------------
// Epic 7 placeholder — used only when no AttachRunner is injected. The router
// carries the selected session into it and it identifies that session (agent + cwd)
// on the thin top line; Esc backs out to the general view.
// ---------------------------------------------------------------------------

type attachModel struct {
	session    protocol.SessionView
	hasSession bool
	width      int
}

func (m rootModel) updateAttach(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if k.Code == tea.KeyEsc {
		cmd := m.enterGeneral()
		return m, cmd
	}
	return m, nil
}

func (m attachModel) view() string {
	agent := m.session.Agent
	cwd := shortenCwd(m.session.Cwd)

	top := styleDim.Render("[ ") + styleTitle.Render(agent) +
		styleDim.Render(" · "+cwd) + m.rule(agent, cwd) + styleDim.Render(" ctrl+q detach ]")

	var b strings.Builder
	b.WriteString(top + "\n\n")
	b.WriteString(styleDim.Render("attach passthrough is Epic 8 — the agent CLI's own screen renders here."))
	return b.String()
}

// rule pads the top line out toward the detach hint with a horizontal rule.
func (m attachModel) rule(agent, cwd string) string {
	used := len("[ ") + len(agent) + len(" · ") + lenRunes(cwd) + len(" ctrl+q detach ]")
	n := m.width - used - 1
	if n < 1 {
		n = 1
	}
	return " " + styleDim.Render(strings.Repeat("─", n))
}

func lenRunes(s string) int { return len([]rune(s)) }
