package engine

// F3 (L1/P-3): Emit is called OUTSIDE the global engine mutex, so a slow, blocking,
// or reentrant subscriber for one session cannot stall the engine for a DIFFERENT
// session. Single-writer ordering (G6) is preserved by a per-session emit lock,
// not by holding the global lock across Emit.

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/status"
)

func TestEmitOutsideLockDoesNotStallOtherSessions(t *testing.T) {
	clk := newClock()
	release := make(chan struct{})
	blockerEmitting := make(chan struct{})
	var otherEmits int32

	emit := func(id string, _ status.Status) {
		if id == "blocker" {
			close(blockerEmitting) // we have entered the wedged subscriber
			<-release              // ...and wedge here, holding no global engine lock
			return
		}
		atomic.AddInt32(&otherEmits, 1)
	}
	e := New(Config{
		Now:                clk.now,
		CPUSampler:         constCPU(0),
		StalenessThreshold: time.Minute,
		PollInterval:       time.Second,
		Emit:               emit,
	})
	e.RegisterSession("blocker", "tb", 1, []adapter.SignalSource{{Kind: "hook"}})
	e.RegisterSession("other", "to", 2, []adapter.SignalSource{{Kind: "hook"}})
	e.RegisterSession("other2", "t2", 3, []adapter.SignalSource{{Kind: "heuristic"}})

	// Wedge the blocker's Emit on its own goroutine.
	go func() {
		_ = e.HandleCallback(Callback{SessionID: "blocker", Token: "tb", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)})
	}()
	<-blockerEmitting // the blocker is now inside Emit, wedged

	// A DIFFERENT session's HandleCallback must complete promptly despite the wedge.
	doneCB := make(chan error, 1)
	go func() {
		doneCB <- e.HandleCallback(Callback{SessionID: "other", Token: "to", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)})
	}()
	select {
	case err := <-doneCB:
		if err != nil {
			t.Fatalf("other session HandleCallback: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("a wedged Emit stalled a DIFFERENT session's HandleCallback (L1/P-3)")
	}

	// ...and so must a DIFFERENT session's OnOutput.
	doneOut := make(chan struct{}, 1)
	go func() {
		e.OnOutput("other2", spinnerSnap())
		doneOut <- struct{}{}
	}()
	select {
	case <-doneOut:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("a wedged Emit stalled a DIFFERENT session's OnOutput (L1/P-3)")
	}

	if got := atomic.LoadInt32(&otherEmits); got != 2 {
		t.Fatalf("other sessions emitted %d change(s), want 2", got)
	}
	close(release) // let the wedged blocker finish
}
