package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestMobileReviewRemovedRosterFocusUsesOldOrder(t *testing.T) {
	for _, header := range []bool{false, true} {
		var sessions, remaining []protocol.SessionView
		for i := range 10 {
			sessions = append(sessions, sNeedsInput("a/"+itoa(i), "codex", "/repo", "input", time.Hour))
		}
		sessions = append(sessions, sWorking("b/only", "codex", "/repo", "work", time.Hour))
		for i := range 30 {
			remaining = append(remaining, sCompleted("c/"+itoa(i), "codex", "/repo", "done", time.Hour))
		}
		sessions = append(sessions, remaining...)
		m := send(New(newFakeClient(sessions...), nil), tea.WindowSizeMsg{Width: 40, Height: 8}).(rootModel)
		if header {
			m.general.fold("working", true)
		} else {
			m.general.restoreSel("b/only")
		}
		m.general.revealFocus()
		want, passed := "", false
		m.general.navigation(func(l boardLine, _ int) {
			if passed && want == "" && l.kind == lineSession {
				want = l.session.ID
			}
			if l.kind == lineSection && l.section == "working" || l.kind == lineSession && l.session.ID == "b/only" {
				passed = true
			}
		})
		m.reconcileRoster(remaining)
		if got := m.general.selectedID(); got != want {
			t.Fatalf("header=%v: expected old-order neighbor %q, got %q", header, want, got)
		}
	}
}

func TestMobileReviewHiddenWorkingAnimationStops(t *testing.T) {
	for _, overlay := range []string{"pairing", "confirm"} {
		t.Run(overlay, func(t *testing.T) {
			m := mobileFrameFixture(t, 32, 8)
			if overlay == "pairing" {
				m = send(m, pairPendingMsg{DeviceName: "phone", SAS: []string{"one", "two"}})
			} else {
				m = send(m, keyCtrlX)
			}
			next, cmd := m.Update(workingAnimationMsg{})
			if cmd != nil || next.(rootModel).animatingWorking {
				t.Fatal("hidden working indicators kept their animation timer")
			}
		})
	}
}
