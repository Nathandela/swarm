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
	"errors"
	"log"
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

// terminalBlanker is the seam that lets an EXPIRED watch tell the phone the machine stopped
// rendering (ADR-017 T4-b, round-3 moderate 5). It is optional on the runner for the same
// reason TerminalSink is optional on the sink: a runner that cannot reach a phone has nothing
// to blank, and its absence must not be an error.
//
// WHY EXPIRY NEEDS IT AND AN EXPLICIT UNWATCH DOES NOT. `Unwatch` is the PHONE saying it
// stopped looking -- leaving the screen, or backgrounding -- and blanking there would erase a
// cached grid the user may legitimately come back to, while fighting the screen's own
// teardown. `Reap` and `UnwatchAll` are the opposite case: the phone said nothing, so it is
// still holding the last grid and still labelling it live. Only an ending the phone did not
// ask for blanks.
type terminalBlanker interface {
	BlankTerminal(session string) error
}

// *Gateway is the production terminalBlanker too.
var _ terminalBlanker = (*Gateway)(nil)

// watchHandle is one live peek's teardown handle: cancel stops its supervised loop and done
// is closed when that loop's goroutine has fully exited, so Unwatch/Close can JOIN it before
// returning (a rapid Unwatch->Watch must not overlap two peeks on the same session).
type watchHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
	// deadline is this watch's HORIZON (ADR-017 amendment T4-b). A watch is a lease, and
	// an unrenewed one expires: Watch/Unwatch/Close were the whole lifecycle, so a phone
	// that simply STOPPED READING left the machine rendering full screens, sealing them
	// and appending them against the shared 8-appends/s budget indefinitely -- the
	// transcript of every other session yielding to snapshots nobody was reading -- and
	// building a backlog the phone then replayed on reconnect.
	deadline time.Time
}

// TerminalWatcher owns the set of live terminal peeks, one supervised RunTerminal goroutine
// per namespaced session id.
type TerminalWatcher struct {
	runner  terminalRunner
	backoff time.Duration
	// horizon is how long a watch stays live without a renewal, and now is its clock --
	// injectable so the horizon is provable without sleeping through it, because a horizon
	// test that sleeps is a horizon test that gets shortened later.
	horizon time.Duration
	now     func() time.Time

	ctx    context.Context    // parent of every watch ctx; cancelled by Close
	cancel context.CancelFunc // cancels ctx (and thus every watch) on Close
	wg     sync.WaitGroup     // joins every watch goroutine on Close

	mu      sync.Mutex
	watches map[string]*watchHandle // session id -> its watch's teardown handle
	closed  bool
}

// bindParent re-roots the watcher's peek-context tree at parent so that cancelling parent (the
// Service ctx, e.g. on revoke) stops every peek reconnecting IMMEDIATELY and structurally --
// not incidentally via the kill switch, and not only when the deferred Close runs after Run
// returns (opus#2 / defense-in-depth). Service.Run calls it once at start, before any Watch, so
// the constructor's context.Background() root (used by unit tests that never bind) is replaced
// with a child of the Service ctx. It is a no-op after Close; the deferred Close still joins the
// peek goroutines. Normal Watch/Unwatch/reconnect behavior is unchanged.
func (w *TerminalWatcher) bindParent(parent context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.cancel() // release the constructor's Background-rooted ctx (no watches derive from it yet)
	w.ctx, w.cancel = context.WithCancel(parent)
}

// NewTerminalWatcher returns a watcher whose peeks run RunTerminal against gw and reconnect
// after backoff (defaulting to 1s) when a peek's connection drops.
func NewTerminalWatcher(gw *Gateway, backoff time.Duration) *TerminalWatcher {
	return newTerminalWatcher(gw, backoff, DefaultWatchHorizon, time.Now)
}

// DefaultWatchHorizon is how long a TerminalViewV1 watch survives without a renewal
// (ADR-017 T4-b).
//
// IT IS SHORTER THAN THE CONTROL HORIZON, and deliberately so. The control horizon is
// fifteen minutes because a user holding a keyboard is present by assumption and the
// ceremony to re-enter is deliberate. A watch has no ceremony and no user gesture: its
// only evidence that anyone is still looking is the renewal itself, and everything it
// costs while nobody is -- rendering, sealing, and appends spent from a budget shared with
// every other session's transcript -- accrues to sessions that are not being watched. So
// the watch's horizon is the shortest one that a phone on a bad link can still meet, and
// the renewal is cheap.
const DefaultWatchHorizon = 60 * time.Second

