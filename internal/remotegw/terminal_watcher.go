package remotegw

// A7 F2 wiring — the TerminalWatcher: the gateway's fan-out of server-rendered terminal
// peeks, one supervised Gateway.RunTerminal goroutine per watched session. It mirrors
// LeaseManager (the input plane's per-session lease fan-out): Watch starts a peek
// (idempotent per session), Unwatch stops one, Close stops all. Where a lease routes
// keystrokes IN, a watch streams snapshots OUT -- both are per-session and both must join
// their goroutines cleanly on teardown (no leak on Unwatch/Close/disconnect).
//
// Each watch runs its own supervised loop: RunTerminal dials the daemon remote.sock,
// subscribes to the session's snapshot stream, and forwards each snapshot to the shared
// RelaySink (which seals it to the phone). When RunTerminal returns (the daemon-conn drops,
// or the daemon refuses -- e.g. the kill switch is OFF at subscribe time), the loop backs
// off and reconnects, exactly like Service.runJournal, until its ctx is cancelled. An
// ESTABLISHED peek that the kill switch blanks mid-stream simply goes quiet (the daemon
// stops emitting on the open conn without an error), so the loop stays parked in its read
// -- it does not busy-reconnect.

import (
	"context"
	"sync"
	"time"
)

// TerminalWatcher owns the set of live terminal peeks, one supervised RunTerminal goroutine
// per namespaced session id.
type TerminalWatcher struct {
	gw      *Gateway
	backoff time.Duration

	ctx    context.Context    // parent of every watch ctx; cancelled by Close
	cancel context.CancelFunc // cancels ctx (and thus every watch) on Close
	wg     sync.WaitGroup     // joins every watch goroutine on Close

	mu      sync.Mutex
	watches map[string]context.CancelFunc // session id -> its watch's cancel
	closed  bool
}

// NewTerminalWatcher returns a watcher whose peeks run RunTerminal against gw and reconnect
// after backoff (defaulting to 1s) when a peek's connection drops.
func NewTerminalWatcher(gw *Gateway, backoff time.Duration) *TerminalWatcher {
	if backoff <= 0 {
		backoff = time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &TerminalWatcher{
		gw:      gw,
		backoff: backoff,
		ctx:     ctx,
		cancel:  cancel,
		watches: make(map[string]context.CancelFunc),
	}
}

// Watch starts a supervised peek for session if one is not already running (idempotent per
// session, so a repeated terminal_watch never spawns a second RunTerminal). It is a no-op
// after Close or for an empty session id.
func (w *TerminalWatcher) Watch(session string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || session == "" {
		return
	}
	if _, ok := w.watches[session]; ok {
		return // already peeking this session
	}
	ctx, cancel := context.WithCancel(w.ctx)
	w.watches[session] = cancel
	w.wg.Add(1)
	go w.run(ctx, session)
}

// Unwatch stops the peek for session (cancel + let its goroutine unwind). It is a no-op for
// a session with no live watch. Unwatch does not block on the goroutine's exit; Close joins
// every goroutine.
func (w *TerminalWatcher) Unwatch(session string) {
	w.mu.Lock()
	cancel := w.watches[session]
	delete(w.watches, session)
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Close cancels every live peek and joins their goroutines, leaving none behind. It is
// idempotent.
func (w *TerminalWatcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.watches = make(map[string]context.CancelFunc)
	w.mu.Unlock()
	w.cancel() // cancels w.ctx -> every watch ctx -> every RunTerminal
	w.wg.Wait()
	return nil
}

// run supervises one session's peek: it (re)runs RunTerminal, backing off between attempts,
// until ctx is cancelled (Unwatch/Close). It mirrors Service.runJournal's reconnect loop.
func (w *TerminalWatcher) run(ctx context.Context, session string) {
	defer w.wg.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		_ = w.gw.RunTerminal(ctx, session)
		if ctx.Err() != nil {
			return
		}
		t := time.NewTimer(w.backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
