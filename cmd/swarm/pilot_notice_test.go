package main

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// noticeSpawnClient is a fake daemon whose launches carry an owner-fallback notice.
type noticeSpawnClient struct{ *fakeSpawnClient }

func (noticeSpawnClient) TakeLaunchNotice(string) string {
	return `launched on the owner login: account "Work" refused`
}

// pilot create and resume print the daemon's launch notice in their JSON output.
func TestPilotCreateAndResumePrintTheLaunchNotice(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	c := noticeSpawnClient{newFakeSpawnClient()}
	c.sessions = []protocol.SessionView{view("local/old", "codex", "AMI", status.GroupCompleted)}
	context := enterPilotForTest(t, c)
	for _, args := range [][]string{{"create", "--cli", "codex", "--prompt", "Investigate."}, {"resume", "local/old"}} {
		code, out, errs := pilotCall(c, append([]string{"--context", context}, args...)...)
		if code != 0 {
			t.Fatalf("%s exit=%d out=%q err=%q", args[0], code, out, errs)
		}
		if !strings.Contains(out, `"notice"`) || !strings.Contains(out, "owner login") {
			t.Errorf("%s output lacks the launch notice: %q", args[0], out)
		}
	}
}
