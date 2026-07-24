package engine

// F7 (E10.8): engine.Run is the fallback-poll driver — it calls Tick at
// Config.PollInterval until ctx is cancelled. Before this, PollInterval was stored
// with no caller, so the staleness guard never fired on its own. Run must actually
// flip a stale active session, must not busy-poll (it respects PollInterval), and
// must stop cleanly on cancellation with no leaked goroutine.

import (
	"context"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func TestRunDrivesStalenessTickUntilCancel(t *testing.T) {
	clk := newClock()
	staleness := 50 * time.Millisecond
	emits := make(chan status.Status, 8)
	e := New(Config{
		Now:                clk.now,
		CPUSampler:         constCPU(0), // idle CPU throughout
		StalenessThreshold: staleness,
		PollInterval:       5 * time.Millisecond, // short real cadence drives the loop
		Emit:               func(_ string, s status.Status) { emits <- s },
	})
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))

	// Mark active at the fake clock's T0, then jump the clock past the threshold so
	// the next Tick sees a stale active with no output and idle CPU.
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("hook: %v", err)
	}
	<-emits // drain the initial active emit
	clk.advance(2 * staleness)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()

	select {
	case s := <-emits:
		if s.Turn != status.TurnUnknown {
			t.Fatalf("Run drove a flip to turn=%s, want unknown", s.Turn)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("Run did not Tick a stale active session to unknown within the deadline")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel (leaked goroutine)")
	}
}

func TestRunRespectsPollIntervalNoBusyPoll(t *testing.T) {
	clk := newClock()
	cpu := &countingCPU{value: 25.0} // busy: no flips, so we measure only sampling cadence
	e := New(Config{
		Now:                clk.now,
		CPUSampler:         cpu.sample,
		StalenessThreshold: time.Minute,
		PollInterval:       20 * time.Millisecond,
		Emit:               func(string, status.Status) {},
	})
	// Seeded active via RegisterSession's initialStatus (not HandleCallback), so
	// its zero-value lastSignalAt is already far past staleness: Tick's sampling
	// gate (R2.3.2, agents-tracker-jmk) only samples turn=active-and-stale
	// sessions, so this fixture must seed exactly that state to remain a valid
	// Tick-cadence probe (coordinator ruling, perf-implementation-plan.md 2.3
	// "v2.1").
	e.RegisterSession("s1", "tok1", 1, kinds("hook"), status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done

	n := cpu.calls()
	if n == 0 {
		t.Fatalf("Run never Ticked; want periodic sampling at PollInterval")
	}
	if n > 40 {
		t.Fatalf("Run sampled %d times in ~120ms at a 20ms interval — a busy-poll; want a handful", n)
	}
}
