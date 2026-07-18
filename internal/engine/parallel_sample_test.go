package engine

// R2.3 (agents-tracker-jmk): Tick used to sample each alive session's CPU
// SERIALLY, so wall-clock cost was N x cpuSampleWindow — at N=10 sessions this
// already overran the 1s PollInterval. Tick now (a) gates sampling to sessions
// whose result it will actually consume (an active turn already past the
// staleness threshold at collection time — engine.go's sole consumer of the
// sample) and (b) samples the gated sessions concurrently, joining every
// sampler goroutine before returning, so wall-clock cost is ~one
// cpuSampleWindow regardless of N.

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// T2.3.a: N concurrent 100ms samplers must not serialize Tick's wall-clock
// cost. Every session is turn=active and already past staleness, so all N are
// gated in and sampled; the sampler reports busy so no flip occurs, isolating
// the timing assertion from the commit path.
func TestTickSamplesConcurrentlyNotSerially(t *testing.T) {
	clk := newClock()
	staleness := 10 * time.Millisecond
	sleepy := func(pid int) (float64, error) {
		time.Sleep(100 * time.Millisecond)
		return 25.0, nil // busy: no flip
	}
	e := newEngine(clk, sleepy, &emitRecorder{}, staleness, time.Second)

	const n = 10
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("s%d", i)
		e.RegisterSession(id, "tok", i, kinds("hook"))
		if err := e.HandleCallback(Callback{SessionID: id, Token: "tok", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
			t.Fatalf("register %s active: %v", id, err)
		}
	}
	clk.advance(2 * staleness) // every session now past the staleness threshold

	start := time.Now()
	e.Tick()
	elapsed := time.Since(start)

	if elapsed >= 300*time.Millisecond {
		t.Fatalf("Tick took %s for %d concurrent 100ms samplers, want < 300ms (serial would be ~%dms)", elapsed, n, n*100)
	}
}

// T2.3.b: only sessions whose sample result Tick would actually consume are
// sampled. engine.go's sole consumer requires turn=active AND already past
// the staleness threshold; an active-but-fresh session's would-be sample is
// discarded by that same check today, so it must not be sampled either. An
// idle-turn session is never consumed by the staleness guard at all.
func TestTickGatesSamplingToActiveAndStale(t *testing.T) {
	clk := newClock()
	staleness := 10 * time.Second
	var mu sync.Mutex
	sampled := map[int]bool{}
	recorder := func(pid int) (float64, error) {
		mu.Lock()
		sampled[pid] = true
		mu.Unlock()
		return 0, nil
	}
	e := newEngine(clk, recorder, &emitRecorder{}, staleness, time.Second)

	e.RegisterSession("active-stale", "t1", 1, kinds("hook"))
	if err := e.HandleCallback(Callback{SessionID: "active-stale", Token: "t1", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("active-stale: %v", err)
	}
	e.RegisterSession("idle", "t3", 3, kinds("hook"))
	if err := e.HandleCallback(Callback{SessionID: "idle", Token: "t3", Sequence: 1, Event: "idle", Payload: turnSignal(status.TurnIdle)}); err != nil {
		t.Fatalf("idle: %v", err)
	}

	clk.advance(2 * staleness) // active-stale now past threshold

	// Registered/activated only AFTER the advance, so its lastSignalAt is
	// fresh relative to Tick's collection instant.
	e.RegisterSession("active-fresh", "t2", 2, kinds("hook"))
	if err := e.HandleCallback(Callback{SessionID: "active-fresh", Token: "t2", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("active-fresh: %v", err)
	}

	e.Tick()

	mu.Lock()
	defer mu.Unlock()
	if !sampled[1] {
		t.Fatalf("active-stale (pid 1) was not sampled; want it sampled (its result IS consumed)")
	}
	if sampled[2] {
		t.Fatalf("active-fresh (pid 2) was sampled; want it skipped (not yet stale, result discarded)")
	}
	if sampled[3] {
		t.Fatalf("idle (pid 3) was sampled; want it skipped (idle turn never consumes a sample)")
	}
}

// T2.3.c: every sampler goroutine Tick spawns is joined before Tick returns —
// no flip is still in flight after the call. Run with -race.
func TestTickJoinsAllSamplersBeforeReturning(t *testing.T) {
	clk := newClock()
	staleness := 10 * time.Millisecond
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, staleness, time.Second) // idle CPU: every gated session flips

	const n = 20
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("s%d", i)
		e.RegisterSession(id, "tok", i, kinds("hook"))
		if err := e.HandleCallback(Callback{SessionID: id, Token: "tok", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	clk.advance(2 * staleness)

	before := rec.count() // setup itself emitted n changes (the initial activations)
	e.Tick()

	if got := rec.count() - before; got != n {
		t.Fatalf("Tick returned with %d new emits recorded, want %d (a sampler goroutine is still in flight after return)", got, n)
	}
}

// T2.3.d: a panicking sampler is recovered. The panicking session is skipped
// for that tick (no flip either way); every other session is still sampled
// and flips normally; the engine keeps working on a later tick.
func TestTickRecoversPanickingSampler(t *testing.T) {
	clk := newClock()
	staleness := 10 * time.Millisecond
	rec := &emitRecorder{}
	panicky := func(pid int) (float64, error) {
		if pid == 1 {
			panic("boom: sampler exploded")
		}
		return 0, nil // idle -> flips
	}
	e := newEngine(clk, panicky, rec, staleness, time.Second)

	e.RegisterSession("panicky", "t1", 1, kinds("hook"))
	if err := e.HandleCallback(Callback{SessionID: "panicky", Token: "t1", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("panicky: %v", err)
	}
	e.RegisterSession("healthy", "t2", 2, kinds("hook"))
	if err := e.HandleCallback(Callback{SessionID: "healthy", Token: "t2", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("healthy: %v", err)
	}
	clk.advance(2 * staleness)

	before := rec.count()
	e.Tick() // must not crash despite the panicking sampler

	if got, ok := rec.last(); !ok || got.id != "healthy" || got.s.Turn != status.TurnUnknown {
		t.Fatalf("last emit = %+v, ok=%v; want healthy flipped to unknown", got, ok)
	}
	if rec.count() != before+1 {
		t.Fatalf("Tick emitted %d change(s), want exactly 1 (healthy only; panicky session skipped)", rec.count()-before)
	}

	// The engine keeps working on a later tick despite the earlier panic.
	clk.advance(2 * staleness)
	before2 := rec.count()
	e.Tick()
	if rec.count() != before2 {
		t.Fatalf("second tick emitted %d unexpected change(s)", rec.count()-before2)
	}
}
