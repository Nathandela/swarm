package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/charmbracelet/x/ansi"
)

func buttonPosition(t *testing.T, m tea.Model, label string) (int, int) {
	t.Helper()
	for y, line := range strings.Split(view(m), "\n") {
		if i := strings.Index(line, label); i >= 0 {
			return ansi.StringWidth(line[:i]), y
		}
	}
	t.Fatalf("missing button %q:\n%s", label, view(m))
	return 0, 0
}

func tapAction(t *testing.T, m tea.Model, label string) (tea.Model, tea.Cmd) {
	t.Helper()
	x, y := buttonPosition(t, m, label)
	m = quiet(t, m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
}

func TestActionButtonsWideCoverageAndNoDuplicateLegend(t *testing.T) {
	for _, ended := range []bool{false, true} {
		s := sWorking("target", "codex", "/repo", "working", time.Minute)
		if ended {
			s.Status.Process = status.ProcessExited
		}
		m, _ := mouseBoard(t, s)
		m = send(m, tea.WindowSizeMsg{Width: 160, Height: 24})
		row := bottomRow(t, m)
		for _, label := range []string{"[New (n)]", "[Open (⏎)]", "[More (m)]", "[Rename (e)]", "[Tag (t)]", "[Handoff (h)]", "[Options (o)]"} {
			if ended && label == "[Open (⏎)]" {
				continue
			}
			if !strings.Contains(row, label) {
				t.Errorf("missing %s in %q", label, row)
			}
		}
		if ended {
			for _, label := range []string{"[Resume (r)]", "[Delete (ctrl+x)]"} {
				if !strings.Contains(row, label) {
					t.Errorf("missing %s in %q", label, row)
				}
			}
		} else if !strings.Contains(row, "[Kill (ctrl+x)]") || strings.Contains(row, "Resume") {
			t.Errorf("wrong running actions: %q", row)
		}
		if strings.Contains(row, " · n new") || strings.Contains(row, "returns") {
			t.Errorf("duplicated legend: %q", row)
		}
	}
}

func TestActionButtonsNarrowMenuHasEveryActionAndPointerNavigation(t *testing.T) {
	m := mobileBoard(t, 24, 4)
	if row := bottomRow(t, m); row != "[Open (⏎)] [More (m)]" {
		t.Fatalf("minimum strip = %q", row)
	}
	m, _ = tapAction(t, m, "[More (m)]")
	seen := map[string]bool{}
	for i := 0; i < 30; i++ {
		for _, label := range []string{"[Open (⏎)]", "[Rename (e)]", "[Tag (t)]", "[Handoff (h)]", "[Kill (ctrl+x)]", "[New (n)]", "[Options (o)]", "[Attention (g)]", "[Quit (q)]"} {
			if strings.Contains(view(m), label) {
				seen[label] = true
			}
		}
		m, _ = tapAction(t, m, "[Down (↓)]")
	}
	if len(seen) != 9 {
		t.Fatalf("pointer scrolling reached %d/9 buttons: %v\n%s", len(seen), seen, view(m))
	}
	m, _ = tapAction(t, m, "[Up (↑)]")
	m, _ = tapAction(t, m, "[Back (esc)]")
	if m.(rootModel).general.more != nil {
		t.Fatal("Back did not return")
	}
}

func TestActionButtonsInvokeExistingAgentHandlers(t *testing.T) {
	for _, tc := range []struct {
		label string
		key   tea.KeyPressMsg
	}{
		{"[Rename (e)]", keyRune('e')}, {"[Tag (t)]", keyRune('t')},
		{"[Handoff (h)]", keyRune('h')}, {"[Kill (ctrl+x)]", keyCtrlX},
	} {
		t.Run(tc.label, func(t *testing.T) {
			m, _ := mouseBoard(t)
			m = send(m, tea.WindowSizeMsg{Width: 160, Height: 24})
			next, _ := tapAction(t, m, tc.label)
			got := next.(rootModel)
			want := send(m, tc.key).(rootModel)
			if got.screen != want.screen || got.general.editID != want.general.editID || got.general.editTag != want.general.editTag || got.general.confirmID != want.general.confirmID {
				t.Fatal("button differs from keyboard handler")
			}
		})
	}
}

func TestActionButtonsCancelAcrossSelectionStatusAndResize(t *testing.T) {
	for _, change := range []string{"selection", "status", "resize"} {
		t.Run(change, func(t *testing.T) {
			m, _ := mouseBoard(t)
			x, y := buttonPosition(t, m, "[Kill (ctrl+x)]")
			m = send(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			r := m.(rootModel)
			switch change {
			case "selection":
				r.general.move(1)
			case "status":
				s, _ := r.general.selected()
				s.Status.Process = status.ProcessExited
				r.general.apply(s)
			case "resize":
				r = send(r, tea.WindowSizeMsg{Width: r.width - 1, Height: r.height}).(rootModel)
			}
			next, cmd := r.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
			if cmd != nil || next.(rootModel).general.confirm {
				t.Fatal("stale button activated")
			}
		})
	}
}

func TestActionButtonsMenuShortcutUsesCapturedTarget(t *testing.T) {
	m := mobileBoard(t, 24, 4)
	m = send(m, keyRune('m'))
	r := m.(rootModel)
	r.general.restoreSel("endpoint/1")
	next := send(r, keyRune('e')).(rootModel)
	if !next.general.editing || next.general.editID != "endpoint/0" {
		t.Fatal("displayed shortcut did not act on captured agent")
	}
}
