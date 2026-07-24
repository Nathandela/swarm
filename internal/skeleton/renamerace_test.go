package skeleton

// codex v0.5 re-confirm, maintained HIGH — the stale-rename event race. The daemon
// has TWO event producers writing full meta snapshots to the same channel: the
// direct emitStatus path (persist, then Get, then send) and the roster poller.
// The losing interleaving: emitStatus captures {newStatus, oldName}, pauses before
// its send; a Rename lands; the poller queues {newStatus, newName} and marks it
// seen; emitStatus's stale snapshot then arrives LAST, the client's row reverts to
// the old name, and the poller never repairs it (seen == current). Persistence is
// correct; the live board is wrong until an unrelated change.
//
// The fix is structural: the poller is the SOLE snapshot producer (emitStatus and
// Rename only nudge it to sample immediately), so a stale snapshot cannot exist.
// This test pins the property at the real client surface: under a concurrent
// rename+status hammer, the subscribe stream must converge to the final name and
// never deliver an event that regresses a session's name after a newer one.

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func TestFanout_RenameNeverRevertedByConcurrentStatus(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print HELLO\nidle 60s\n")

	c := dialClient(t, sk, "subscribe")
	events, err := c.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	waitOneView(t, c)

	// Hammer: renames racing engine-derived status emits. Each iteration issues the
	// rename FIRST, then the status emit — the exact ordering whose stale snapshot
	// could revert the name under the two-producer design.
	const rounds = 120
	final := ""
	for i := 1; i <= rounds; i++ {
		final = fmt.Sprintf("n%03d", i)
		var wg sync.WaitGroup
		wg.Add(2)
		go func(name string) {
			defer wg.Done()
			if err := sk.api.Rename(m.ID, name); err != nil {
				t.Errorf("Rename: %v", err)
			}
		}(final)
		go func(i int) {
			defer wg.Done()
			turn := status.TurnActive
			if i%2 == 0 {
				turn = status.TurnIdle
			}
			sk.api.emitStatus(m.ID, status.Status{Process: status.ProcessRunning, Turn: turn, Interaction: status.InteractionNone})
		}(i)
		wg.Wait()
	}

	// Drain the subscribe stream until it goes quiet for a full second (several
	// poll intervals), collecting every delivered name in arrival order. The LAST
	// delivered name must be the final rename — a trailing stale snapshot is
	// exactly the codex interleaving.
	var names []string
	overall := time.After(15 * time.Second)
drain:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				break drain
			}
			if ev.Session.ID != "" {
				names = append(names, ev.Session.Name)
			}
		case <-time.After(time.Second):
			break drain
		case <-overall:
			break drain
		}
	}

	if len(names) == 0 {
		t.Fatal("no events delivered")
	}
	if last := names[len(names)-1]; last != final {
		t.Fatalf("subscribe stream converged to %q, want the final rename %q (a trailing stale snapshot reverted the board - the two-producer race)", last, final)
	}
}
