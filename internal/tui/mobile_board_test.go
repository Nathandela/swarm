package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Nathandela/swarm/internal/protocol"
	"strings"
	"testing"
	"time"
)

func mobileBoard(t *testing.T, width, height int) tea.Model {
	t.Helper()
	var sessions []protocol.SessionView
	for i := 0; i < 50; i++ {
		s := sWorking("endpoint/"+itoa(i), "codex", "/repo", "summary-"+itoa(i), time.Minute)
		s.Name = "session-" + itoa(i)
		sessions = append(sessions, s)
	}
	m, _ := mouseBoard(t, sessions...)
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}
func TestMobileBoardWheelKeepsFocusAndRevealFirst(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{keyEnter, keyRune('e'), keyRune('t'), keyRune('r'), keyRune('h'), keyCtrlX} {
		m := mobileBoard(t, 40, 8)
		id := selectedOf(m)
		for i := 0; i < 20; i++ {
			m = send(m, tea.MouseWheelMsg{X: 3, Y: 3, Button: tea.MouseWheelDown})
		}
		if selectedOf(m) != id {
			t.Fatal("wheel changed focus")
		}
		m, cmd := m.Update(key)
		r := m.(rootModel)
		if cmd != nil || r.screen != screenGeneral || r.general.editing || r.general.confirm {
			t.Fatalf("offscreen %v acted instead of revealing", key)
		}
		if !strings.Contains(view(m), "session-0") {
			t.Fatal("action did not reveal selection")
		}
	}
}
func TestMobileBoardCompactIdentityAndBounds(t *testing.T) {
	for _, size := range [][2]int{{24, 4}, {32, 8}, {36, 12}, {40, 18}, {48, 24}, {72, 12}, {90, 16}, {80, 24}, {120, 40}, {1, 1}, {1, 2}, {2, 3}} {
		m := mobileBoard(t, size[0], size[1])
		rows := strings.Split(view(m), "\n")
		if len(rows) > size[1] {
			t.Fatalf("%v height %d", size, len(rows))
		}
		for _, row := range rows {
			if lipgloss.Width(row) > size[0] {
				t.Fatalf("%v overflow: %q", size, row)
			}
		}
		if size[0] >= 24 && size[1] >= 4 && !strings.Contains(view(m), "session-0") {
			t.Fatalf("%v lost identity:\n%s", size, view(m))
		}
	}
}
func TestMobileBoardFoldIsSemanticAndAttentionReveals(t *testing.T) {
	m := mobileBoard(t, 40, 8)
	m = send(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if selectedOf(m) != "" {
		t.Fatal("collapsed header still targets session")
	}
	r := m.(rootModel)
	s := r.general.sessions[0]
	s.Summary = "changed"
	r.general.apply(s)
	if r.general.selectedID() != "" {
		t.Fatal("status update lost header focus")
	}
	m = send(r, tea.KeyPressMsg{Code: tea.KeyRight})
	if selectedOf(m) == "" {
		t.Fatal("expand did not focus first member")
	}
	r = m.(rootModel)
	s = r.general.sessions[49]
	s.Group = "needs_input"
	r.general.apply(s)
	m = send(r, keyRune('g'))
	if selectedOf(m) != s.ID {
		t.Fatal("attention did not reveal target")
	}
}
func TestMobileBoardResizeAndDragCancelClick(t *testing.T) {
	for _, interrupt := range []tea.Msg{tea.WindowSizeMsg{Width: 40, Height: 9}, tea.MouseMotionMsg{X: 7, Y: 3, Button: tea.MouseLeft}} {
		m := mobileBoard(t, 40, 8)
		y := rowY(t, m, "session-0")
		m = send(m, press(y))
		m = send(m, interrupt)
		m, cmd := m.Update(release(y))
		if cmd != nil || m.(rootModel).screen != screenGeneral {
			t.Fatalf("%T did not cancel", interrupt)
		}
	}
}

func TestMobileBoardMoreUsesCapturedTargetAndInvalidatesKill(t *testing.T) {
	m := mobileBoard(t, 24, 4)
	m = send(m, keyRune('m'))
	r := m.(rootModel)
	if r.general.more == nil || r.general.more.target != "endpoint/0" {
		t.Fatal("More did not capture visible selection")
	}
	r.general.restoreSel("endpoint/1")
	next, _ := r.activateMore("e")
	r = next.(rootModel)
	if !r.general.editing || r.general.editID != "endpoint/0" {
		t.Fatal("More retargeted to live focus")
	}
	r.general.closeEdit()
	r.general.restoreSel("endpoint/0")
	r.general.revealFocus()
	r.openMore()
	s, _ := r.general.sessionByID("endpoint/0")
	s.Status.Process = "exited"
	r.general.apply(s)
	for _, line := range r.moreLines() {
		if line.key == "x" {
			t.Fatal("Kill became Delete inside an existing menu")
		}
	}
	r.general.remove("endpoint/0")
	next, cmd := r.activateMore("e")
	r = next.(rootModel)
	if cmd != nil || r.general.editing || r.general.confirm {
		t.Fatal("vanished menu target dispatched")
	}
}
func TestMobileBoardFocusedHeaderSurvivesRefreshPaths(t *testing.T) {
	for _, path := range []string{"apply", "remove", "tag", "layout", "reconcile"} {
		t.Run(path, func(t *testing.T) {
			m := mobileBoard(t, 40, 8)
			r := send(m, tea.KeyPressMsg{Code: tea.KeyLeft}).(rootModel)
			section := r.general.focusSection
			switch path {
			case "apply":
				s := r.general.sessions[0]
				s.Summary = "new"
				r.general.apply(s)
			case "remove":
				r.general.remove("endpoint/49")
			case "tag":
				r.general.applyTag("endpoint/49", "tag")
			case "layout":
				r.general.setLayout(r.general.grouping, orderByName)
			case "reconcile":
				r.reconcileRoster(append([]protocol.SessionView(nil), r.general.sessions...))
			}
			if r.general.focusSection != section || r.general.selectedID() != "" {
				t.Fatal("refresh lost semantic header focus")
			}
		})
	}
}
func TestMobileBoardAttentionBannerVersionTogether(t *testing.T) {
	m := mobileBoard(t, 24, 4).(rootModel)
	m.clientVersion = "v99.0.0"
	m.daemonVersion = "v1.0.0"
	m.general.setBanner("error: unavailable")
	m.syncBoardGeometry(0)
	if !strings.Contains(view(m), "error") {
		t.Fatal("error banner vanished")
	}
	actionable := false
	start, rows, _, _ := m.boardGeometry()
	for y := start; y < start+rows; y++ {
		l, ok := m.boardLineAt(y)
		if ok && (l.kind == lineSession || l.kind == lineSection && m.general.isCollapsed(l.section)) {
			actionable = true
		}
	}
	if !actionable {
		t.Fatal("banner left no navigable line")
	}
}

func TestMobileBoardWheelOrderChromeAndPaddedRows(t *testing.T) {
	m := mobileBoard(t, 40, 8)
	// Clamping each input is observable: up at zero then down ends at one.
	m = send(m, tea.MouseWheelMsg{X: 39, Y: 3, Button: tea.MouseWheelUp})
	m = send(m, tea.MouseWheelMsg{X: 39, Y: 3, Button: tea.MouseWheelDown})
	if m.(rootModel).general.top != 1 {
		t.Fatal("wheel events lost their order at boundary")
	}
	for _, y := range []int{-1, 0, 7, 9} {
		before := m.(rootModel).general.top
		m = send(m, tea.MouseWheelMsg{X: 3, Y: y, Button: tea.MouseWheelDown})
		if m.(rootModel).general.top != before {
			t.Fatal("chrome wheel scrolled content")
		}
	}
	r := m.(rootModel)
	l, ok := r.boardLineAt(4)
	if !ok || l.kind != lineSession {
		t.Fatal("missing session fixture")
	}
	m = send(m, tea.MouseClickMsg{X: 39, Y: 4, Button: tea.MouseLeft})
	m = send(m, tea.MouseReleaseMsg{X: 39, Y: 4, Button: tea.MouseLeft})
	if selectedOf(m) != l.session.ID {
		t.Fatal("padded row cells did not select whole row")
	}
}
func TestMobileBoardCollapseModesAndNearbyFocus(t *testing.T) {
	a := sWorking("a", "codex", "/working", "a", time.Minute)
	a.Tag = "working"
	b := sWorking("b", "codex", "/other", "b", time.Minute)
	g := newGeneralModel([]protocol.SessionView{a, b})
	g.width = 40
	g.contentRows = 5
	g.fold("working", true)
	g.setLayout(groupByRepo, orderByArrival)
	if g.isCollapsed("working") {
		t.Fatal("status fold leaked into repo mode")
	}
	g.fold("working", true)
	g.applyTag("a", "new")
	if g.focusSection != "working" {
		t.Fatal("tag update lost repo header")
	}
	g.remove("a")
	if g.selectedID() != "b" {
		t.Fatal("vanished focused group did not choose surviving neighbor")
	}
	g.setLayout(groupByStatus, orderByArrival)
	if !g.isCollapsed("working") {
		t.Fatal("mode-local fold was lost")
	}
}
func TestMobileBoardMenuReleaseAndNativeOwnership(t *testing.T) {
	m := mobileBoard(t, 24, 4)
	r := m.(rootModel)
	if !strings.Contains(bottomRow(t, m), "[New]") {
		t.Fatal("missing action strip")
	}
	// More activates only after its own completed release.
	m = send(m, tea.MouseClickMsg{X: 15, Y: 3, Button: tea.MouseLeft})
	if m.(rootModel).general.more != nil {
		t.Fatal("More opened on press")
	}
	m = send(m, tea.MouseReleaseMsg{X: 15, Y: 3, Button: tea.MouseLeft})
	r = m.(rootModel)
	if r.general.more == nil || m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("More did not take navigation capture")
	}
	y := -1
	for i, l := range r.moreLines() {
		if l.key == "e" {
			y = 1 + i - r.general.more.top
		}
	}
	if y < 1 || y > 2 {
		t.Fatal("Rename not visible in minimum menu")
	}
	m = send(m, tea.MouseClickMsg{X: 23, Y: y, Button: tea.MouseLeft})
	if m.(rootModel).general.editing {
		t.Fatal("entry acted on press")
	}
	m = send(m, tea.MouseReleaseMsg{X: 23, Y: y, Button: tea.MouseLeft})
	r = m.(rootModel)
	if !r.general.editing || r.general.editID != "endpoint/0" || m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("menu rename failed captured identity/native editing ownership")
	}
}
func BenchmarkMobileBoardRender(b *testing.B) {
	for _, count := range []int{50, 500} {
		for _, grouping := range []groupingMode{groupByStatus, groupByRepo, groupByTag} {
			b.Run(itoa(count)+"/"+groupingLabels[grouping], func(b *testing.B) {
				sessions := make([]protocol.SessionView, count)
				for i := range sessions {
					s := sWorking(itoa(i), "codex", "/repo/"+itoa(i%20), "working", time.Hour)
					s.Name = "session " + itoa(i)
					s.Tag = itoa(i % 20)
					sessions[i] = s
				}
				m := New(newFakeClient(sessions...), detectMixed()).(rootModel)
				m.width = 40
				m.height = 18
				m.general.width = 40
				m.general.setLayout(grouping, orderByArrival)
				m.syncBoardGeometry(0)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = m.View()
				}
			})
		}
	}
}

