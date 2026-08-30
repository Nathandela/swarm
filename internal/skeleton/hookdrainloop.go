package skeleton

// THE PRODUCTION CALLER of HookDrainer (R6 review fix-pack round 1, HIGH 3).
//
// The R6 slice shipped a complete, well-tested structured-capture channel that nothing
// ever ran: shimSpawnConfig carried no hook socket, so the shim bound no listener; the
// agent env carried no EnvHookSocket, so hookclient.PostSmart always took the daemon
// fallback; and no HookDrainer was constructed outside a test, so EmitStructuredGap
// could not fire even in principle. Playbook §6.1's obligations -- "the daemon drains
// the spool idempotently", "daemon unavailability neither fails a provider hook nor
// loses an accepted item", "a spool/cursor gap emits an exact structured_gap boundary
// before retained/future events resume" -- are properties of a RUNNING system, so this
// file is the part that makes them true of one. The launch half lives in internal/daemon
// (spawnShim mints the socket path and the DRAIN token, injectHookEnv points the
// agent at it, SessionHookChannel recovers all of it after a restart).
//
// ONE LOOP PER SESSION, started from the same OnSessionStart seam that registers a
// session with the status engine -- so it covers a fresh launch AND the reconcile of a
// session whose shim outlived the daemon, which is exactly the survival boundary this
// channel exists for.

