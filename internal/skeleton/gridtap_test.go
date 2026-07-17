package skeleton

// FIX 7: the grid tap samples each running session in its own goroutine (deduped
// per session), so a busy shim can no longer head-of-line block other sessions'
// sampling cadence (L1). This white-box test drives sampleGridAsync through the
// overridable sampleFn seam — a "busy shim" is a sample that blocks.

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestGridTap_BusyShimDoesNotBlockOtherSessions(t *testing.T) {
	var mu sync.Mutex
	active, maxConcurrent := 0, 0
	calls := map[string]int{}
	started := make(chan string, 8)
	release := make(chan struct{})

	d := &Daemon{sampling: make(map[string]struct{})}
	d.sampleFn = func(id string) {
		mu.Lock()
		active++
		if active > maxConcurrent {
			maxConcurrent = active
		}
		calls[id]++
		mu.Unlock()
		started <- id
		<-release // simulate a busy/slow shim holding the sample open
		mu.Lock()
		active--
		mu.Unlock()
	}

	ctx := context.Background()
	// A "busy" session A and another session B are sampled; both must be able to run
	// AT THE SAME TIME (no head-of-line blocking) — the old serial loop would run B
	// only after A's blocking sample returned.
	d.sampleGridAsync(ctx, "A")
	d.sampleGridAsync(ctx, "B")
	waitStart(t, started) // A active
	waitStart(t, started) // B active

	// While A's sample is still in flight, a re-sample of A is DEDUPED (skipped).
	d.sampleGridAsync(ctx, "A")

	close(release)
	drained := make(chan struct{})
	go func() { d.sampleWG.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight grid samples did not drain")
	}

	mu.Lock()
	defer mu.Unlock()
	if maxConcurrent < 2 {
		t.Errorf("grid samples ran serially (maxConcurrent=%d); a busy shim head-of-line blocked another session", maxConcurrent)
	}
	if calls["A"] != 1 {
		t.Errorf("session A was sampled %d times; an in-flight sample must be deduped to 1", calls["A"])
	}
	if calls["B"] != 1 {
		t.Errorf("session B was sampled %d times; want exactly 1", calls["B"])
	}
}

func waitStart(t *testing.T, started <-chan string) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("a grid sample never started (concurrency stalled)")
	}
}
