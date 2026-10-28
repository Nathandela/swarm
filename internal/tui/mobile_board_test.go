package tui

import (
	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
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
	if !strings.Contains(bottomRow(t, m), "[More (m)]") {
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
	m = send(m, keyDown) // Open precedes Rename in the menu.
	r = m.(rootModel)
	y := -1
	for i, l := range r.moreLines() {
		if l.key == "e" {
			y = 1 + i - r.general.more.top
		}
	}
	if y < 1 || y > r.moreBudget() {
		t.Fatal("Rename not visible in minimum menu")
	}
	m = send(m, tea.MouseClickMsg{X: 3, Y: y, Button: tea.MouseLeft})
	if m.(rootModel).general.editing {
		t.Fatal("entry acted on press")
	}
	m = send(m, tea.MouseReleaseMsg{X: 3, Y: y, Button: tea.MouseLeft})
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

func TestMobileBoardRosterKeepsHeaderThroughIntermediateEmptyGroup(t *testing.T) {
	old := sWorking("old", "codex", "/repo", "old", time.Minute)
	m := New(newFakeClient(old), detectMixed()).(rootModel)
	m.general.fold("working", true)
	old.Group = "needs_input"
	replacement := sWorking("replacement", "codex", "/repo", "replacement", time.Minute)
	m.reconcileRoster([]protocol.SessionView{old, replacement})
	if m.general.focusSection != "working" || m.general.selectedID() != "" {
		t.Fatal("authoritative roster lost surviving collapsed header during intermediate apply")
	}
}

func TestMobileBoardCompactWorkingUsesExistingAnimationAndPalette(t *testing.T) {
	m := mobileBoard(t, 24, 4).(rootModel)
	s, _ := m.general.selected()
	before := m.general.renderRow(s, s.Group, true)
	if !strings.Contains(before, groupStyle(s.Group).Render(groupIcon(s.Group, 0))) {
		t.Fatal("compact Working row dropped existing animated glyph/palette")
	}
	m.general.spinnerFrame++
	after := m.general.renderRow(s, s.Group, true)
	if before == after || !strings.Contains(after, groupIcon(s.Group, 1)) {
		t.Fatal("compact Working animation tick changed no visible cell")
	}
	if lipgloss.Width(before) > 24 || lipgloss.Width(after) > 24 || !strings.Contains(stripANSI(after), "session-0") {
		t.Fatal("compact glyph displaced useful identity or exceeded row bound")
	}
}

func TestMobileBoardKeyboardFoldRevealsHeaderWithoutMovingPointerAnchor(t *testing.T) {
	var sessions []protocol.SessionView
	for i := 0; i < 30; i++ {
		sessions = append(sessions, sWorking("work/"+itoa(i), "codex", "/repo", "work", time.Minute), sCompleted("done/"+itoa(i), "codex", "/repo", "done", time.Hour))
	}
	m := New(newFakeClient(sessions...), detectMixed())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 8})
	r := m.(rootModel)
	r.general.restoreSel("work/20")
	r.general.revealFocus()
	m = send(r, tea.KeyPressMsg{Code: tea.KeyLeft})
	r = m.(rootModel)
	if r.general.focusSection != "working" || !r.general.focusVisible() {
		t.Fatal("keyboard collapse left its focused header offscreen")
	}
	m = send(m, tea.KeyPressMsg{Code: tea.KeyRight})
	r = m.(rootModel)
	if r.general.selectedID() == "" || !r.general.focusVisible() {
		t.Fatal("keyboard expand left its first member offscreen")
	}
}
func TestMobileBoardMoreQuitRemainsLocalWhenDisconnected(t *testing.T) {
	m := mobileBoard(t, 24, 4)
	m = send(m, keyRune('m'))
	m = send(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	r := m.(rootModel)
	m, _ = r.Update(connectionLostMsg{from: r.events})
	_, cmd := m.Update(keyEnter)
	if cmd == nil {
		t.Fatal("disconnected menu blocked local Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("local menu Quit did not quit")
	}
}

func TestMobileBoardRemovedAnchorChoosesPreviousOrderNeighbor(t *testing.T) {
	r := mobileBoard(t, 40, 8).(rootModel)
	r.general.top = 21
	anchor := r.general.anchor()
	var ordered []protocol.SessionView
	r.general.content(func(l boardLine) {
		if l.kind == lineSession {
			ordered = append(ordered, l.session)
		}
	})
	index := -1
	for i, s := range ordered {
		if s.ID == anchor.id {
			index = i
		}
	}
	if index < 0 || index+1 >= len(ordered) {
		t.Fatal("bad anchor fixture")
	}
	want := ordered[index+1].ID
	r.reconcileRoster(append([]protocol.SessionView(nil), ordered[index+1:]...))
	if got := r.general.anchor().id; got != want {
		t.Fatalf("removed anchor %q jumped to %q instead of prior-order neighbor %q", anchor.id, got, want)
	}
	r = mobileBoard(t, 40, 8).(rootModel)
	r.general.top = 21
	anchor = r.general.anchor()
	r.general.remove(anchor.id)
	if got := r.general.anchor().id; got != want {
		t.Fatalf("single removed anchor jumped to %q instead of %q", got, want)
	}
}

func TestMobileBoardPointerFoldKeepsClickedHeaderRow(t *testing.T) {
	var sessions []protocol.SessionView
	for i := 0; i < 30; i++ {
		sessions = append(sessions, sNeedsInput("input/"+itoa(i), "codex", "/repo", "input", time.Hour), sWorking("work/"+itoa(i), "codex", "/repo", "work", time.Hour), sCompleted("done/"+itoa(i), "codex", "/repo", "done", time.Hour))
	}
	m := New(newFakeClient(sessions...), detectMixed())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 8})
	r := m.(rootModel)
	index := 0
	header := -1
	r.general.content(func(l boardLine) {
		if l.kind == lineSection && l.section == "working" {
			header = index
		}
		index++
	})
	r.general.top = header - 2
	m = r
	for i := 0; i < 2; i++ {
		y := rowY(t, m, "WORKING")
		m = send(m, tea.MouseClickMsg{X: 39, Y: y, Button: tea.MouseLeft})
		m = send(m, tea.MouseReleaseMsg{X: 39, Y: y, Button: tea.MouseLeft})
		if got := rowY(t, m, "WORKING"); got != y {
			t.Fatalf("pointer fold moved clicked header row %d to %d", y, got)
		}
	}
}

