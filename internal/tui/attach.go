package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/attach"
	"github.com/Nathandela/swarm/internal/protocol"
)

// AttachRunner drives one attach for a selected session. Production releases the
// hosting Bubble Tea terminal, dials Client.Attach, and runs internal/attach over
// the real controlling terminal; unit tests inject a fake that records the call. A
// completed/lost row attaches READ-ONLY (G3).
type AttachRunner func(session protocol.SessionView, readOnly bool) error

// Option customizes the router at construction (see New).
type Option func(*rootModel)

// WithAttachRunner injects the attach runner invoked when Enter attaches to a row.
// Without it, Enter falls back to the Epic 7 placeholder (which only identifies the
// selected session), so the 2-arg New used across the existing suite is unaffected.
func WithAttachRunner(r AttachRunner) Option {
	return func(m *rootModel) { m.attachRunner = r }
}

// attachDoneMsg signals the passthrough returned (detached / session ended /
// error), so the router returns to the general board.
type attachDoneMsg struct{ err error }

// runAttach invokes the runner off the update loop and reports completion. The
// session and read-only flag are captured at the keypress, so a concurrent regroup
// cannot retarget the attach.
func runAttach(r AttachRunner, s protocol.SessionView, readOnly bool) tea.Cmd {
	return func() tea.Msg {
		return attachDoneMsg{err: r(s, readOnly)}
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
	return func(s protocol.SessionView, readOnly bool) error {
		// nx44.7, CORRECTED by M0.1 (docs/verification/mirror-m0.md). The dial below does
		// NOT evict the phone, and in the assembled daemon it never did: production runs TWO
		// protocol Servers over ONE coreAPI (internal/skeleton/serve.go -- owner d.srv on the
		// main UDS, remote d.remoteSrv on remote.sock), each with its OWN lease map, and
		// coreAPI.Attach subscribes to the SHARED per-session tap (internal/skeleton/api.go,
		// tap.subscribe) rather than re-dialing the shim. The single-subscriber constraint is
		// at the shim hub (internal/shim/server.go hub.attach) and the tap reaches it ONCE per
		// session, on its first subscriber; every later attach -- owner attach, remote
		// take_control, remote peek -- fans off that one upstream.
		// TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive
		// (internal/skeleton/copresence_test.go) proves both live streams survive in BOTH
		// orders, and that the phone's control lease still reaches the PTY afterwards.
		// Eviction is real only WITHIN one tier: a second OWNER attach supersedes the first on
		// d.srv's lease map (internal/protocol/server.go attach, phase 2).
		//
		// So this flag does not mean "we took the session from the phone"; it means "a phone
		// held control when this attach started". The reserved row it drives still SAYS "took
		// over from phone", which is now false -- but rewording it changes that note's contract
		// and the assertions pinning it (internal/attach/hintrow_takeover_test.go), and the
		// honest replacement is a LIVE co-presence indicator rather than a one-shot sample. That
		// is design work, deferred to M2 as agents-tracker-dwwv.3.1. Sampling once is kept
		// meanwhile: the chrome carries one value for the life of the attach either way.
		tookOver := s.RemoteControlled
		sess, cleanup, err := dial(s.ID)
		if err != nil {
			return err
		}
		if cleanup != nil {
			// Release the per-attach connection once the passthrough returns, on every
			// exit path (detach, session end, or a recovered fault — attach.Run does not
			// propagate panics), so a fresh per-attach conn is never leaked (item 1).
			defer cleanup()
		}
		if hand.Release != nil {
			_ = hand.Release()
		}
		if hand.Restore != nil {
			defer func() { _ = hand.Restore() }()
		}
		tc, err := attach.NewTermControl(os.Stdin, os.Stdout)
		if err != nil {
			return err
		}
		_, err = attach.Run(attach.Config{
			Term:     tc,
			Session:  sess,
			ReadOnly: readOnly,
			// Chrome ON (ADR-006 v0.3): the return hint gets its OWN reserved bottom row
			// — the PTY is sized to rows-1 and a DECSTBM region keeps the agent off that
			// row — so it can no longer overdraw snapshot/agent content the way the v0.2
			// top-row banner did. The output pump self-heals full-screen damage.
			Chrome:         true,
			Name:           displayName(s), // P2: identify the session by its label (falls back to the agent)
			TookOverRemote: tookOver,
		})
		return err
	}
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
