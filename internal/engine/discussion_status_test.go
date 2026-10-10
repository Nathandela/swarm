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
		if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(i + 1), Event: event, Payload: map[string]string{"agent_id": "child-1"}}); err != nil {
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
