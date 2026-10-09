package tui

// ADR-029: the session board takes mouse clicks. A left click selects the row under
// the pointer, a click on the row that is ALREADY selected opens it exactly as Enter
// does (acted on the release; no timing, so a phone tap works too), the wheel moves the selection one row without wrapping, and the board asks the
// terminal for mouse reports only while it is in its plain navigation state -- never
// in a form, the pairing modal, an inline edit, a kill/delete confirm, or an attach.
//
// Rows are located by scanning the RENDERED board for a unique summary, so every
// click below lands on the line the user would actually see that session on.

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/protocol"
)

// mouseBoard is a four-group board (one session per status section unless
// sessions are given) with an injected attach runner.
func mouseBoard(t *testing.T, sessions ...protocol.SessionView) (tea.Model, *recordingRunner) {
	t.Helper()
	if len(sessions) == 0 {
		sessions = []protocol.SessionView{
			sNeedsInput("endpoint/n1", "claude", "~/Code/a", "needs-row-summary", time.Minute),
			sWorking("endpoint/w1", "codex", "~/Code/b", "working-row-summary", 2*time.Minute),
			sReview("endpoint/r1", "claude", "~/Code/c", "review-row-summary", 3*time.Minute),
			sCompleted("endpoint/c1", "codex", "~/Code/d", "ended-row-summary", 4*time.Minute),
		}
	}
	f := newFakeClient(sessions...)
	r := &recordingRunner{}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	return m, r
}

// rowY returns the screen row the rendered board shows needle on.
func rowY(t *testing.T, m tea.Model, needle string) int {
	t.Helper()
	for y, line := range strings.Split(view(m), "\n") {
		if strings.Contains(line, needle) {
			return y
		}
	}
	t.Fatalf("no rendered line contains %q:\n%s", needle, view(m))
	return -1
}

func press(y int) tea.MouseClickMsg { return tea.MouseClickMsg{X: 6, Y: y, Button: tea.MouseLeft} }
func release(y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{X: 6, Y: y, Button: tea.MouseLeft}
}

func selectedOf(m tea.Model) string { return m.(rootModel).general.selectedID() }

// quiet applies msg and fails if it produced a command: a mouse event that only
// selects (or is ignored) has nothing to run, so any command here would be a stray
// attach, banner or launch.
func quiet(t *testing.T, m tea.Model, msg tea.Msg) tea.Model {
	t.Helper()
	m, cmd := m.Update(msg)
	if cmd != nil {
		t.Fatalf("%T at %+v returned a command; a plain select/ignore must not", msg, msg)
	}
	return m
}

// click is a press+release on row y that must only select.
func click(t *testing.T, m tea.Model, y int) tea.Model {
	t.Helper()
	return quiet(t, quiet(t, m, press(y)), release(y))
}

// settle runs cmd and feeds its message back (the attach runner's completion brings
// the router back to the board). Follow-up commands are not run: they are ticks.
func settle(m tea.Model, cmd tea.Cmd) tea.Model {
	if cmd == nil {
		return m
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	return m
}

// openClick is a press+release on row y, which must already be the selected row,
// running whatever the release returns. The press itself must stay quiet: see
// TestMouse_ClickOnTheSelectedRowAttachesOnRelease.
func openClick(t *testing.T, m tea.Model, y int) tea.Model {
	t.Helper()
	m = quiet(t, m, press(y))
	m, cmd := m.Update(release(y))
	return settle(m, cmd)
}

func TestMouse_BoardAsksForCellMotionReports(t *testing.T) {
	m, _ := mouseBoard(t)
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("board MouseMode = %v, want MouseModeCellMotion (clicks, releases, wheel; no idle motion)", got)
	}
}

