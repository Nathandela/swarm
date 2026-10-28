package tui

import (
	"github.com/Nathandela/swarm/internal/status"
	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"strings"
	"testing"
	"time"
)

func TestClaudeChatActionsNeverOfferOrRouteResumeAndHandoff(t *testing.T) {
	for _, ended := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "ended"}[ended], func(t *testing.T) {
			row := sWorking("chat-target", "claude", "/repo", "chat", time.Minute)
			row.RequiredRuntime = "claude-chat"
			if ended {
				row.Status.Process = status.ProcessExited
			}
			m, _ := mouseBoard(t, row)
			client := m.(rootModel).client.(*fakeClient)
			m = send(m, tea.WindowSizeMsg{Width: 180, Height: 24})
			for _, button := range m.(rootModel).boardButtons() {
				if button.key == "r" || button.key == "h" {
					t.Fatal("chat footer offered unsupported action", button.label)
				}
			}
			state := m.(rootModel)
			state.openMore()
			for _, line := range state.moreLines() {
				if line.key == "r" || line.key == "h" {
					t.Fatal("chat More menu offered unsupported action", line.text)
				}
			}
			for _, key := range []rune{'r', 'h'} {
				next, cmd := m.Update(keyRune(key))
				execCmd(cmd)
				if next.(rootModel).screen == screenResume || next.(rootModel).screen == screenHandoff || len(client.launchReqs()) != 0 {
					t.Fatal("chat keyboard routed unsupported successor", key)
				}
			}
			if ended {
				next, cmd := m.Update(keyEnter)
				execCmd(cmd)
				if strings.Contains(view(next), "r resume") || next.(rootModel).screen == screenResume || len(client.launchReqs()) != 0 {
					t.Fatal("ended chat offered resume instead of explicit fresh launch")
				}
			}
		})
	}
}