func TestMobileBoardEventRemovalHasSingleAnchorOwner(t *testing.T) {
	for _, path := range []string{"hidden", "superseded-by", "supersedes"} {
		t.Run(path, func(t *testing.T) {
			var sessions []protocol.SessionView
			for i := 0; i < 10; i++ {
				sessions = append(sessions, sNeedsInput("input/"+itoa(i), "codex", "/repo", "input", time.Hour))
			}
			old := sWorking("working/only", "codex", "/repo", "work", time.Hour)
			sessions = append(sessions, old)
			for i := 0; i < 10; i++ {
				sessions = append(sessions, sCompleted("done/"+itoa(i), "codex", "/repo", "done", time.Hour))
			}
			m := New(newFakeClient(sessions...), detectMixed())
			m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 8})
			r := m.(rootModel)
			index := 0
			r.general.content(func(l boardLine) {
				if l.session.ID == old.ID {
					r.general.top = index
				}
				index++
			})
			switch path {
			case "hidden":
				old.RosterHidden = true
			case "superseded-by":
				old.SupersededBy = "done/0"
			case "supersedes":
				replacement := sCompleted("replacement", "codex", "/repo", "replacement", time.Hour)
				replacement.Supersedes = []string{old.ID}
				old = replacement
			}
			r = send(r, eventMsg{ev: protocol.Event{Session: old}}).(rootModel)
			if got := r.general.anchor(); got.section != "completed" {
				t.Fatalf("%s event overwrote nearest surviving completed header: %+v", path, got)
			}
		})
	}
}

