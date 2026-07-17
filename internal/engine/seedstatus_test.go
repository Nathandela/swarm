package engine

// B3 (S7): a reconnected session seeded with its PERSISTED status must be subject
// to the staleness guard. Before this fix RegisterSession hardcoded turn=unknown,
// so after a restart a persisted turn=active was invisible to Tick (which only
// downgrades an internally-active session) and lingered as stale-active.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func TestSeedStatus_EnablesStalenessDowngradeAfterReconcile(t *testing.T) {
	now := time.Now()
	var emitted status.Status
	var gotEmit bool
	e := New(Config{
		Now:                func() time.Time { return now },
		CPUSampler:         func(int) (float64, error) { return 0, nil }, // idle
		StalenessThreshold: time.Second,
		Emit:               func(_ string, s status.Status) { emitted = s; gotEmit = true },
	})
	// Reconcile registers WITH the persisted status (turn=active) in one atomic op.
	e.RegisterSession("s1", "tok", 1, kinds("hook", "heuristic"),
		status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone})

	now = now.Add(2 * time.Second) // past staleness, no fresh signal
	e.Tick()

	if !gotEmit || emitted.Turn != status.TurnUnknown {
		t.Fatalf("stale reconciled active session was not downgraded by Tick (emitted=%+v, gotEmit=%v); "+
			"RegisterSession/SeedStatus must seed the persisted status so the staleness guard applies (S7)", emitted, gotEmit)
	}
}

// TestSeedStatus_FreshBaselineNotDowngradedSpuriously guards the other direction: a
// freshly-seeded turn=unknown session (the fresh-launch baseline) is not touched by
// Tick — only an active turn is a downgrade candidate.
func TestSeedStatus_FreshBaselineNotDowngradedSpuriously(t *testing.T) {
	now := time.Now()
	var gotEmit bool
	e := New(Config{
		Now:                func() time.Time { return now },
		CPUSampler:         func(int) (float64, error) { return 0, nil },
		StalenessThreshold: time.Second,
		Emit:               func(_ string, _ status.Status) { gotEmit = true },
	})
	e.RegisterSession("s1", "tok", 1, kinds("hook"),
		status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone})
	now = now.Add(2 * time.Second)
	e.Tick()
	if gotEmit {
		t.Errorf("Tick emitted for a turn=unknown baseline session; only an active turn is a staleness-downgrade candidate")
	}
}

// TestRegisterSession_FirstSignalNotClobbered (C2): folding the initial status into
// RegisterSession makes the old register->seed clobber structurally impossible. A
// hook that lands right after a fresh registration advances the status AND its
// high-water, with no separate seed step to overwrite it — so the real signal is
// retained and its replay is rejected.
func TestRegisterSession_FirstSignalNotClobbered(t *testing.T) {
	var last status.Status
	e := New(Config{Emit: func(_ string, s status.Status) { last = s }})
	e.RegisterSession("s1", "tok", 1, kinds("hook")) // fresh: one atomic install (baseline)

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok", Sequence: 5, Event: "e", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("first hook after registration: %v", err)
	}
	if last.Turn != status.TurnActive {
		t.Fatalf("first hook after registration was not retained: %+v", last)
	}
	// A replay of seq=5 must be rejected — proof the register path did not reset the
	// high-water (which the old separate-seed step could not, but a future regression
	// re-installing the session would).
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok", Sequence: 5, Event: "e", Payload: turnSignal(status.TurnIdle)}); err == nil {
		t.Errorf("a replayed seq=5 was accepted; the anti-replay high-water was reset")
	}
}
