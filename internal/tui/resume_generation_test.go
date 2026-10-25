package tui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestResumeGenerationCurrentSuccessAutoattaches(t *testing.T) {
	for _, relocate := range []bool{false, true} {
		for _, shrink := range []bool{false, true} {
			name := "resume"
			if relocate {
				name = "relocate"
			}
			if shrink {
				name += "/shrink-and-leave"
			}
			t.Run(name, func(t *testing.T) {
				source := sCompleted("source", "codex", t.TempDir(), "done", time.Hour)
				client := newFakeClient(source)
				m := New(client, nil).(rootModel)
				m.reconnectGeneration = 2
				m = send(m, tea.WindowSizeMsg{Width: 120, Height: 40}).(rootModel)
				key := keyRune('r')
				if relocate {
					m = m.openResume(source, "")
					key = keyEnter
				}
				next, cmd := m.Update(key)
				if cmd == nil {
					t.Fatal("resume not dispatched")
				}
				if shrink {
					next = send(next, tea.WindowSizeMsg{Width: 1, Height: 1})
					if relocate {
						next = send(next, keyEsc)
					}
				}
				result := cmd()
				next, _ = next.Update(result)
				got := next.(rootModel)
				if got.screen != screenAttach || got.attach.session.ID != client.launchID {
					t.Fatalf("current resume did not autoattach: screen=%v result=%#v", got.screen, result)
				}
				if got.rosterRevision != 1 {
					t.Fatalf("one resume completion advanced roster revision %d times", got.rosterRevision)
				}
				if len(client.launchReqs()) != 1 {
					t.Fatal("resize duplicated resume launch")
				}
			})
		}
	}
}

func TestResumeGenerationStaleResultCannotReplaceCurrentScreen(t *testing.T) {
	for _, failure := range []bool{false, true} {
		source := sCompleted("source", "codex", t.TempDir(), "done", time.Hour)
		client := newFakeClient(source)
		if failure {
			client.launchErr = errors.New("old connection failure")
		}
		m := send(New(client, nil), tea.WindowSizeMsg{Width: 40, Height: 8}).(rootModel)
		next, cmd := m.Update(keyRune('r'))
		if cmd == nil {
			t.Fatal("resume not dispatched")
		}
		result := cmd()
		m = next.(rootModel)
		m.reconnectGeneration = 2
		m.screen = screenOptions
		m.options.grouping = groupByRepo
		m.options.contextGuard.threshold.set("draft")
		next, followup := m.Update(result)
		got := next.(rootModel)
		if followup != nil || got.screen != screenOptions || got.options.grouping != groupByRepo || got.options.contextGuard.threshold.text != "draft" || got.rosterRevision != 0 {
			t.Fatalf("stale resume failure=%v replaced current state: screen=%v revision=%d", failure, got.screen, got.rosterRevision)
		}
	}
}

func TestResumeGenerationCurrentFailureStillOffersRelocation(t *testing.T) {
	source := sCompleted("source", "codex", t.TempDir(), "done", time.Hour)
	client := newFakeClient(source)
	client.launchErr = errors.New("current resume failure")
	m := send(New(client, nil), tea.WindowSizeMsg{Width: 40, Height: 8}).(rootModel)
	m.reconnectGeneration = 2
	next, cmd := m.Update(keyRune('r'))
	if cmd == nil {
		t.Fatal("resume not dispatched")
	}
	result := cmd().(resumeResultMsg)
	if result.result.generation != 2 {
		t.Fatalf("nested launch generation=%d; want current2", result.result.generation)
	}
	next, _ = next.Update(result)
	got := next.(rootModel)
	if got.screen != screenResume || got.resume.source.ID != source.ID || got.resume.reason == "" || got.rosterRevision != 1 {
		t.Fatalf("current failure lost recovery: screen=%v source=%q reason=%q revision=%d", got.screen, got.resume.source.ID, got.resume.reason, got.rosterRevision)
	}
}
