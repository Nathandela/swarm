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
// off and reconnects, exactly like Service.runJournal, until its ctx is cancelled. When the
// kill switch flips OFF mid-stream the daemon now TERMINATES the peek and signals the gateway
// (an OpError frame), so RunTerminal returns and the loop backs off -- while OFF each reconnect
// is refused at subscribe time (bounded backoff-retry at `backoff`), and when the switch flips
// back ON a reconnect re-subscribes and the peek resumes (OFF->ON recovery).

import (
	"context"
	"sync"
	"time"
)

// terminalRunner runs one session's read-only peek to completion, returning when the peek
// ends (conn drop, daemon refusal, or ctx cancel). *Gateway is the production implementation
// (RunTerminal); the seam lets a test inject a fake runner to exercise the watch lifecycle
// (join-on-Unwatch, no-overlap-on-rewatch) without a live daemon.
type terminalRunner interface {
	RunTerminal(ctx context.Context, session string) error
}

// *Gateway is the production terminalRunner. Pinned at compile time.
var _ terminalRunner = (*Gateway)(nil)

// watchHandle is one live peek's teardown handle: cancel stops its supervised loop and done
// is closed when that loop's goroutine has fully exited, so Unwatch/Close can JOIN it before
// returning (a rapid Unwatch->Watch must not overlap two peeks on the same session).
type watchHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// TerminalWatcher owns the set of live terminal peeks, one supervised RunTerminal goroutine
// per namespaced session id.
type TerminalWatcher struct {
	runner  terminalRunner
	backoff time.Duration

	ctx    context.Context    // parent of every watch ctx; cancelled by Close
	cancel context.CancelFunc // cancels ctx (and thus every watch) on Close
	wg     sync.WaitGroup     // joins every watch goroutine on Close

	mu      sync.Mutex
	watches map[string]*watchHandle // session id -> its watch's teardown handle
	closed  bool
}

// NewTerminalWatcher returns a watcher whose peeks run RunTerminal against gw and reconnect
// after backoff (defaulting to 1s) when a peek's connection drops.
func NewTerminalWatcher(gw *Gateway, backoff time.Duration) *TerminalWatcher {
	return newTerminalWatcher(gw, backoff)
}

// newTerminalWatcher is the runner-injecting constructor NewTerminalWatcher and the tests
// share, so a fake terminalRunner can drive the watch lifecycle without a live daemon.
func newTerminalWatcher(runner terminalRunner, backoff time.Duration) *TerminalWatcher {
	if backoff <= 0 {
		backoff = time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &TerminalWatcher{
		runner:  runner,
		backoff: backoff,
		ctx:     ctx,
		cancel:  cancel,
		watches: make(map[string]*watchHandle),
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
	done := make(chan struct{})
	w.watches[session] = &watchHandle{cancel: cancel, done: done}
	w.wg.Add(1)
	go w.run(ctx, session, done)
}

// Unwatch stops the peek for session and JOINS its goroutine before returning, so a rapid
// Unwatch->Watch never overlaps the old peek with the new one (two RunTerminal goroutines /
// read-only taps on the same session). It is a no-op for a session with no live watch. The
// join is bounded: cancelling the ctx makes RunTerminal return within its read deadline.
func (w *TerminalWatcher) Unwatch(session string) {
	w.mu.Lock()
	h := w.watches[session]
	delete(w.watches, session)
	w.mu.Unlock()
	if h != nil {
		h.cancel()
		<-h.done // join: the peek's goroutine has fully exited (tap released) before we return
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
	w.watches = make(map[string]*watchHandle)
	w.mu.Unlock()
	w.cancel() // cancels w.ctx -> every watch ctx -> every RunTerminal
	w.wg.Wait() // join every goroutine (each closes its done via run's defer)
	return nil
}

// run supervises one session's peek: it (re)runs RunTerminal, backing off between attempts,
// until ctx is cancelled (Unwatch/Close). It mirrors Service.runJournal's reconnect loop.
// It closes done on exit so Unwatch/Close can join it (no peek overlaps a rewatch).
func (w *TerminalWatcher) run(ctx context.Context, session string, done chan struct{}) {
	defer w.wg.Done()
	defer close(done)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = w.runner.RunTerminal(ctx, session)
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
