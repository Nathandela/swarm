package engine

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func TestClaudeChildrenHoldDiscussionAcrossGridFallback(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	for i, event := range []string{"UserPromptSubmit", "SubagentStart", "Stop"} {
		if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(i + 1), Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	clk.advance(time.Minute)
	e.OnOutput("s1", claudeIdleScreen())
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("grid cleared outstanding child: %+v", got.s)
	}
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 4, Event: "SubagentStop"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnIdle {
		t.Fatalf("last child left stopped parent working: %+v", got.s)
	}
}

func TestClaudeUnrelatedNotificationCannotFinishTurn(t *testing.T) {
	for _, subtype := range []string{"auth_success", "agent_completed", "future_notification"} {
		t.Run(subtype, func(t *testing.T) {
			clk, rec := newClock(), &emitRecorder{}
			e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
			e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
			if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "UserPromptSubmit"}); err != nil {
				t.Fatal(err)
			}
			if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 2, Event: "Notification", Payload: map[string]string{"notification_type": subtype}}); err != nil {
				t.Fatal(err)
			}
			if got, _ := rec.last(); got.s.Turn != status.TurnActive {
				t.Fatalf("%s finished turn: %+v", subtype, got.s)
			}
		})
	}
}

func TestClaudeLastChildCannotFinishNewParentTurn(t *testing.T) {
	for _, start := range []string{"UserPromptSubmit", "PreToolUse"} {
		t.Run(start, func(t *testing.T) {
			clk, rec := newClock(), &emitRecorder{}
			e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
			e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
			for i, event := range []string{"SubagentStart", "Stop", start, "SubagentStop"} {
				if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(i + 1), Event: event}); err != nil {
					t.Fatal(err)
				}
			}
			if got, _ := rec.last(); got.s.Turn != status.TurnActive {
				t.Fatalf("child finished new parent turn: %+v", got.s)
			}
		})
	}
}

func TestClaudeChildToolDoesNotReopenStoppedParent(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	for i, event := range []string{"SubagentStart", "Stop", "PreToolUse", "SubagentStop"} {
		var payload map[string]string
		if event != "Stop" {
			payload = map[string]string{"agent_id": "child-1"}
		}
		if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(i + 1), Event: event, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnIdle {
		t.Fatalf("child tool reopened parent: %+v", got.s)
	}
}

func TestClaudeChildAccountingRejectsReplayAndInvalidHooks(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	cb := Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "SubagentStart", Payload: turnSignal(status.TurnActive)}
	if err := e.HandleCallback(cb); err != nil {
		t.Fatal(err)
	}
	if err := e.HandleCallback(cb); err == nil {
		t.Fatal("replayed start accepted")
	}
	cb.Sequence = 2
	cb.Payload = map[string]string{PayloadKeyTurn: "invalid"}
	if err := e.HandleCallback(cb); err == nil {
		t.Fatal("invalid start accepted")
	}
	for i, event := range []string{"Stop", "SubagentStop"} {
		if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(i + 2), Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnIdle {
		t.Fatalf("rejected start leaked an outstanding child: %+v", got.s)
	}
}

func TestClaudeMultipleChildrenAndPermissionGrid(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	for i, event := range []string{"UserPromptSubmit", "SubagentStart", "SubagentStart", "Stop", "SubagentStop"} {
		if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(i + 1), Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	clk.advance(time.Minute)
	e.OnOutput("s1", claudeIdleScreen())
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("remaining child lost: %+v", got.s)
	}
	permission := snapFromLines(80, 0, 0, false, []string{"Do you want to proceed?", "❯ 1. Yes", "Esc to cancel · Tab to amend"})
	e.OnOutput("s1", permission)
	if got, _ := rec.last(); status.Derive(got.s) != status.GroupNeedsInput {
		t.Fatalf("children hid permission: %+v", got.s)
	}
}

func TestClaudeNamedChildrenDoNotConsumeSiblingStops(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	for i, h := range []struct{ event, actor string }{
		{"UserPromptSubmit", ""}, {"SubagentStart", "a"}, {"SubagentStart", "a"}, {"Stop", ""}, {"SubagentStop", "internal-agent"},
	} {
		if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(i + 1), Event: h.event, Payload: map[string]string{"agent_id": h.actor}}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("unrelated stop ended discussion: %+v", got.s)
	}
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 6, Event: "SubagentStop", Payload: map[string]string{"agent_id": "a"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnIdle {
		t.Fatalf("duplicate start inflated child count: %+v", got.s)
	}
}

func TestClaudeChildLifecycleOrderingIsIndependentOfParent(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	for _, h := range []struct {
		seq          uint64
		event, actor string
	}{
		{1, "UserPromptSubmit", ""}, {3, "Stop", ""}, {2, "SubagentStart", "a"}, {5, "PreToolUse", "a"}, {4, "PreToolUse", ""}, {6, "SubagentStop", "a"},
	} {
		if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: h.seq, Event: h.event, Payload: map[string]string{"agent_id": h.actor}}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("child callbacks suppressed a new parent turn: %+v", got.s)
	}
}

func TestClaudeSiblingActivityDoesNotClearAnotherActorsPermission(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	for i, h := range []struct{ event, actor string }{
		{"UserPromptSubmit", ""}, {"SubagentStart", "a"}, {"PermissionRequest", "a"}, {"PreToolUse", "b"}, {"SubagentStop", "b"}, {"PreToolUse", ""},
	} {
		if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(i + 1), Event: h.event, Payload: map[string]string{"agent_id": h.actor}}); err != nil {
			t.Fatal(err)
		}
		if i >= 2 {
			if got, _ := rec.last(); status.Derive(got.s) != status.GroupNeedsInput {
				t.Fatalf("%s/%s cleared waiting actor: %+v", h.event, h.actor, got.s)
			}
		}
	}
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 7, Event: "PostToolUse", Payload: map[string]string{"agent_id": "a"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := rec.last(); status.Derive(got.s) != status.GroupWorking {
		t.Fatalf("requester resume did not clear its wait: %+v", got.s)
	}
}
