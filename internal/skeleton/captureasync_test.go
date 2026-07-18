package skeleton

// R2.1.2 (agents-tracker-vyd) — captureConversationID must run in its OWN
// per-session goroutine (the sampleGridAsync pattern, gridtap_test.go), so a slow
// disk read for one uncaptured session cannot delay dispatching the tap for
// another session. White-box: drives captureConversationIDAsync directly through
// the overridable captureFn seam, mirroring TestGridTap_BusyShimDoesNotBlockOtherSessions.

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestCaptureConversationIDAsync_SlowReadDoesNotBlockOtherSessions(t *testing.T) {
	var mu sync.Mutex
	active, maxConcurrent := 0, 0
	calls := map[string]int{}
	started := make(chan string, 8)
	release := make(chan struct{})

	d := &Daemon{}
	d.captureFn = func(id string) {
		mu.Lock()
		active++
		if active > maxConcurrent {
			maxConcurrent = active
		}
		calls[id]++
		mu.Unlock()
		started <- id
		<-release // simulate a slow disk read holding the capture open
		mu.Lock()
		active--
		mu.Unlock()
	}

	ctx := context.Background()
	d.captureConversationIDAsync(ctx, "A")
	d.captureConversationIDAsync(ctx, "B")
	waitStart(t, started) // A active
	waitStart(t, started) // B active

	// A re-dispatch for A while its capture is still in flight is deduped.
	d.captureConversationIDAsync(ctx, "A")

	close(release)
	drained := make(chan struct{})
	go func() { d.captureWG.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight conversation-id captures did not drain")
	}

	mu.Lock()
	defer mu.Unlock()
	if maxConcurrent < 2 {
		t.Errorf("captures ran serially (maxConcurrent=%d); a slow disk read head-of-line blocked another session", maxConcurrent)
	}
	if calls["A"] != 1 {
		t.Errorf("session A was captured %d times; an in-flight capture must be deduped to 1", calls["A"])
	}
	if calls["B"] != 1 {
		t.Errorf("session B was captured %d times; want exactly 1", calls["B"])
	}
}
