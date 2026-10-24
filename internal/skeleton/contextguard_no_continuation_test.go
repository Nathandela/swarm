package skeleton

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/contextguard"
)

func TestContextGuardCompactionDoesNotInventAnotherTurn(t *testing.T) {
	r := newDispatchRig(t)
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.waitPhase(t, contextguard.StateAwaitingConfirmation)
	calls := r.conn.snapshot()
	if calls[0].method != "thread/compact/start" || !calls[0].wrote {
		t.Fatal("automatic compaction did not cross its normal write boundary")
	}
	r.ingestLifecycle(t, 2, "item/started")
	r.ingestLifecycle(t, 3, "item/completed")
	r.waitPhase(t, contextguard.StateLatched)
	r.nudge()
	r.requireNoPlainCallsFor(t, 150*time.Millisecond)
	// A later completed compaction still supplies no evidence of unfinished work.
	r.ingestUsage(t, 4, 60, 100)
	r.waitPhase(t, contextguard.StateArmed)
	r.ingestUsage(t, 5, 90, 100)
	r.waitCalls(t, 2)
	r.ingestLifecycle(t, 6, "item/completed")
	r.waitPhase(t, contextguard.StateLatched)
	r.nudge()
	r.requireNoPlainCallsFor(t, 150*time.Millisecond)
}
