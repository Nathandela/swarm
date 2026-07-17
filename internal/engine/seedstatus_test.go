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
	e.RegisterSession("s1", "tok", 1, kinds("hook", "heuristic"))
	// Reconcile seeds the PERSISTED status (turn=active) recovered after a restart.
	e.SeedStatus("s1", status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone})

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
	e.RegisterSession("s1", "tok", 1, kinds("hook"))
	e.SeedStatus("s1", status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone})
	now = now.Add(2 * time.Second)
	e.Tick()
	if gotEmit {
		t.Errorf("Tick emitted for a turn=unknown baseline session; only an active turn is a staleness-downgrade candidate")
	}
}
