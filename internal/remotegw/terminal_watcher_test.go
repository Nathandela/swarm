package remotegw

// Tests for the A7 F2 terminal-PEEK wiring: the CommandBridge routes terminal_watch /
// terminal_unwatch to the TerminalWatchRouter (never to the daemon forwarder), and the
// TerminalWatcher is idempotent per session and leaks no goroutine on Unwatch/Close.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// fakeTerminalWatchRouter records Watch/Unwatch so the routing is unit-tested without a
// live daemon terminal_subscribe.
type fakeTerminalWatchRouter struct {
	mu        sync.Mutex
	watched   []string
	unwatched []string
}

func (f *fakeTerminalWatchRouter) Watch(session string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watched = append(f.watched, session)
}

func (f *fakeTerminalWatchRouter) Unwatch(session string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unwatched = append(f.unwatched, session)
}

// TestCommandBridge_RoutesTerminalWatch: an unsigned terminal_watch/terminal_unwatch
// RemoteCommand is routed to the watch plane by its target session, and is NEVER forwarded
// to the daemon (a peek is a read; the daemon gates it by cap + kill switch, not a device
// signature the gateway would forward).
func TestCommandBridge_RoutesTerminalWatch(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 3)
	}
	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealRemoteCmd(t, key, 1, protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{Action: protocol.ActionTerminalWatch, Session: "m/s1"}})},
		{Cursor: 2, Envelope: sealRemoteCmd(t, key, 2, protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{Action: protocol.ActionTerminalUnwatch, Session: "m/s1"}})},
	}}
	fwd := &fakeForwarder{}
	watch := &fakeTerminalWatchRouter{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Watchers:    watch,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	if _, err := b.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	watch.mu.Lock()
	defer watch.mu.Unlock()
	if len(watch.watched) != 1 || watch.watched[0] != "m/s1" {
		t.Fatalf("watched = %v, want [m/s1]", watch.watched)
	}
	if len(watch.unwatched) != 1 || watch.unwatched[0] != "m/s1" {
		t.Fatalf("unwatched = %v, want [m/s1]", watch.unwatched)
	}
	// A peek request must NOT be forwarded to the daemon: it is unsigned and read-only.
	fwd.mu.Lock()
	defer fwd.mu.Unlock()
	if len(fwd.ops) != 0 {
		t.Fatalf("forwarded %d ops to the daemon; a terminal_watch must never be forwarded", len(fwd.ops))
	}
	// No reply is sealed for a watch (unlike a mutating command).
	if len(mb.replies) != 0 {
		t.Fatalf("sealed %d replies; a terminal_watch produces none", len(mb.replies))
	}
}

// TestTerminalWatcher_IdempotentUnwatchAndCloseNoLeak: Watch is idempotent per session,
// Unwatch drops one peek, and Close joins every supervised goroutine (no leak). The gateway
// dials a nonexistent socket so each RunTerminal returns at once and the loop parks on its
// backoff timer, exercising the cancel/join paths deterministically.
func TestTerminalWatcher_IdempotentUnwatchAndCloseNoLeak(t *testing.T) {
	gw := New("/nonexistent/swarm-terminal-watcher.sock", &recordingTerminalSink{done: make(chan protocol.TerminalSnapshot, 1)})
	w := NewTerminalWatcher(gw, 5*time.Millisecond)

	w.Watch("m/s1")
	w.Watch("m/s1") // idempotent: no second goroutine for the same session
	w.Watch("m/s2")
	if n := w.numWatches(); n != 2 {
		t.Fatalf("live watches = %d, want 2 (Watch must be idempotent per session)", n)
	}

	w.Unwatch("m/s1")
	if n := w.numWatches(); n != 1 {
		t.Fatalf("live watches after Unwatch = %d, want 1", n)
	}
	w.Unwatch("m/nonexistent") // no-op for a session with no watch

	// Close must join every goroutine within a bound; a hang here is a leak.
	done := make(chan struct{})
	go func() { _ = w.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TerminalWatcher.Close did not return within 2s: a peek goroutine leaked")
	}

	// Watch after Close is a no-op (fail-closed against a post-shutdown request).
	w.Watch("m/s3")
	if n := w.numWatches(); n != 0 {
		t.Fatalf("live watches after Close = %d, want 0 (Watch must be a no-op after Close)", n)
	}
}

