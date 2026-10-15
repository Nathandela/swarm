package engine

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

type kvjHook struct {
	seq          uint64
	event, actor string
}

// feedKVJ delivers hooks in the given order and returns each callback's error.
func feedKVJ(t *testing.T, e *Engine, hooks []kvjHook) []error {
	t.Helper()
	errs := make([]error, len(hooks))
	for i, h := range hooks {
		var payload map[string]string
		if h.actor != "" {
			payload = map[string]string{"agent_id": h.actor}
		}
		errs[i] = e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: h.seq, Event: h.event, Payload: payload})
	}
	return errs
}

// swarm-kvj: hook sequences carry no causal order, so a child's SubagentStop can
// take a LOWER sequence than that child's last tool hook. The stop is still
// terminal for the child; rejecting it leaked the child and pinned the session in
// Working until a daemon restart.
func TestEngine_OutOfOrderSubagentStopReleasesTheChild(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	errs := feedKVJ(t, e, []kvjHook{
		{2, "SubagentStart", "A"}, {4, "PostToolUse", "A"}, {3, "SubagentStop", "A"}, {5, "Stop", ""},
	})
	if got, _ := rec.last(); status.Derive(got.s) != status.GroupReadyForReview || got.s.Turn != status.TurnIdle {
		t.Errorf("out-of-order SubagentStop leaked the child: got %+v (group %s), want idle/ready_for_review", got.s, status.Derive(got.s))
	}
	if errs[2] != nil {
		t.Errorf("SubagentStop for an active child was rejected: %v", errs[2])
	}
}

// Guard: a replayed SubagentStop from an EARLIER incarnation of the same actor
// must not release the child a newer SubagentStart opened.
func TestEngine_ReplayedSubagentStopDoesNotReleaseANewerIncarnation(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	feedKVJ(t, e, []kvjHook{
		{1, "SubagentStart", "A"}, {2, "SubagentStop", "A"}, {3, "SubagentStart", "A"}, {4, "Stop", ""}, {2, "SubagentStop", "A"},
	})
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("replayed SubagentStop released the newer child: %+v", got.s)
	}
}