import (
	"errors"
	"log"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// hookDrainInterval is how often each session's spool is drained. The shim's spool is
// the durable buffer, so this cadence bounds LATENCY (how long an accepted hook waits
// before it reaches the engine), never loss. Kept well inside the L1 change->delivery
// budget the grid tap already spends 500ms of.
const hookDrainInterval = 250 * time.Millisecond

// hookDrainState is the assembly's set of live per-session drain loops, and the drainer
// each one runs -- kept so stopHookDrain can perform the session's FINAL drain through
// the same object (and therefore the same drainMu) the loop uses.
type hookDrainState struct {
	mu       sync.Mutex
	stop     map[string]chan struct{}
	drainers map[string]*HookDrainer
	wg       sync.WaitGroup
}

// startHookDrain begins draining sessionID's shim-owned spool, if that session has a
// hook channel at all. A session launched by a pre-R6 daemon has none, and gets no
// loop -- the same "unset means disabled" compat the shim and PostSmart already keep.
// Re-starting an already-running session's loop is a no-op (reconcile can re-fire
// OnSessionStart for a session this incarnation already registered).
func (d *Daemon) startHookDrain(sessionID string) {
	if d.core == nil {
		return
	}
	ch, ok := d.core.SessionHookChannel(sessionID)
	if !ok || ch.SocketPath == "" {
		return
	}
	d.drains.mu.Lock()
	defer d.drains.mu.Unlock()
	if d.drains.stop == nil {
		d.drains.stop = map[string]chan struct{}{}
		d.drains.drainers = map[string]*HookDrainer{}
	}
	if _, running := d.drains.stop[sessionID]; running {
		return
	}
	hd := NewHookDrainer(d, sessionID, ch.SocketPath, ch.CursorPath)
	hd.SetToken(ch.DrainToken)
	hd.SetSpoolPath(ch.SpoolPath)
	stop := make(chan struct{})
	d.drains.stop[sessionID] = stop
	d.drains.drainers[sessionID] = hd
	d.drains.wg.Add(1)
	go d.runHookDrain(sessionID, hd, stop)
}

// startHookDrainsForRunning starts a loop for every session the core is already
// serving. It exists because daemon.Open runs reconcile SYNCHRONOUSLY, firing
// OnSessionStart (registerSession) for every reconnected session BEFORE Open has
// returned the core this assembly holds -- so those sessions, the ones whose shims
// outlived the daemon and therefore the ones the spool exists for, would otherwise be
// the only ones never drained. Called once, after the core is wired and before the
// daemon serves. Idempotent per session, so the overlap with registerSession's own
// call for a freshly launched session costs nothing.
func (d *Daemon) startHookDrainsForRunning() {
	if d.core == nil {
		return
	}
	for _, m := range d.core.List() {
		if m.Status.Process == status.ProcessRunning {
			d.startHookDrain(m.ID)
		}
	}
}

// stopHookDrain signals sessionID's loop to end and then performs the session's FINAL
// drain, synchronously, before returning.
//
// THE FINAL DRAIN IS THE WHOLE POINT (R6 review fix-pack round 2, BLOCKER 2). This is
// called from OnSessionEnd, and round 1 only signalled: it did no last read. But the
// shim's hookServer is the ONLY socket-side reader of hooks.spool and it is shut down
// the moment the agent is reaped, so every event durably acked inside the last 250ms
// drain interval became permanently unreachable while its bytes sat on disk -- 5/5 trials
// through the real launch path lost the final Stop, leaving the session stuck "active".
// That is not a narrow race: an agent that exits right after its last hook (a headless
// `claude -p`, a Ctrl-C after a turn) hits it every time. HookDrainer.FinalDrain tries
// the socket and then always reads the spool FILE, which is what makes the ack contract
// hold at the most common moment in a session's life.
//
// IT RUNS ON THIS GOROUTINE, not by joining the loop's. Round 1's comment gave the
// reason not to join ("a loop mid-drain is holding the journal writer this side of that
// path"), and it still holds; FinalDrain instead takes the drainer's own drainMu, so it
// simply serializes behind an in-flight poll rather than waiting on the goroutine. The
// ORDER in endSession matters and is deliberate: this runs BEFORE d.eng.EndSession(id),
// so a record applied here still authenticates against a live engine session.
func (d *Daemon) stopHookDrain(sessionID string) {
	d.drains.mu.Lock()
	hd := d.drains.drainers[sessionID]
	if stop, ok := d.drains.stop[sessionID]; ok {
		close(stop)
		delete(d.drains.stop, sessionID)
	}
	delete(d.drains.drainers, sessionID)
	d.drains.mu.Unlock()

	if hd != nil {
		hd.FinalDrain()
	}
}

// stopHookDrains ends every live loop and waits for them, so no drain can still be
// applying a record when Close tears the core down under it. It does NOT final-drain:
// these sessions are still RUNNING, their shims outlive this daemon by design, and the
// next incarnation resumes from the same persisted cursor. Only a session that has
// actually ENDED has a spool no one will ever come back for.
func (d *Daemon) stopHookDrains() {
	d.drains.mu.Lock()
	for id, stop := range d.drains.stop {
		close(stop)
		delete(d.drains.stop, id)
		delete(d.drains.drainers, id)
	}
	d.drains.mu.Unlock()
	d.drains.wg.Wait()
}

// runHookDrain polls one session's spool until the session ends, the daemon closes, or
// a gap remains unreadable even after the explicit-boundary recovery reset.
func (d *Daemon) runHookDrain(sessionID string, hd *HookDrainer, stop chan struct{}) {
	defer d.drains.wg.Done()

	t := time.NewTicker(hookDrainInterval)
	defer t.Stop()
	var everDrained, loggedFailure bool
	for {
		select {
		case <-stop:
			return
		case <-d.closing:
			return
		case <-t.C:
		}

		_, _, err := hd.DrainOnce()
		switch {
		case err == nil:
			everDrained = true
		case errors.Is(err, ErrHookDrainGap) && hd.recoveringGap():
			// The boundary is durable and the cursor now spells "adopt the
			// retained sequence space". Stay alive: the next tick folds the
			// retained side, and every later tick keeps capturing future events.
			// No record beyond the hole was applied in the boundary-producing
			// drain, so journal chronology remains explicit.
			continue
		case errors.Is(err, ErrHookDrainGap):
			// The SAME boundary survived a read from the reset coordinate. There
			// is no retained far side to adopt (the torn-tail shape), so every
			// further poll would rediscover identical bytes and learn nothing.
			return
		case everDrained && !loggedFailure:
			// A channel that WORKED and then stopped working is worth exactly one line
			// (agents-tracker-sskl: a session's signal going silently dead). A failure
			// before the first success is ordinary -- the shim binds its hook socket
			// after it spawns the agent -- and says nothing.
			loggedFailure = true
			log.Printf("skeleton: hook drain for session %s: %v", sessionID, err)
		}
	}
}