func TestMouse_OffWhereverTheBoardIsNotInPlainNavigation(t *testing.T) {
	cases := []struct {
		name string
		open func(tea.Model) tea.Model
	}{
		{"launch form", func(m tea.Model) tea.Model { return send(m, keyRune('n')) }},
		{"options window", func(m tea.Model) tea.Model { return send(m, keyRune('o')) }},
		{"handoff form", func(m tea.Model) tea.Model { return send(m, keyRune('h')) }},
		{"inline rename", func(m tea.Model) tea.Model { return send(m, keyRune('e')) }},
		{"inline tag edit", func(m tea.Model) tea.Model { return send(m, keyRune('t')) }},
		{"kill/delete confirm", func(m tea.Model) tea.Model { return send(m, keyCtrlX) }},
		{"pairing modal", func(m tea.Model) tea.Model {
			return send(m, pairPendingMsg{SAS: []string{"a", "b"}, DeviceName: "phone"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := mouseBoard(t)
			m = tc.open(m)
			if got := m.View().MouseMode; got != tea.MouseModeNone {
				t.Fatalf("%s: MouseMode = %v, want MouseModeNone (native selection and paste stay the terminal's)", tc.name, got)
			}
		})
	}
}

// The attach screen never asks for mouse reports: the agent owns the terminal.
func TestMouse_OffOnTheAttachScreen(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/w1", "codex", "~/Code/b", "working-row-summary", time.Minute))
	m := New(f, detectMixed()) // no runner: Enter shows the attach placeholder screen
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	m = send(m, keyEnter)
	if m.(rootModel).screen != screenAttach {
		t.Fatalf("precondition: Enter should show the attach screen, got screen %v", m.(rootModel).screen)
	}
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("attach screen MouseMode = %v, want MouseModeNone", got)
	}
}

func TestMouse_LeftClickSelectsTheRowUnderThePointer(t *testing.T) {
	m, r := mouseBoard(t)
	if selectedOf(m) != "endpoint/n1" {
		t.Fatalf("precondition: first row selected, got %q", selectedOf(m))
	}
	m = click(t, m, rowY(t, m, "review-row-summary"))

	if got := selectedOf(m); got != "endpoint/r1" {
		t.Fatalf("click on the review row selected %q, want endpoint/r1", got)
	}
	if n := len(r.recorded()); n != 0 {
		t.Fatalf("a single click must never attach; runner called %d times", n)
	}
}

func TestMouse_ClicksOnChromeChangeNothing(t *testing.T) {
	m, r := mouseBoard(t)
	reviewY := rowY(t, m, "review-row-summary")
	rows := len(strings.Split(view(m), "\n"))
	chrome := []int{
		0,           // the "swarm" header
		1,           // the spacer under it
		reviewY - 1, // the READY FOR REVIEW section header
		reviewY + 1, // the spacer after that section
		rows - 1,    // the status bar
		rows + 5,    // below the screen
		-1,          // above it
	}
	for _, y := range chrome {
		before := selectedOf(m)
		m = click(t, m, y)
		m = click(t, m, y) // a second click on chrome opens nothing either
		if got := selectedOf(m); got != before {
			t.Fatalf("click on chrome row %d moved the selection %q -> %q", y, before, got)
		}
	}
	if n := len(r.recorded()); n != 0 {
		t.Fatalf("clicks on chrome must never attach; runner called %d times", n)
	}
}

func TestMouse_ClickOnTheSelectedRowAttachesOnRelease(t *testing.T) {
	m, r := mouseBoard(t)
	if selectedOf(m) != "endpoint/n1" {
		t.Fatalf("precondition: first row selected, got %q", selectedOf(m))
	}
	// openClick asserts the PRESS returns no command. Acting on the press would hand the
	// terminal to the agent with the release report still in flight, and that report
	// would reach the agent as input (the ADR-019 class of leak).
	m = openClick(t, m, rowY(t, m, "needs-row-summary"))

	calls := r.recorded()
	if len(calls) != 1 {
		t.Fatalf("click on the selected running row: runner called %d times, want 1", len(calls))
	}
	if calls[0].session.ID != "endpoint/n1" || calls[0].readOnly {
		t.Fatalf("runner got %q readOnly=%v, want endpoint/n1 read-write (the Enter path)", calls[0].session.ID, calls[0].readOnly)
	}
	if m.(rootModel).screen != screenGeneral {
		t.Fatal("after the passthrough returns the board must be shown again")
	}
}

// Click to select, click again to open: the desktop and phone (Termius taps) gesture.
func TestMouse_SecondClickOnARowOpensIt(t *testing.T) {
	m, r := mouseBoard(t)
	y := rowY(t, m, "working-row-summary")
	m = click(t, m, y)
	if n := len(r.recorded()); n != 0 {
		t.Fatalf("the click that selects must not attach; runner called %d times", n)
	}
	_ = openClick(t, m, y)
	if calls := r.recorded(); len(calls) != 1 || calls[0].session.ID != "endpoint/w1" {
		t.Fatalf("second click on the working row: runner got %+v, want one attach of endpoint/w1", calls)
	}
}

// "Already selected" is about the selection, not about how it got there.
func TestMouse_ClickOnARowSelectedByKeyboardOpensIt(t *testing.T) {
	m, r := mouseBoard(t)
	m = send(m, keyDown)
	if selectedOf(m) != "endpoint/w1" {
		t.Fatalf("precondition: down selects endpoint/w1, got %q", selectedOf(m))
	}
	_ = openClick(t, m, rowY(t, m, "working-row-summary"))
	if calls := r.recorded(); len(calls) != 1 || calls[0].session.ID != "endpoint/w1" {
		t.Fatalf("click on the keyboard-selected row: runner got %+v, want one attach of endpoint/w1", calls)
	}
}

func TestMouse_ClickOnTheSelectedEndedRowBannersLikeEnter(t *testing.T) {
	m, r := mouseBoard(t)
	for i := 0; i < 3; i++ {
		m = send(m, keyDown)
	}
	if selectedOf(m) != "endpoint/c1" {
		t.Fatalf("precondition: the ended row selected, got %q", selectedOf(m))
	}
	y := rowY(t, m, "ended-row-summary")
	m = quiet(t, m, press(y))
	m = send(m, release(y)) // returns the banner's expiry tick

	if n := len(r.recorded()); n != 0 {
		t.Fatalf("an ended row must never be attached; runner called %d times", n)
	}
	if !strings.Contains(view(m), "session has ended") {
		t.Fatalf("click on the selected ended row must show Enter's banner:\n%s", view(m))
	}
}

func TestMouse_ReleasedOnAnotherRowCancels(t *testing.T) {
	m, r := mouseBoard(t)
	y := rowY(t, m, "needs-row-summary") // the selected row
	other := rowY(t, m, "review-row-summary")
	m = quiet(t, m, press(y))
	m = quiet(t, m, release(other))

	if n := len(r.recorded()); n != 0 {
		t.Fatalf("a click dragged off the selected row must cancel; runner called %d times", n)
	}
	if got := selectedOf(m); got != "endpoint/n1" {
		t.Fatalf("a cancelled open moved the selection to %q", got)
	}
}

// A release with no press on the selected row before it opens nothing (a press that
// selected another row, or a release whose press was lost).
func TestMouse_StrayReleaseOpensNothing(t *testing.T) {
	m, r := mouseBoard(t)
	y := rowY(t, m, "needs-row-summary") // the selected row
	m = quiet(t, m, release(y))
	m = quiet(t, m, press(rowY(t, m, "working-row-summary")))
	_ = quiet(t, m, release(rowY(t, m, "working-row-summary")))

	if n := len(r.recorded()); n != 0 {
		t.Fatalf("a release without its press on the selected row attached; runner called %d times", n)
	}
}

func TestMouse_OtherButtonsAreIgnored(t *testing.T) {
	m, _ := mouseBoard(t)
	y := rowY(t, m, "review-row-summary")
	for _, b := range []tea.MouseButton{tea.MouseRight, tea.MouseMiddle} {
		for i := 0; i < 2; i++ {
			m = quiet(t, m, tea.MouseClickMsg{X: 6, Y: y, Button: b})
			m = quiet(t, m, tea.MouseReleaseMsg{X: 6, Y: y, Button: b})
		}
	}
	if got := selectedOf(m); got != "endpoint/n1" {
		t.Fatalf("right/middle clicks moved the selection to %q", got)
	}
}

func TestMouse_WheelMovesTheSelectionWithoutWrapping(t *testing.T) {
	m, _ := mouseBoard(t)
	wheel := func(b tea.MouseButton) { m = quiet(t, m, tea.MouseWheelMsg{X: 6, Y: 10, Button: b}) }

	wheel(tea.MouseWheelUp)
	if got := selectedOf(m); got != "endpoint/n1" {
		t.Fatalf("wheel up on the first row must stay there (no wrap), got %q", got)
	}
	wheel(tea.MouseWheelDown)
	if got := selectedOf(m); got != "endpoint/w1" {
		t.Fatalf("wheel down moved to %q, want endpoint/w1", got)
	}
	for i := 0; i < 5; i++ {
		wheel(tea.MouseWheelDown)
	}
	if got := selectedOf(m); got != "endpoint/c1" {
		t.Fatalf("wheel down past the last row must stay on it (no wrap), got %q", got)
	}
	wheel(tea.MouseWheelUp)
	if got := selectedOf(m); got != "endpoint/r1" {
		t.Fatalf("wheel up moved to %q, want endpoint/r1", got)
	}
}

// The banner pushes every row down two lines; the hit-test must follow the frame.
func TestMouse_HitTestFollowsTheBanner(t *testing.T) {
	m, _ := mouseBoard(t)
	m = click(t, m, rowY(t, m, "ended-row-summary"))
	m = send(m, keyEnter) // Enter on the ended row raises the "session has ended" banner
	if !strings.Contains(view(m), "session has ended") {
		t.Fatalf("precondition: banner should be showing:\n%s", view(m))
	}
	m = click(t, m, rowY(t, m, "working-row-summary"))
	if got := selectedOf(m); got != "endpoint/w1" {
		t.Fatalf("with the banner showing, a click on the working row selected %q", got)
	}
}

// Sectioned by tag the board has different headers; the hit-test follows them too.
func TestMouse_HitTestFollowsTagSections(t *testing.T) {
	a := sWorking("endpoint/a", "codex", "~/Code/a", "alpha-row", time.Minute)
	a.Tag = "zeta"
	b := sReview("endpoint/b", "claude", "~/Code/b", "beta-row", 2*time.Minute)
	b.Tag = "alpha"
	m, _ := mouseBoard(t, a, b)
	rm := m.(rootModel)
	rm.general.setLayout(groupByTag, orderByArrival)
	m = rm
	if selectedOf(m) != "endpoint/a" {
		t.Fatalf("precondition: the regroup keeps endpoint/a selected, got %q", selectedOf(m))
	}

	// "alpha" sorts first, so b's section now sits above a's: each click must move
	// the selection off the row it is on.
	m = click(t, m, rowY(t, m, "beta-row"))
	if got := selectedOf(m); got != "endpoint/b" {
		t.Fatalf("tag-sectioned board: click on beta-row selected %q", got)
	}
	m = click(t, m, rowY(t, m, "alpha-row"))
	if got := selectedOf(m); got != "endpoint/a" {
		t.Fatalf("tag-sectioned board: click on alpha-row selected %q", got)
	}
}

// A row the board clipped off the bottom is not on screen; the status bar drawn on
// that row must not select it.
func TestMouse_ClippedRowsAreNotClickable(t *testing.T) {
	var sessions []protocol.SessionView
	for i := 0; i < 12; i++ {
		id := "endpoint/w" + itoa(i)
		sessions = append(sessions, sWorking(id, "codex", "~/Code/x", "row-"+itoa(i), time.Duration(i+1)*time.Minute))
	}
	m, _ := mouseBoard(t, sessions...)
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: 8})
	lines := strings.Split(view(m), "\n")
	if len(lines) != 8 {
		t.Fatalf("precondition: an 8-row board, got %d rows", len(lines))
	}
	before := selectedOf(m)
	m = click(t, m, 7)
	if got := selectedOf(m); got != before {
		t.Fatalf("a click on the status bar selected a clipped row (%q -> %q)", before, got)
	}
}

