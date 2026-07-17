package engine

// E10.7 (V-2/L1, engine half): a status change calls Emit synchronously on the
// triggering event, with no deferral to a later Tick. The full signal->TUI
// pipeline latency (<=1 s) is composed and asserted at Epic 14; here we pin that
// the engine adds no delay of its own — Emit has already fired by the time the
// triggering method returns.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func TestEmitIsSynchronousOnHookChange(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))

	before := rec.count()
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if rec.count() != before+1 {
		t.Fatalf("HandleCallback change emitted %d synchronously, want 1 (no deferral)", rec.count()-before)
	}
	if got, _ := rec.last(); got.id != "s1" || got.s.Turn != status.TurnActive {
		t.Fatalf("emit=(%q,%s), want (s1,active)", got.id, got.s.Turn)
	}
}

func TestEmitIsSynchronousOnHeuristicChange(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("heuristic"))

	before := rec.count()
	e.OnOutput("s1", spinnerSnap())
	if rec.count() != before+1 {
		t.Fatalf("OnOutput change emitted %d synchronously, want 1", rec.count()-before)
	}
}

func TestEmitIsSynchronousOnStalenessTick(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	staleness := 10 * time.Second
	e := newEngine(clk, constCPU(0), rec, staleness, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("hook: %v", err)
	}

	before := rec.count()
	clk.advance(2 * staleness)
	e.Tick()
	if rec.count() != before+1 {
		t.Fatalf("Tick staleness change emitted %d synchronously, want 1", rec.count()-before)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnUnknown {
		t.Fatalf("Tick emit turn=%s, want unknown", got.s.Turn)
	}
}

// A status-dimension change is what triggers an emit (L1 is per dimension
// change): an authenticated callback that leaves every dimension unchanged
// produces no emit.
func TestNoEmitWhenStatusUnchanged(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("hook seq=1: %v", err)
	}
	n := rec.count()

	// Same status, newer sequence: authenticated and accepted, but no dimension
	// changed -> no emit.
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 2, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("hook seq=2: %v", err)
	}
	if rec.count() != n {
		t.Fatalf("unchanged status emitted %d change(s), want 0", rec.count()-n)
	}
}
