package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
)

// noticeClient is a fake daemon whose launch reply carries a launch notice.
type noticeClient struct {
	*fakeClient
	notice string
}

func (c noticeClient) LaunchWithNotice(r protocol.LaunchReq) (string, string, string, error) {
	id, name, err := c.Launch(r)
	return id, name, c.notice, err
}

const fallbackNotice = `launched on the owner login: account "Work" refused: accounts: private account or configuration is unavailable`

// The launch producer carries the daemon's owner-fallback notice into the result.
func TestLaunchCmd_CarriesTheOwnerFallbackNotice(t *testing.T) {
	c := noticeClient{fakeClient: newFakeClient(), notice: fallbackNotice}
	msg, ok := launchCmd(c, protocol.LaunchReq{Agent: "codex", Name: "fix-login"})().(launchResultMsg)
	if !ok || msg.notice != fallbackNotice || msg.err != nil {
		t.Fatalf("launch result = %+v, want the owner-fallback notice", msg)
	}
}

// An owner-fallback launch is surfaced on the launch banner, where refusals are
// shown, and the board stays up so the owner reads it before attaching.
func TestBanner_OwnerFallbackLaunchIsShown(t *testing.T) {
	m := New(newFakeClient(), detectMixed())
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	m = send(m, launchResultMsg{id: "endpoint/new-1", agent: "codex", name: "fix-login", notice: fallbackNotice})
	shown := strings.Join(strings.Fields(view(m)), " ")
	if !strings.Contains(shown, `launched on the owner login: account "Work" refused`) {
		t.Errorf("banner lacks the owner-fallback notice; view:\n%s", view(m))
	}
	if m.(rootModel).screen != screenGeneral {
		t.Errorf("screen = %v, want the board so the notice is read", m.(rootModel).screen)
	}
}