// Mouse events that arrive while the board is not in plain navigation (a report
// already in flight when a confirm opened) are dropped, never acted on.
func TestMouse_EventsDuringAConfirmAreDropped(t *testing.T) {
	m, r := mouseBoard(t)
	y := rowY(t, m, "working-row-summary")
	m = send(m, keyCtrlX) // kill/delete confirm owns input: y/n only
	m = click(t, m, y)
	m = click(t, m, y)
	m = quiet(t, m, tea.MouseWheelMsg{X: 6, Y: y, Button: tea.MouseWheelDown})

	if n := len(r.recorded()); n != 0 {
		t.Fatalf("a double-click during a confirm must not attach; runner called %d times", n)
	}
	if !m.(rootModel).general.confirm {
		t.Fatal("mouse input must not resolve or cancel a pending confirm")
	}
	if got := selectedOf(m); got != "endpoint/n1" {
		t.Fatalf("mouse input during a confirm moved the selection to %q", got)
	}
}

// Same for a form opened over the board: its own state is untouched and nothing
// on the board beneath it moves.
func TestMouse_EventsOnAFormAreDropped(t *testing.T) {
	m, r := mouseBoard(t)
	y := rowY(t, m, "working-row-summary")
	m = send(m, keyRune('n')) // the launch form
	before := view(m)
	m = click(t, m, y)
	m = click(t, m, y)
	m = quiet(t, m, tea.MouseWheelMsg{X: 6, Y: y, Button: tea.MouseWheelDown})

	if n := len(r.recorded()); n != 0 {
		t.Fatalf("clicks on a form must not attach; runner called %d times", n)
	}
	if view(m) != before {
		t.Fatal("mouse input changed the launch form")
	}
	if got := selectedOf(m); got != "endpoint/n1" {
		t.Fatalf("mouse input on a form moved the board selection to %q", got)
	}
}