// numWatches reports the live watch count under the watcher lock (test-only accessor).
func (w *TerminalWatcher) numWatches() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.watches)
}

// blockingRunner is a fake terminalRunner: each RunTerminal blocks until its ctx is cancelled,
// then does a bit of teardown work (exitDelay) before returning. It tracks live and peak
// concurrent invocations so a test can prove Unwatch JOINS the goroutine (live back to 0 when
// Unwatch returns) and a rewatch never OVERLAPS the old peek (peak stays 1).
type blockingRunner struct {
	exitDelay time.Duration
	mu        sync.Mutex
	live      int
	peak      int
	starts    int
}

func (r *blockingRunner) RunTerminal(ctx context.Context, _ string) error {
	r.mu.Lock()
	r.live++
	r.starts++
	if r.live > r.peak {
		r.peak = r.live
	}
	r.mu.Unlock()

	<-ctx.Done() // park until Unwatch/Close cancels this peek

	if r.exitDelay > 0 {
		time.Sleep(r.exitDelay) // simulate tap-release teardown; a non-joining Unwatch would overlap here
	}
	r.mu.Lock()
	r.live--
	r.mu.Unlock()
	return ctx.Err()
}

func (r *blockingRunner) stat() (live, peak, starts int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live, r.peak, r.starts
}

// TestTerminalWatcher_UnwatchJoinsBeforeReturn pins Blocker 3: Unwatch must JOIN the peek's
// goroutine before returning, so a rapid Unwatch->Watch never overlaps two RunTerminal
// goroutines (two read-only taps) on the same session. The fake runner's teardown is delayed,
// so a NON-joining Unwatch would return while the old peek is still live and the rewatch would
// push peak concurrency to 2; the joining Unwatch keeps it at 1 and leaves live at 0.
func TestTerminalWatcher_UnwatchJoinsBeforeReturn(t *testing.T) {
	runner := &blockingRunner{exitDelay: 50 * time.Millisecond}
	w := newTerminalWatcher(runner, 5*time.Millisecond)
	t.Cleanup(func() { _ = w.Close() })

	// Start a peek and wait until its goroutine is actually running.
	w.Watch("m/s1")
	waitFor(t, func() bool { _, _, starts := runner.stat(); return starts == 1 }, 2*time.Second,
		"first peek never started")

	// Unwatch must not return until the peek goroutine has fully exited (its teardown done).
	w.Unwatch("m/s1")
	if live, _, _ := runner.stat(); live != 0 {
		t.Fatalf("after Unwatch returned, %d peek goroutine(s) still live; Unwatch must JOIN before returning (Blocker 3)", live)
	}

	// Rewatch immediately: because Unwatch joined, the old peek is gone, so the new one never
	// overlaps it. A non-joining Unwatch would let this rewatch run concurrently with the still
	// -tearing-down old peek (peak == 2).
	w.Watch("m/s1")
	waitFor(t, func() bool { _, _, starts := runner.stat(); return starts == 2 }, 2*time.Second,
		"rewatch never started")
	w.Unwatch("m/s1")

	if _, peak, _ := runner.stat(); peak != 1 {
		t.Fatalf("peak concurrent peeks on one session = %d, want 1; a rewatch overlapped a not-yet-joined peek (Blocker 3)", peak)
	}
}

// waitFor polls cond until true or the deadline, failing with msg on timeout.
func waitFor(t *testing.T, cond func() bool, within time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}
