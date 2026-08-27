package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/Nathandela/swarm/internal/protocol"
)

// The session-board options window (owner decision 2026-08-27): grouping and
// ordering are picked in ONE form opened with `o`, rendered like the new-session
// form, applied on Enter and discarded on Esc. No per-feature cycling keys.

func TestOptionsWindowAppliesGroupingAndOrderingOnEnter(t *testing.T) {
	s := sWorking("endpoint/a", "claude", "/code/repo", "working", time.Minute)
	m := newModel(t, newFakeClient(s), detectMixed())

	m = send(m, keyRune('o'))
	rm := m.(rootModel)
	if rm.screen != screenOptions {
		t.Fatalf("o opened screen %v, want screenOptions", rm.screen)
	}
	view := stripANSI(rm.View().Content)
	for _, want := range []string{"swarm · options", "● status", "○ repo", "○ tag", "● arrival", "○ name"} {
		if !strings.Contains(view, want) {
			t.Fatalf("options view lacks %q:\n%s", want, view)
		}
	}

	m = send(m, keyRight) // group: status -> repo
	m = send(m, keyDown)  // focus: order
	m = send(m, keyRight) // order: arrival -> activity
	m = send(m, keyEnter)
	rm = m.(rootModel)
	if rm.screen != screenGeneral {
		t.Fatalf("enter left screen %v, want screenGeneral", rm.screen)
	}
	if rm.general.grouping != groupByRepo || rm.general.ordering != orderByActivity {
		t.Fatalf("applied grouping=%v ordering=%v, want repo/activity", rm.general.grouping, rm.general.ordering)
	}
	if got := stripANSI(rm.general.header()); !strings.Contains(got, "group: repo · order: activity") {
		t.Fatalf("active layout is not visible in header: %q", got)
	}

	// Reopening seeds the form with the applied layout, not the defaults.
	m = send(m, keyRune('o'))
	rm = m.(rootModel)
	if rm.options.grouping != groupByRepo || rm.options.ordering != orderByActivity {
		t.Fatalf("reopened form grouping=%v ordering=%v, want repo/activity", rm.options.grouping, rm.options.ordering)
	}
	// A roster event arriving while the window is open still lands on the board.
	m = send(m, eventMsg{ev: protocol.Event{Session: sWorking("endpoint/b", "codex", "/code/other", "working", time.Minute)}})
	m = send(m, keyEsc)
	rm = m.(rootModel)
	if _, ok := rm.general.sessionByID("endpoint/b"); !ok {
		t.Fatalf("event during options window did not reach the board: %d sessions", len(rm.general.sessions))
	}
}

func TestOptionsWindowNarrowTerminalKeepsSelectionVisible(t *testing.T) {
	const cols = 48 // a width the board is contract-tested at (responsive_board_test.go)
	m := newModel(t, newFakeClient(), detectMixed())
	m, _ = m.Update(tea.WindowSizeMsg{Width: cols, Height: 40})
	m = send(m, keyRune('o'))
	view := stripANSI(m.(rootModel).View().Content)
	// The 30-cell group row still fits; the 61-cell order row degrades to the
	// compact picker rather than clipping "name" and the ▸ affordance away.
	if !strings.Contains(view, "● status   ○ repo   ○ tag ▸") || !strings.Contains(view, "◂ arrival ▸") {
		t.Fatalf("narrow options view:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > cols {
			t.Fatalf("line overflows %d cols (%d): %q", cols, w, line)
		}
	}
}

func TestOptionsWindowEscDiscardsAndPickersWrap(t *testing.T) {
	s := sWorking("endpoint/a", "claude", "/code/repo", "working", time.Minute)
	m := newModel(t, newFakeClient(s), detectMixed())

	m = send(m, keyRune('o'))
	m = send(m, keyLeft) // group: status -> tag (wraps backward)
	m = send(m, keyUp)   // focus wraps: group -> order
	m = send(m, keyLeft) // order: arrival -> name (wraps backward)
	rm := m.(rootModel)
	if rm.options.grouping != groupByTag || rm.options.ordering != orderByName {
		t.Fatalf("form grouping=%v ordering=%v, want tag/name", rm.options.grouping, rm.options.ordering)
	}

	m = send(m, keyEsc)
	rm = m.(rootModel)
	if rm.screen != screenGeneral || rm.general.grouping != groupByStatus || rm.general.ordering != orderByArrival {
		t.Fatalf("esc: screen=%v grouping=%v ordering=%v, want general/status/arrival", rm.screen, rm.general.grouping, rm.general.ordering)
	}

	// The per-feature cycling keys are gone: g is inert on the board.
	rm = send(m, keyRune('g')).(rootModel)
	if rm.general.grouping != groupByStatus {
		t.Fatalf("g cycled grouping to %v; it must be inert", rm.general.grouping)
	}
}

// GOLDEN: the options form matches the new-session form's look. Regenerate with
// `go test ./internal/tui/ -update`.
func TestGoldenOptionsForm(t *testing.T) {
	tm := startTM(t, New(newFakeClient(), detectMixed()))
	waitContains(t, tm, "options") // general footer painted first
	tm.Send(keyRune('o'))
	waitContains(t, tm, "· options")
	quitTM(t, tm)
	teatest.RequireEqualOutput(t, []byte(finalView(t, tm)))
}