func TestMouse_ArmedClickRequiresLeftOrLegacyRelease(t *testing.T) {
	for _, button := range []tea.MouseButton{tea.MouseRight, tea.MouseMiddle, tea.MouseNone} {
		t.Run(tea.Mouse{Button: button}.String(), func(t *testing.T) {
			m, r := mouseBoard(t)
			y := rowY(t, m, "needs-row-summary")
			m = quiet(t, m, press(y))
			m, cmd := m.Update(tea.MouseReleaseMsg{X: 6, Y: y, Button: button})
			_ = settle(m, cmd)
			want := 0
			if button == tea.MouseNone { // Legacy reports do not identify the released button.
				want = 1
			}
			if got := len(r.recorded()); got != want {
				t.Fatalf("release button %v attached %d times, want %d", button, got, want)
			}
		})
	}
}

func TestMouse_InterruptedClickDoesNotOpenAfterReturningToBoard(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{keyRune('n'), keyCtrlX} {
		t.Run(key.String(), func(t *testing.T) {
			m, r := mouseBoard(t)
			y := rowY(t, m, "needs-row-summary")
			m = quiet(t, m, press(y))
			m = send(m, key)
			m = send(m, keyEsc)
			m = quiet(t, m, release(y))
			if len(r.recorded()) != 0 {
				t.Fatal("interrupted click opened a session")
			}
		})
	}
}

