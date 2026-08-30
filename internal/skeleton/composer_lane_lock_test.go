package skeleton

import (
	"strconv"
	"sync"
	"testing"
)

// TestComposerLane_SupersededTurnUsesOneSynchronizationDomain is the race-detector pin for
// the Stop/retry coordinate. composerSend selects and clears this value while the provider
// queue is moving; interruptTurn reads it to decide whether the turn on screen is already
// proven dead. Those accesses used to be split between itemMu and lane.mu. The split happened
// to rely on the serving-ticket goroutine never overlapping the retry callback, an implicit
// ownership rule neither mutex expressed. Keep the coordinate under the one combined helper
// that also preserves the declared lane.mu -> itemMu order.
func TestComposerLane_SupersededTurnUsesOneSynchronizationDomain(t *testing.T) {
	d := &Daemon{}
	lane := d.composerLaneFor("lock-domain")

	const rounds = 2_000
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				d.withComposerInteractionState(lane, func() {
					if worker%2 == 0 {
						lane.supersededTurn = strconv.Itoa(i)
						return
					}
					_ = lane.supersededTurn
				})
			}
		}()
	}
	close(start)
	wg.Wait()
}
