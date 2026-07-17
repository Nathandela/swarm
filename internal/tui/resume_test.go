package tui

// FIX 5 TUI resume affordance (R-2): pressing 'r' on an ended/lost row issues a
// resume-as-new-session launch carrying the source id under the reserved
// resume_from option (agent + cwd carried over); 'r' on a running row is a no-op.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestResume_KeyIssuesResumeLaunchOnEndedRow(t *testing.T) {
	f := newFakeClient(sCompleted("endpoint/done1", "claude", "~/Code/x", "exit 0", time.Hour))
	m := newModel(t, f, detectMixed())

	_, cmd := m.Update(keyRune('r'))
	execCmd(cmd)

	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("pressing 'r' on an ended row issued %d launches; want exactly 1 resume launch", len(reqs))
	}
	req := reqs[0]
	if got := req.Options[protocol.OptionResumeFrom]; got != "endpoint/done1" {
		t.Errorf("resume launch resume_from = %q; want the source id %q", got, "endpoint/done1")
	}
	if req.Agent != "claude" {
		t.Errorf("resume launch agent = %q; want the source agent %q", req.Agent, "claude")
	}
	if req.Cwd != "~/Code/x" {
		t.Errorf("resume launch cwd = %q; want the source cwd", req.Cwd)
	}
	if req.Cols <= 0 || req.Rows <= 0 {
		t.Errorf("resume launch must carry valid cols/rows; got %dx%d", req.Cols, req.Rows)
	}
}

func TestResume_KeyIsNoOpOnRunningRow(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/run1", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())

	_, cmd := m.Update(keyRune('r'))
	execCmd(cmd)

	if reqs := f.launchReqs(); len(reqs) != 0 {
		t.Fatalf("pressing 'r' on a running row must not resume; got %d launches", len(reqs))
	}
}
