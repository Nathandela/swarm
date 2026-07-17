package engine

// E10.4 precedence (S7): a fresher typed signal beats a heuristic; a heuristic
// never overrides a typed signal that is still fresh.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// A fresher typed signal overrides a heuristic result — in both value
// directions (heuristic idle overridden by hook active, and vice versa).
func TestTypedSignalBeatsHeuristic(t *testing.T) {
	// Direction A: heuristic says idle, a fresher hook says active -> active wins.
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook", "heuristic"))

	e.OnOutput("s1", idlePromptSnap())
	if got, _ := rec.last(); got.s.Turn != status.TurnIdle {
		t.Fatalf("heuristic setup turn=%s, want idle", got.s.Turn)
	}
	clk.advance(time.Second)
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("fresher hook did not override heuristic: turn=%s, want active", got.s.Turn)
	}

	// Direction B (vice versa): heuristic says active, a fresher hook says idle -> idle wins.
	clk2 := newClock()
	rec2 := &emitRecorder{}
	e2 := newEngine(clk2, constCPU(0), rec2, time.Minute, time.Second)
	e2.RegisterSession("s1", "tok1", 1, kinds("hook", "heuristic"))

	e2.OnOutput("s1", spinnerSnap())
	if got, _ := rec2.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("heuristic setup turn=%s, want active", got.s.Turn)
	}
	clk2.advance(time.Second)
	if err := e2.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "idle", Payload: turnSignal(status.TurnIdle)}); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if got, _ := rec2.last(); got.s.Turn != status.TurnIdle {
		t.Fatalf("fresher hook did not override heuristic: turn=%s, want idle", got.s.Turn)
	}
}

// A heuristic never overrides a typed signal that is still fresh. While a typed
// signal is within the staleness threshold, a later OnOutput whose grid would
// imply a different turn must not change the status.
func TestHeuristicNeverOverridesFresherTypedSignal(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	staleness := time.Minute
	e := newEngine(clk, constCPU(0), rec, staleness, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook", "heuristic"))

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("hook: %v", err)
	}
	afterTyped := rec.count()

	// A later heuristic (still within the staleness window) that would read idle
	// must NOT override the fresher typed signal.
	clk.advance(staleness / 4)
	e.OnOutput("s1", idlePromptSnap())
	if rec.count() != afterTyped {
		t.Fatalf("heuristic overrode a fresher typed signal (%d extra emit), want 0 (S7)", rec.count()-afterTyped)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("after heuristic, turn=%s, want active preserved (S7)", got.s.Turn)
	}
}
