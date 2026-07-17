package engine

// F1 re-review (G5): the anti-replay dimension. A monotonic counter allocates
// distinct sequences, but concurrent `swarm hook` processes release the counter
// lock before posting, so callbacks can ARRIVE out of order. A strict global
// seq<=lastSeq check would drop the later-arriving lower sequence even though it
// carries a legitimate, not-yet-seen update. The engine instead keeps a
// per-dimension high-water sequence: a callback writes a dimension only if its
// sequence is newer than that dimension's high-water. This accepts reordered
// callbacks that touch DIFFERENT dimensions, rejects an exact replay, and never
// lets a stale sequence regress a dimension a newer sequence already set.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// Two concurrently-allocated callbacks touching DIFFERENT dimensions, delivered
// OUT OF ORDER (higher sequence first), are BOTH accepted — neither is wrongly
// rejected — and the final status carries both updates.
func TestOutOfOrderDisjointDimensionsBothAccepted(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))

	// seq=2 (interaction) arrives before seq=1 (turn): the reorder the counter
	// permits under concurrent hooks.
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 2, Event: "e", Payload: interactionSignal(status.InteractionPermission)}); err != nil {
		t.Fatalf("seq=2 interaction: %v", err)
	}
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "e", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("seq=1 turn arriving after seq=2 was WRONGLY REJECTED: %v", err)
	}

	got, ok := rec.last()
	if !ok {
		t.Fatalf("no status emitted")
	}
	if got.s.Turn != status.TurnActive || got.s.Interaction != status.InteractionPermission {
		t.Fatalf("final status = (turn=%s, interaction=%s), want (active, permission) — both out-of-order updates kept",
			got.s.Turn, got.s.Interaction)
	}
}

// An EXACT replay of an already-applied sequence is rejected: no re-apply, no emit.
func TestExactReplayRejected(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 2, Event: "e", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("seq=2: %v", err)
	}
	n := rec.count()
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 2, Event: "e", Payload: turnSignal(status.TurnActive)}); err == nil {
		t.Fatalf("exact replay of seq=2: got nil error, want rejection")
	}
	if rec.count() != n {
		t.Fatalf("exact replay emitted %d change(s), want 0", rec.count()-n)
	}
}

// A stale sequence for a dimension a NEWER sequence already set is rejected and
// does not regress that dimension (the no-regression invariant).
func TestStaleSameDimensionRejectedNoRegression(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 5, Event: "e", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("seq=5 turn active: %v", err)
	}
	n := rec.count()
	// A lower sequence for the SAME dimension is a stale reorder: reject, keep active.
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 3, Event: "e", Payload: turnSignal(status.TurnIdle)}); err == nil {
		t.Fatalf("stale seq=3 turn idle after seq=5 active: got nil error, want rejection")
	}
	if rec.count() != n {
		t.Fatalf("stale same-dimension callback emitted %d change(s), want 0 (no regression)", rec.count()-n)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("after stale callback, turn=%s, want active preserved", got.s.Turn)
	}
}

// A sequence far below a dimension's high-water is rejected (the ancient case).
func TestFarBelowHighWaterRejected(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1000, Event: "e", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("seq=1000: %v", err)
	}
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "e", Payload: turnSignal(status.TurnIdle)}); err == nil {
		t.Fatalf("ancient seq=1 after seq=1000: got nil error, want rejection")
	}
}