func TestMobileBoardVanishedHeaderKeepsOldOrderNeighbor(t *testing.T) {
	for _, path := range []string{"status", "tag"} {
		t.Run(path, func(t *testing.T) {
			var sessions []protocol.SessionView
			for i := range 10 {
				s := sNeedsInput("a/"+itoa(i), "codex", "/repo", "input", time.Hour)
				s.Tag = "a"
				sessions = append(sessions, s)
			}
			only := sWorking("b/only", "codex", "/repo", "work", time.Hour)
			only.Tag = "b"
			sessions = append(sessions, only)
			for i := range 10 {
				s := sCompleted("c/"+itoa(i), "codex", "/repo", "done", time.Hour)
				s.Tag = "c"
				sessions = append(sessions, s)
			}
			m := New(newFakeClient(sessions...), nil).(rootModel)
			m = send(m, tea.WindowSizeMsg{Width: 40, Height: 8}).(rootModel)
			section, following := "working", "completed"
			if path == "tag" {
				m.general.setLayout(groupByTag, orderByArrival)
				section, following = "b", "c"
			}
			m.general.fold(section, true)
			i := 0
			m.general.content(func(l boardLine) {
				if l.kind == lineSection && l.section == section {
					m.general.top = i
				}
				i++
			})
			if path == "status" {
				only.Group = "needs_input"
				m.general.apply(only)
			} else {
				m.general.applyTag(only.ID, "a")
			}
			anchor := m.general.anchor()
			if anchor.kind != lineSection || anchor.section != following {
				t.Fatalf("vanished header should reveal following %q header, got %+v", following, anchor)
			}
			if got := m.general.selectedID(); got != "c/0" {
				t.Fatalf("focus should choose next old-order session c/0, got %q", got)
			}
		})
	}
}

func TestMobileBoardTagUpdateDoesNotApplyLifecycleProjection(t *testing.T) {
	source := sWorking("source", "codex", "/repo", "source", time.Hour)
	other := sCompleted("other", "codex", "/repo", "other", time.Hour)
	m := newGeneralModel([]protocol.SessionView{source, other})
	for i := range m.sessions {
		if m.sessions[i].ID == source.ID {
			m.sessions[i].RosterHidden = true
			m.sessions[i].SupersededBy = other.ID
			m.sessions[i].Supersedes = []string{other.ID}
		}
	}
	m.applyTag(source.ID, "new-tag")
	updated, ok := m.sessionByID(source.ID)
	if !ok || updated.Tag != "new-tag" || len(m.sessions) != 2 {
		t.Fatal("tag update applied lifecycle projection instead of metadata")
	}
	if _, ok := m.sessionByID(other.ID); !ok {
		t.Fatal("tag update removed a lifecycle source")
	}
}

func TestMobileBoardWidthBoundariesAndUnicode(t *testing.T) {
	const identity = "界é🐙"
	sessions := []protocol.SessionView{
		sNeedsInput("endpoint/input", "claude", "/界/é/🐙/input", "question", time.Minute),
		sWorking("endpoint/working", "codex", "/界/é/🐙/working", "working", time.Minute),
		sReview("endpoint/review", "opencode", "/界/é/🐙/review", "review", time.Minute),
	}
	for i := range sessions {
		sessions[i].Name = identity + strings.Repeat("long name ", 12)
		sessions[i].Tag = identity + strings.Repeat("long tag ", 12)
	}
	for _, mode := range []groupingMode{groupByStatus, groupByRepo, groupByTag} {
		m := New(newFakeClient(sessions...), detectMixed()).(rootModel)
		m.general.setLayout(mode, m.general.ordering)
		for _, height := range []int{3, 4, 5, 7, 8, 9, 11, 12, 13} {
			for width := 1; width <= 200; width++ {
				next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
				m = next.(rootModel)
				m.general.revealFocus()
				if width >= 24 {
					for _, session := range sessions {
						row := m.general.renderRow(session, session.Group, false)
						if lipgloss.Width(row) > width {
							t.Fatalf("group=%v %dx%d: unclipped row overflow %q", mode, width, height, row)
						}
					}
				}
				frame := view(m)
				lines := strings.Split(frame, "\n")
				if len(lines) > height {
					t.Fatalf("group=%v %dx%d: height=%d", mode, width, height, len(lines))
				}
				for _, line := range lines {
					if lipgloss.Width(line) > width {
						t.Fatalf("group=%v %dx%d: row overflow %q", mode, width, height, line)
					}
				}
				if width >= 24 && height >= 4 && !strings.Contains(lineContaining(frame, "▌"), identity) {
					t.Fatalf("group=%v %dx%d: lost readable identity:\n%s", mode, width, height, frame)
				}
			}
		}
	}
}