// newTerminalWatcher is the runner-injecting constructor NewTerminalWatcher and the tests
// share, so a fake terminalRunner can drive the watch lifecycle without a live daemon.
func newTerminalWatcher(runner terminalRunner, backoff, horizon time.Duration, now func() time.Time) *TerminalWatcher {
	if backoff <= 0 {
		backoff = time.Second
	}
	if horizon <= 0 {
		horizon = DefaultWatchHorizon
	}
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &TerminalWatcher{
		runner:  runner,
		backoff: backoff,
		horizon: horizon,
		now:     now,
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
	if h, ok := w.watches[session]; ok {
		// Already peeking this session -- EXACTLY ONE PRODUCER PER SESSION (T4-a): the
		// coalescer holds one newest snapshot per session, latest-wins, and two producers
		// with independent revision counters make the released stream zigzag so the
		// phone's "strictly greater" rule discards an arbitrary half of it.
		//
		// A repeated Watch RENEWS, because re-asserting a watch is the natural thing for a
		// phone to do after a reconnect and it must not be expired out from under itself
		// while it is actively asking.
		h.deadline = w.now().Add(w.horizon)
		return
	}
	ctx, cancel := context.WithCancel(w.ctx)
	done := make(chan struct{})
	w.watches[session] = &watchHandle{cancel: cancel, done: done, deadline: w.now().Add(w.horizon)}
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

// Renew extends session's watch by one horizon. It is the phone's evidence that someone is
// still looking, on the same discipline as the control keepalive (ADR-017 T4-b), and a
// no-op for a session with no live watch -- a renewal never STARTS one, because that would
// let a phone acquire a peek without the verb the capability gate is written over.
func (w *TerminalWatcher) Renew(session string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if h, ok := w.watches[session]; ok {
		h.deadline = w.now().Add(w.horizon)
	}
}

// WatchLive reports whether session has a live, unexpired watch. It is the third clause of
// the liveness predicate the render loop already polls every tick -- kill switch AND
// capability AND watch liveness -- so the horizon costs no new loop.
func (w *TerminalWatcher) WatchLive(session string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	h, ok := w.watches[session]
	return ok && w.now().Before(h.deadline)
}

// reapInterval is how often the reaper must run for THIS watcher's horizon: a quarter of it,
// so an expired watch outlives its horizon by at most that and never by the horizon again.
// It is derived from the watcher's own horizon rather than from the default constant, or a
// watcher configured with a different horizon would be reaped on a cadence that has nothing
// to do with it.
func (w *TerminalWatcher) reapInterval() time.Duration {
	w.mu.Lock()
	horizon := w.horizon
	w.mu.Unlock()
	d := horizon / 4
	if d <= 0 {
		d = time.Second
	}
	return d
}

// Reap ends every watch whose horizon has passed, cancelling its peek and joining it
// exactly as Unwatch does. It is called from the tick that already exists.
func (w *TerminalWatcher) Reap() {
	now := w.now()
	var expired []*watchHandle
	var sessions []string
	w.mu.Lock()
	for session, h := range w.watches {
		if !now.Before(h.deadline) {
			delete(w.watches, session)
			expired = append(expired, h)
			sessions = append(sessions, session)
		}
	}
	w.mu.Unlock()
	for _, h := range expired {
		h.cancel()
		<-h.done // join, exactly as Unwatch does: an expired watch leaves no goroutine behind
	}
	// AND THE PHONE IS TOLD. The peek ends from THIS side, so the daemon sends nothing and
	// the phone keeps the last grid with no staleness signal on it (round-3 moderate 5).
	w.blank(sessions)
}

// blank publishes an empty snapshot for each session whose watch ended without the phone
// asking, so the screen cannot present a grid the machine stopped rendering as a live
// terminal. A runner that cannot reach a phone is a no-op.
func (w *TerminalWatcher) blank(sessions []string) {
	if len(sessions) == 0 {
		return
	}
	b, ok := w.runner.(terminalBlanker)
	if !ok {
		return
	}
	for _, session := range sessions {
		if err := b.BlankTerminal(session); err != nil {
			log.Printf("remotegw: could not blank the phone's copy of %s: %v", session, err)
		}
	}
}

// UnwatchAll ends EVERY live watch immediately -- transport loss (ADR-017 T4-b). It does
// not wait for each watch to time out on its own horizon: until the horizon passed, the
// machine would be rendering and appending for a phone that provably is not there.
//
// It is not Close: the watcher stays usable, so a reconnecting phone can watch again.
func (w *TerminalWatcher) UnwatchAll(reason string) {
	w.mu.Lock()
	handles := make([]*watchHandle, 0, len(w.watches))
	sessions := make([]string, 0, len(w.watches))
	for session, h := range w.watches {
		delete(w.watches, session)
		handles = append(handles, h)
		sessions = append(sessions, session)
	}
	w.mu.Unlock()
	for _, h := range handles {
		h.cancel()
		<-h.done
	}
	// Transport loss is not the phone saying it stopped looking; it is the LINK dying under
	// a phone that said nothing, so every screen it ends is a screen still holding its last
	// grid. Blanked for Reap's reason (round-3 moderate 5).
	w.blank(sessions)
	if reason != "" && len(handles) > 0 {
		log.Printf("remotegw: ended %d terminal watch(es): %s", len(handles), reason)
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
	w.cancel()  // cancels w.ctx -> every watch ctx -> every RunTerminal
	w.wg.Wait() // join every goroutine (each closes its done via run's defer)
	return nil
}

// endWatch drops session's handle WITHOUT joining it -- it is called from that watch's own
// goroutine, where joining would deadlock. The goroutine returns immediately after.
func (w *TerminalWatcher) endWatch(session string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.watches, session)
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
		err := w.runner.RunTerminal(ctx, session)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errPeekCapabilityRefused) || errors.Is(err, errWatchHorizonPassed) {
			// ADR-017 T2-c, the GATEWAY side of the daemon's session gate: a peek the
			// session's capability record forbids ends here rather than reconnecting
			// forever. The record is authored once per incarnation and immutable except in
			// the degrading direction, so a retry can only earn the same refusal -- and a
			// downlevel or compromised app that merely asks must not be able to hold a
			// permanent reconnect loop open against a healthy structured session.
			//
			// errWatchHorizonPassed lands here for the mirror-image reason (T4-b): nobody
			// is looking, so a reconnect would resume rendering, sealing and appending for
			// an absent phone -- which is the whole cost the horizon exists to stop.
			w.endWatch(session)
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