func TestMobileBoardOversizedErrorIsNonactivating(t *testing.T) {
	m := mobileBoard(t, 24, 4).(rootModel)
	m.general.setBanner("error: the launch was refused; inspect provider credentials and retry only after correcting the named account")
	for _, key := range []tea.KeyPressMsg{keyEnter, keyRune('e'), keyRune('n'), keyCtrlX} {
		next, cmd := m.Update(key)
		r := next.(rootModel)
		if cmd != nil || r.screen != screenGeneral || r.general.confirm || r.general.editing {
			t.Fatal("oversized mandatory banner allowed action")
		}
	}
	if !strings.Contains(view(m), "Resize to read") {
		t.Fatal("clipped mandatory error lacked nonactivating notice")
	}
}

func TestMobileBoardRequiredBannerTailReturnsOnResize(t *testing.T) {
	m := mobileBoard(t, 24, 4).(rootModel)
	m.general.setBanner("error: provider refused this launch; correct the credential source before retrying; required final remedy is SIGN IN")
	if m.boardUsable() {
		t.Fatal("long mandatory context was clipped into an active board")
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(rootModel)
	if !m.boardUsable() || !strings.Contains(view(m), "SIGN IN") {
		t.Fatal("resize did not restore full mandatory error tail")
	}
	m.width = 24
	m.height = 4
	m.general.width = 24
	m.general.bannerMandatory = false
	if !m.boardUsable() {
		t.Fatal("ordinary attention banner unnecessarily disabled board")
	}
}