func TestMouse_SectionHeadersFitTheTerminal(t *testing.T) {
	for _, grouping := range []groupingMode{groupByStatus, groupByRepo, groupByTag} {
		m, _ := mouseBoard(t)
		rm := m.(rootModel)
		rm.width, rm.general.width = 24, 24
		for i := range rm.general.sessions {
			rm.general.sessions[i].Cwd = "/very/long/repository/path/that/would/wrap"
			rm.general.sessions[i].Tag = strings.Repeat("界", 30)
		}
		rm.general.setLayout(grouping, orderByArrival)
		for _, section := range rm.general.sectionOrder() {
			if got := lipgloss.Width("  " + rm.general.sectionHeader(section)); got > rm.width {
				t.Fatalf("section header is %d cells wide, terminal is %d", got, rm.width)
			}
		}
	}
}

func TestMouse_BannerLayoutChangesOnlyWhenExpiryIsHandled(t *testing.T) {
	m, _ := mouseBoard(t)
	rm := m.(rootModel)
	_ = rm.general.setBanner("notification")
	m = rm
	y := rowY(t, m, "review-row-summary")
	// The displayed banner must keep its rows until Update processes its expiry.
	rm.general.bannerExpiry = time.Now().Add(-time.Second)
	m = click(t, rm, y)
	if got := selectedOf(m); got != "endpoint/r1" {
		t.Fatalf("click on the displayed review row selected %q", got)
	}
	m = send(m, bannerExpireMsg{})
	if strings.Contains(view(m), "notification") {
		t.Fatal("handled expiry left the banner visible")
	}
	rm = m.(rootModel)
	_ = rm.general.setBanner("new notification")
	m = send(rm, bannerExpireMsg{}) // A prior banner's tick cannot clear a new one.
	if !strings.Contains(view(m), "new notification") {
		t.Fatal("stale expiry cleared a newer banner")
	}
}
