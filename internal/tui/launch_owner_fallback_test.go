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

func (c noticeClient) TakeLaunchNotice(string) string { return c.notice }

const fallbackNotice = `launched on the owner login: account "Work" refused: accounts: private account or configuration is unavailable`

// The launch producer carries the daemon's owner-fallback notice into the result.
func TestLaunchCmd_CarriesTheOwnerFallbackNotice(t *testing.T) {
	c := noticeClient{fakeClient: newFakeClient(), notice: fallbackNotice}
	msg, ok := launchCmd(c, protocol.LaunchReq{Agent: "codex", Name: "fix-login"})().(launchResultMsg)
	if !ok || msg.notice != fallbackNotice || msg.err != nil {
		t.Fatalf("launch result = %+v, want the owner-fallback notice", msg)
	}
}

// An owner-fallback launch auto-attaches like any launch; its notice is shown on
// the board's banner when the owner returns from the session.
func TestBanner_OwnerFallbackLaunchIsShown(t *testing.T) {
	m := New(newFakeClient(), detectMixed())
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	m = send(m, launchResultMsg{id: "endpoint/new-1", agent: "codex", name: "fix-login", notice: fallbackNotice})
	if m.(rootModel).screen != screenAttach {
		t.Fatalf("screen = %v, want the new session attached as on any launch", m.(rootModel).screen)
	}
	assertNoticeOnReturn(t, m)
}

// A relocated resume that fell back to the owner login attaches like any resume,
// and the notice is shown when the owner returns to the board.
func TestBanner_OwnerFallbackResumeAutoAttachesAndBannersOnReturn(t *testing.T) {
	m := New(newFakeClient(), detectMixed())
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	source := protocol.SessionView{ID: "endpoint/old", Agent: "codex", Name: "fix-login", Cwd: t.TempDir()}
	rm := m.(rootModel).openResume(source, "")
	rm.resume.busy = true
	m = send(rm, resumeResultMsg{source: source, result: launchResultMsg{id: "endpoint/new-1", agent: "codex", name: "fix-login", notice: fallbackNotice}})
	if m.(rootModel).screen != screenAttach {
		t.Fatalf("screen = %v, want the resumed session attached as on any resume", m.(rootModel).screen)
	}
	assertNoticeOnReturn(t, m)
}

// assertNoticeOnReturn detaches back to the board and expects the notice banner.
func assertNoticeOnReturn(t *testing.T, m tea.Model) {
	t.Helper()
	m = send(m, attachDoneMsg{})
	if m.(rootModel).screen != screenGeneral {
		t.Fatalf("screen = %v after detaching, want the board", m.(rootModel).screen)
	}
	if shown := strings.Join(strings.Fields(view(m)), " "); !strings.Contains(shown, `launched on the owner login: account "Work" refused`) {
		t.Errorf("banner lacks the owner-fallback notice on return; view:\n%s", view(m))
	}
}
