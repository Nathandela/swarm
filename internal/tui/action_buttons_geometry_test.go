package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestActionButtonsCapturedOpenSurvivesFoldedFocus(t *testing.T) {
	s := sWorking("captured", "codex", "/repo", "working", time.Minute)
	m, runner := mouseBoard(t, s)
	m = send(m, keyRune('m'))
	r := m.(rootModel)
	r.general.fold(r.general.groupKey(s), true)
	if r.general.focusSection == "" {
		t.Fatal("test needs folded section focus")
	}
	next, cmd := tapAction(t, r, "[Open (⏎)]")
	if next.(rootModel).screen != screenAttach || cmd == nil {
		t.Fatal("captured Open did not attach")
	}
	settle(next, cmd)
	calls := runner.recorded()
	if len(calls) != 1 || calls[0].session.ID != s.ID {
		t.Fatalf("attach calls = %+v", calls)
	}
}

func TestActionButtonsPaintAndHitCellsAcrossWidths(t *testing.T) {
	pattern := regexp.MustCompile(`\[[^\]]+\]`)
	for _, state := range []string{"running", "ended", "empty", "folded"} {
		t.Run(state, func(t *testing.T) {
			s := sWorking("target", "codex", "/repo", "working", time.Minute)
			if state == "ended" {
				s.Status.Process = status.ProcessExited
			}
			var m tea.Model
			if state == "empty" {
				m = newModel(t, newFakeClient(), detectMixed())
			} else {
				m, _ = mouseBoard(t, s)
			}
			for width := 24; width <= 160; width++ {
				m = send(m, tea.WindowSizeMsg{Width: width, Height: 24})
				r := m.(rootModel)
				if state == "folded" {
					r.general.fold(r.general.groupKey(s), true)
				}
				row := bottomRow(t, r)
				want := make([]bool, width)
				for _, span := range pattern.FindAllStringIndex(row, -1) {
					label := row[span[0]:span[1]]
					start := ansi.StringWidth(row[:span[0]])
					end := start + ansi.StringWidth(label)
					if end > width {
						t.Fatalf("width=%d partial button %q", width, label)
					}
					for x := start; x < end; x++ {
						want[x] = true
						g := r.boardTarget(x, r.height-1)
						if g.kind != actionCommand || g.label != label {
							t.Fatalf("width=%d cell=%d label=%q target=%+v", width, x, label, g)
						}
					}
				}
				for x, active := range want {
					if !active && r.boardTarget(x, r.height-1).kind != actionNone {
						t.Fatalf("width=%d gutter=%d is active", width, x)
					}
				}
				if state == "empty" || state == "folded" {
					for _, b := range r.boardButtons() {
						if b.key == "e" || b.key == "t" || b.key == "h" || b.key == "x" || b.key == "r" {
							t.Fatalf("%s offers session action %q", state, b.text())
						}
					}
				}
				if width == 24 {
					for x, active := range want {
						if !active {
							next, cmd := r.Update(tea.MouseClickMsg{X: x, Y: r.height - 1, Button: tea.MouseLeft})
							if cmd != nil {
								t.Fatal("gutter press activated")
							}
							next, cmd = next.Update(tea.MouseReleaseMsg{X: x, Y: r.height - 1, Button: tea.MouseLeft})
							if cmd != nil || next.(rootModel).screen != screenGeneral || next.(rootModel).general.more != nil {
								t.Fatal("gutter release activated")
							}
							break
						}
					}
				}
			}
		})
	}
}

func TestActionButtonsEveryEligibleMenuActionReachable(t *testing.T) {
	for _, ended := range []bool{false, true} {
		s := sWorking("target", "codex", "/repo", "working", time.Minute)
		if ended {
			s.Status.Process = status.ProcessExited
		}
		m, _ := mouseBoard(t, s)
		m = send(m, tea.WindowSizeMsg{Width: 24, Height: 4})
		m, _ = tapAction(t, m, "[More (m)]")
		labels := []string{"[Rename (e)]", "[Tag (t)]", "[Handoff (h)]", "[New (n)]", "[Options (o)]", "[Attention (g)]", "[Quit (q)]"}
		if ended {
			labels = append(labels, "[Resume (r)]", "[Delete (ctrl+x)]")
		} else {
			labels = append(labels, "[Open (⏎)]", "[Kill (ctrl+x)]")
		}
		seen := map[string]bool{}
		for i := 0; i < 50; i++ {
			r := m.(rootModel)
			for _, label := range labels {
				if strings.Contains(view(m), label) {
					x, y := buttonPosition(t, m, label)
					for _, cell := range []int{x, x + ansi.StringWidth(label) - 1} {
						if g := r.boardTarget(cell, y); g.kind != actionMenu {
							t.Fatalf("ended=%v %q cell=%d target=%+v", ended, label, cell, g)
						}
					}
					seen[label] = true
				}
			}
			m, _ = tapAction(t, m, "[Down (↓)]")
		}
		for _, label := range labels {
			if !seen[label] {
				t.Errorf("ended=%v unreachable %s", ended, label)
			}
		}
	}
}
