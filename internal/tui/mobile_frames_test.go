package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"github.com/Nathandela/swarm/internal/protocol"
	teatest "github.com/Nathandela/swarm/internal/testutil/teatest"
)

func mobileFrameFixture(t *testing.T, width, height int) tea.Model {
	t.Helper()
	sessions := []protocol.SessionView{
		sNeedsInput("endpoint/api", "claude", "/project/api", "Review the database change", 10*time.Hour),
		sWorking("endpoint/tests", "codex", "/project/api", "Running tests", 11*time.Hour),
		sReview("endpoint/docs", "opencode", "/project/docs", "Review documentation", 12*time.Hour),
	}
	for i, name := range []string{"Login API", "Test runner", "Documentation"} {
		sessions[i].Name = name
	}
	m := New(newFakeClient(sessions...), func() []AgentInfo {
		return []AgentInfo{{Name: "claude", Installed: true, InRange: true}}
	})
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

// Keep compact/menu/decision frames reviewable as well as geometrically bounded.
func TestMobileFrames(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		prepare       func(tea.Model) tea.Model
		want          []string
	}{
		{"board_40x8", 40, 8, nil, []string{"Login API", "approval", "New", "More"}},
		{"board_24x4", 24, 4, nil, []string{"Login API", "approval", "Open", "More"}},
		{"fold_40x8", 40, 8, func(m tea.Model) tea.Model { return send(send(m, keyDown), tea.KeyPressMsg{Code: tea.KeyLeft}) }, []string{"Expand", "WORKING"}},
		{"more_24x4", 24, 4, func(m tea.Model) tea.Model { return send(m, keyRune('m')) }, []string{"More", "[Open (⏎)]", "[Back (esc)]", "[Down (↓)]"}},
		{"prompt_40x8", 40, 8, func(m tea.Model) tea.Model {
			m = send(m, detectMsg{agents: []AgentInfo{{Name: "claude", Installed: true, InRange: true}}})
			r := send(m, keyRune('n')).(rootModel)
			r.launch.cwd.set("/project/api")
			r.launch.focus = r.launch.promptIndex()
			r.launch.prompt.set("Keep this draft")
			return r
		}, []string{"Keep this draft", "New session", "enter launch", "esc cancel"}},
		{"confirm_32x8", 32, 8, func(m tea.Model) tea.Model { return send(m, keyCtrlX) }, []string{"Kill: Login API", "Agent: claude", "ID: endpoint/api", "esc cancel"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := mobileFrameFixture(t, tc.width, tc.height)
			if tc.prepare != nil {
				m = tc.prepare(m)
			}
			frame := view(m)
			for _, text := range tc.want {
				if !strings.Contains(frame, text) {
					t.Fatalf("missing meaningful context %q:\n%s", text, frame)
				}
			}
			teatest.RequireEqualOutput(t, []byte(frame))
		})
	}
}
