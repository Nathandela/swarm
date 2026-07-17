package engine

// E10.3 (S7 NEVER-CONFIDENTLY-WRONG): the staleness guard. A session left
// turn=active with no output AND idle CPU past the threshold is transitioned to
// turn=unknown by Tick — it is never left confidently active.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func TestStalenessGuardFlipsActiveToUnknown(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	staleness := 30 * time.Second
	e := newEngine(clk, constCPU(0), rec, staleness, time.Second) // CPU idle throughout
	e.RegisterSession("s1", "tok1", 1, kinds("hook", "heuristic"))

	// A typed signal marks the session active at t0.
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("seq=1: unexpected error %v", err)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("setup turn=%s, want active", got.s.Turn)
	}
	before := rec.count()

	// Within the threshold: Tick must NOT flip — staying active is legitimate here.
	clk.advance(staleness / 2)
	e.Tick()
	if got, ok := rec.last(); ok && got.s.Turn == status.TurnUnknown {
		t.Fatalf("Tick before threshold flipped to unknown prematurely")
	}

	// Past the threshold with no output and idle CPU: must flip to unknown.
	clk.advance(staleness)
	e.Tick()
	if rec.count() == before {
		t.Fatalf("stale active session emitted no change; want a flip to unknown")
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnUnknown {
		t.Fatalf("stale active turn=%s, want unknown (S7 never-confidently-active)", got.s.Turn)
	}
}

// E10.3 negative: the guard fires only on (no output AND no CPU). A busy process
// (nonzero CPU) is genuine activity, so Tick leaves an active turn active.
func TestStalenessGuardKeepsBusyActive(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	staleness := 30 * time.Second
	e := newEngine(clk, constCPU(25.0), rec, staleness, time.Second) // CPU busy
	e.RegisterSession("s1", "tok1", 1, kinds("hook", "heuristic"))

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("seq=1: unexpected error %v", err)
	}

	clk.advance(3 * staleness)
	e.Tick()
	if got, ok := rec.last(); ok && got.s.Turn == status.TurnUnknown {
		t.Fatalf("busy session flipped to unknown; CPU activity must defeat the staleness guard")
	}
}
