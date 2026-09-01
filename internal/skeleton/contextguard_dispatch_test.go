package skeleton

// The ADR-023 amendment 1 dispatch lane, pinned against the live gate evidence
// that shaped it: the provider serializes NOTHING (a compact sent mid-turn
// cancels the turn; two concurrent compacts interrupt each other), so every
// guarantee below is the daemon's own -- quiet promotion, queue-head
// revalidation inside the composer lane, the durable executing record at the
// provider write boundary, and hold-never-resend once bytes may have left.
// Every seam is a fake; the worker goroutine is real.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/contextguard"
	"github.com/Nathandela/swarm/internal/protocol"
)

// fakeDispatchConn honors the CallAtWriteBoundary contract exactly: a
// beforeWrite error aborts with provably no bytes and no afterWrite; otherwise
// afterWrite runs after the write attempt, then the scripted reply decides.
type fakeDispatchConn struct {
	mu    sync.Mutex
	calls []fakeDispatchCall
	// script[i] shapes call i; a missing entry is the happy path.
	script map[int]fakeDispatchBehavior
	// plainCalls records Call (the continuation enqueue path); plainErr, when
	// set, fails every Call.
	plainCalls []fakeDispatchCall
	plainErr   error
}

func (f *fakeDispatchConn) Call(_ context.Context, method string, params, _ any) error {
	f.mu.Lock()
	f.plainCalls = append(f.plainCalls, fakeDispatchCall{method: method, params: params.(map[string]any)})
	err := f.plainErr
	f.mu.Unlock()
	return err
}

func (f *fakeDispatchConn) plainSnapshot() []fakeDispatchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeDispatchCall(nil), f.plainCalls...)
}

type fakeDispatchBehavior struct {
	refuseBeforeCallbacks bool          // the closed-conn shape: error before beforeWrite runs
	replyErr              error         // returned AFTER the write (afterWrite has run)
	replyGate             chan struct{} // when set, the reply blocks until the gate closes
}

type fakeDispatchCall struct {
	method string
	params map[string]any
	wrote  bool
}

var errFakeConnClosed = errors.New("fake conn closed")

func (f *fakeDispatchConn) CallAtWriteBoundary(ctx context.Context, method string, params, _ any, beforeWrite func() error, afterWrite func()) error {
	f.mu.Lock()
	n := len(f.calls)
	behavior := f.script[n]
	f.calls = append(f.calls, fakeDispatchCall{method: method, params: params.(map[string]any)})
	f.mu.Unlock()
	if behavior.refuseBeforeCallbacks {
		return errFakeConnClosed
	}
	if beforeWrite != nil {
		if err := beforeWrite(); err != nil {
			return err
		}
	}
	// The real client writes here, before afterWrite; the flag must follow the
	// same order or an observer that saw afterWrite's effects reads wrote=false.
	f.mu.Lock()
	f.calls[n].wrote = true
	f.mu.Unlock()
	if afterWrite != nil {
		afterWrite()
	}
	if behavior.replyGate != nil {
		// The real client's reply wait ends when the context ends.
		select {
		case <-behavior.replyGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return behavior.replyErr
}

func (f *fakeDispatchConn) snapshot() []fakeDispatchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeDispatchCall(nil), f.calls...)
}

// dispatchRig wires one automatic-capable session over the fakes.
type dispatchRig struct {
	manager *contextGuardManager
	conn    *fakeDispatchConn
	lane    *composerLane
	key     contextguard.Key

	mu          sync.Mutex
	quietNow    bool
	uncertain   bool
	attendedNow bool
}

func newDispatchRig(t *testing.T) *dispatchRig {
	t.Helper()
	manager, _ := contextGuardTestManager(t, true, 80)
	source, ok := adapter.AsContextGuardSource(codex.New())
	if !ok {
		t.Fatal("Codex ContextGuard source unavailable")
	}
	// 0.151.0 is the live-gated version of record (2026-09-01).
	action, ok := source.ContextGuardAction("0.151.0")
	if !ok || !action.AutomaticDispatch {
		t.Fatalf("0.151.0 action = %#v, %v; want automatic", action, ok)
	}
	r := &dispatchRig{
		manager: manager, conn: &fakeDispatchConn{script: map[int]fakeDispatchBehavior{}},
		key: contextGuardTestKey("instance", "epoch"), quietNow: true,
	}
	r.lane = &composerLane{}
	r.lane.ready = sync.NewCond(&r.lane.mu)
	wire := func(s *contextGuardSession) {
		s.conn = r.conn
		s.lane = func() *composerLane { return r.lane }
		s.quiet = func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.quietNow }
		s.uncertain = func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.uncertain }
		// The continuation seams, through the REAL codex continuer at the
		// live-gated version -- the rig proves the worker choreography against
		// the exact request shape production sends.
		if continuer, ok := adapter.AsContextGuardContinuer(codex.New()); ok {
			s.continuation = func(threadID, text string) (string, map[string]any, bool) {
				return continuer.ContextGuardContinuation("0.151.0", threadID, text)
			}
		}
		s.attended = func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.attendedNow }
	}
	if !manager.registerCurrentWired("session", r.key, source, action, nil, wire) {
		t.Fatal("wired registration failed")
	}
	return r
}

func (r *dispatchRig) setQuiet(v bool)     { r.mu.Lock(); r.quietNow = v; r.mu.Unlock() }
func (r *dispatchRig) setUncertain(v bool) { r.mu.Lock(); r.uncertain = v; r.mu.Unlock() }

func (r *dispatchRig) ingestUsage(t *testing.T, seq uint64, used, limit int) {
	t.Helper()
	r.manager.ingest("session", r.key, seq, contextGuardUsageMethod, contextGuardUsageFrame(used, used, limit), time.Now())
}

func (r *dispatchRig) ingestLifecycle(t *testing.T, seq uint64, method string) {
	t.Helper()
	r.manager.ingest("session", r.key, seq, method, contextGuardLifecycleFrame(method, "1700000000000"), time.Now())
}

func (r *dispatchRig) nudge() { r.manager.noteStatus("session") }

func (r *dispatchRig) waitPhase(t *testing.T, want contextguard.State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		view, ok := r.manager.view("session")
		if ok && view.Phase == string(want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("phase = %q (ok=%v); want %q", view.Phase, ok, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *dispatchRig) waitCalls(t *testing.T, want int) []fakeDispatchCall {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		calls := r.conn.snapshot()
		if len(calls) >= want {
			return calls
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider calls = %d; want %d", len(calls), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *dispatchRig) requireNoCallsFor(t *testing.T, d time.Duration) {
	t.Helper()
	time.Sleep(d)
	if calls := r.conn.snapshot(); len(calls) != 0 {
		t.Fatalf("provider calls = %d; want none", len(calls))
	}
}

func TestContextGuardDispatchesOnceQuietAndLatchesOnCompletion(t *testing.T) {
	r := newDispatchRig(t)
	r.ingestUsage(t, 1, 85, 100)
	calls := r.waitCalls(t, 1)
	if calls[0].method != "thread/compact/start" || calls[0].params["threadId"] != contextGuardTestThread {
		t.Fatalf("dispatch = %+v; want thread/compact/start for the session's thread", calls[0])
	}
	r.waitPhase(t, contextguard.StateAwaitingConfirmation)
	if calls := r.conn.snapshot(); !calls[0].wrote {
		t.Fatal("the phase says written but the boundary was never crossed")
	}
	// The provider's own lifecycle confirms and latches; nothing is re-sent.
	r.ingestLifecycle(t, 2, "item/started")
	r.waitPhase(t, contextguard.StateProviderCompacting)
	r.ingestLifecycle(t, 3, "item/completed")
	r.waitPhase(t, contextguard.StateLatched)
	// Still above the re-arm gap: latched holds even at threshold.
	r.ingestUsage(t, 4, 85, 100)
	if calls := r.conn.snapshot(); len(calls) != 1 {
		t.Fatalf("provider calls after latch = %d; want 1", len(calls))
	}
	// Below trigger-10: re-arms, and a fresh crossing dispatches again.
	r.ingestUsage(t, 5, 60, 100)
	r.waitPhase(t, contextguard.StateArmed)
	r.ingestUsage(t, 6, 90, 100)
	r.waitCalls(t, 2)
}

func TestContextGuardNeverDispatchesWhileAttendedOrBusy(t *testing.T) {
	r := newDispatchRig(t)
	r.setQuiet(false) // mid-turn, an interaction, or a human at the controls
	r.ingestUsage(t, 1, 85, 100)
	r.waitPhase(t, contextguard.StatePendingIdle)
	r.requireNoCallsFor(t, 150*time.Millisecond)
	// The session goes quiet; the status edge alone completes the dispatch.
	r.setQuiet(true)
	r.nudge()
	r.waitCalls(t, 1)
}

func TestContextGuardRevalidatesAtTheQueueHead(t *testing.T) {
	r := newDispatchRig(t)
	// Quiet for the promotion, busy again at the queue head: the lane wait is
	// exactly where a composer send or Stop can slip in ahead.
	flips := 0
	r.mu.Lock()
	r.quietNow = true
	r.mu.Unlock()
	// Replace quiet with a counter: first call (promotion) true, later calls false.
	r.withQuietFunc(t, func() bool {
		flips++
		return flips == 1
	})
	r.ingestUsage(t, 1, 85, 100)
	r.waitPhase(t, contextguard.StatePendingIdle)
	r.requireNoCallsFor(t, 150*time.Millisecond)
}

// withQuietFunc swaps the rig session's quiet seam; the worker reads it on its
// own goroutine, so the swap happens under the manager's session lookup.
func (r *dispatchRig) withQuietFunc(t *testing.T, quiet func() bool) {
	t.Helper()
	r.manager.mu.Lock()
	s := r.manager.sessions["session"]
	r.manager.mu.Unlock()
	if s == nil {
		t.Fatal("session not registered")
	}
	s.quiet = quiet
}

func TestContextGuardUncertainLaneBlocksTheDispatch(t *testing.T) {
	r := newDispatchRig(t)
	r.setUncertain(true)
	r.ingestUsage(t, 1, 85, 100)
	r.waitPhase(t, contextguard.StatePendingIdle) // degraded back at the queue head
	r.requireNoCallsFor(t, 150*time.Millisecond)
	r.setUncertain(false)
	r.nudge()
	r.waitCalls(t, 1)
}

func TestContextGuardOrdersBehindTheComposerLane(t *testing.T) {
	r := newDispatchRig(t)
	// Occupy the lane the way a composer send does; the dispatch must queue.
	r.lane.enter()
	r.ingestUsage(t, 1, 85, 100)
	r.requireNoCallsFor(t, 150*time.Millisecond)
	r.lane.leave()
	r.waitCalls(t, 1)
}

func TestContextGuardAbortsAtTheBoundaryWhenExecutingCannotPersist(t *testing.T) {
	r := newDispatchRig(t)
	// Fail persistence exactly when the executing record must become durable:
	// the write-boundary callback returns the error and the contract guarantees
	// no bytes follow.
	r.manager.mu.Lock()
	s := r.manager.sessions["session"]
	r.manager.mu.Unlock()
	inner := s.persistFn
	s.persistFn = func(m contextguard.Machine) error {
		if m.State == contextguard.StateExecuting {
			return errors.New("disk full")
		}
		return inner(m)
	}
	r.ingestUsage(t, 1, 85, 100)
	r.waitPhase(t, contextguard.StateBlockedCorrupt) // an unwritable guard stops automating
	calls := r.waitCalls(t, 1)
	if calls[0].wrote {
		t.Fatal("bytes crossed the boundary although the executing record could not persist")
	}
	view, _ := r.manager.view("session")
	if view.ErrorCode != "state_write_failed" || strings.Contains(view.ErrorCode, "disk") {
		t.Fatalf("view.ErrorCode = %q; want the stable code, never the raw error", view.ErrorCode)
	}
}

func TestContextGuardPreWriteFailureRetriesOnALaterQuietEdge(t *testing.T) {
	r := newDispatchRig(t)
	r.conn.script[0] = fakeDispatchBehavior{refuseBeforeCallbacks: true} // conn already closed
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.waitPhase(t, contextguard.StatePendingIdle) // provably unwritten: retryable
	r.nudge()
	r.waitCalls(t, 2)
	// The phase is the settled fact; the fake's wrote flag trails the call
	// record by the callback interval, so assert it only after the phase landed.
	r.waitPhase(t, contextguard.StateAwaitingConfirmation)
	if calls := r.conn.snapshot(); !calls[1].wrote {
		t.Fatal("the retry did not reach the provider")
	}
}

func TestContextGuardHoldsForeverOnceBytesMayHaveLeft(t *testing.T) {
	r := newDispatchRig(t)
	r.conn.script[0] = fakeDispatchBehavior{replyErr: errors.New("timeout waiting for reply")}
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.waitPhase(t, contextguard.StateOutcomeUnknownHold)
	// Compaction is non-idempotent: no telemetry, edge, or crossing re-sends it.
	r.ingestUsage(t, 2, 95, 100)
	r.nudge()
	time.Sleep(150 * time.Millisecond)
	if calls := r.conn.snapshot(); len(calls) != 1 {
		t.Fatalf("provider calls after unknown outcome = %d; want 1 (hold, never resend)", len(calls))
	}
}

// TestContextGuardReleasesTheLaneAtTheWriteBoundary pins the composer-send
// discipline: the lane is held across the write only. A provider that stalls
// its reply must not stall the user's sends for the dispatch timeout.
func TestContextGuardReleasesTheLaneAtTheWriteBoundary(t *testing.T) {
	r := newDispatchRig(t)
	gate := make(chan struct{})
	defer close(gate)
	r.conn.script[0] = fakeDispatchBehavior{replyGate: gate}
	r.ingestUsage(t, 1, 85, 100)
	r.waitPhase(t, contextguard.StateAwaitingConfirmation) // written; reply still gated
	freed := make(chan struct{})
	go func() {
		r.lane.enter()
		r.lane.leave()
		close(freed)
	}()
	select {
	case <-freed:
	case <-time.After(2 * time.Second):
		t.Fatal("the composer lane is still held during the provider reply wait")
	}
}

// session returns the rig's live session for direct seam access (same package).
func (r *dispatchRig) session(t *testing.T) *contextGuardSession {
	t.Helper()
	r.manager.mu.Lock()
	s := r.manager.sessions["session"]
	r.manager.mu.Unlock()
	if s == nil {
		t.Fatal("session not registered")
	}
	return s
}

// TestContextGuardVetoesEvidenceQueuedDuringTheLaneWait is the double-compact
// gate: a provider compaction (native, or a manual /compact by a briefly
// attached human) whose item/started arrives while the dispatch waits in the
// lane MUST veto the dispatch -- the promotion was decided before the evidence
// existed. The guard then simply observes the provider's own compaction.
func TestContextGuardVetoesEvidenceQueuedDuringTheLaneWait(t *testing.T) {
	r := newDispatchRig(t)
	r.lane.enter() // a composer send holds the lane
	r.ingestUsage(t, 1, 85, 100)
	r.requireNoCallsFor(t, 100*time.Millisecond) // queued behind the lane
	r.ingestLifecycle(t, 2, "item/started")      // a compaction starts meanwhile
	r.lane.leave()
	r.waitPhase(t, contextguard.StateProviderCompacting)
	r.requireNoCallsFor(t, 150*time.Millisecond)
	r.ingestLifecycle(t, 3, "item/completed")
	r.waitPhase(t, contextguard.StateLatched)
	if calls := r.conn.snapshot(); len(calls) != 0 {
		t.Fatalf("the daemon dispatched into a provider compaction: %d calls", len(calls))
	}
}

// TestContextGuardQueuedDisableOutrunsTheDispatch: the owner turns the guard
// off while the dispatch waits in the lane. The disable must win.
func TestContextGuardQueuedDisableOutrunsTheDispatch(t *testing.T) {
	r := newDispatchRig(t)
	r.lane.enter()
	r.ingestUsage(t, 1, 85, 100)
	r.requireNoCallsFor(t, 100*time.Millisecond)
	r.manager.updateSettings(protocol.ContextGuardSettings{
		Revision: 2, AutoCompact: protocol.ContextGuardAutoCompact{Enabled: false, ThresholdPercent: 80},
	})
	r.lane.leave()
	r.waitPhase(t, contextguard.StateDisabled)
	r.requireNoCallsFor(t, 150*time.Millisecond)
}

// TestContextGuardRevisionMismatchRefusesAtTheWriteBoundary is the last line of
// defense for M1's race: a settings revision the worker has not reduced yet
// refuses the write with provably no bytes, then retries on a later edge.
func TestContextGuardRevisionMismatchRefusesAtTheWriteBoundary(t *testing.T) {
	r := newDispatchRig(t)
	r.lane.enter()
	r.ingestUsage(t, 1, 85, 100)
	r.requireNoCallsFor(t, 100*time.Millisecond)
	// Simulate the sliver where the revision is published but the config frame
	// is not yet visible: no queued evidence, only the atomic.
	r.session(t).settingsRevision.Store(999)
	r.lane.leave()
	calls := r.waitCalls(t, 1)
	r.waitPhase(t, contextguard.StatePendingIdle)
	if calls[0].wrote {
		t.Fatal("bytes crossed the boundary under a stale settings revision")
	}
}

// TestContextGuardConfirmationDeadlineBecomesAnHonestHold: the reply proved
// nothing and the provider's lifecycle completion never arrives (interrupted
// compaction, or a patch changed the item shape). Without the deadline the
// machine wedges in awaiting_confirmation forever -- and with the composer gate
// in place, would refuse the user's sends forever with it.
func TestContextGuardConfirmationDeadlineBecomesAnHonestHold(t *testing.T) {
	r := newDispatchRig(t)
	r.session(t).confirmTimeout = 50 * time.Millisecond // before any dispatch work
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.waitPhase(t, contextguard.StateAwaitingConfirmation)
	r.waitPhase(t, contextguard.StateOutcomeUnknownHold)
	if r.manager.compactionInFlight("session") {
		t.Fatal("a held guard still gates the composer")
	}
	// The hold is permanent: fresh pressure never resends.
	r.ingestUsage(t, 2, 95, 100)
	time.Sleep(100 * time.Millisecond)
	if calls := r.conn.snapshot(); len(calls) != 1 {
		t.Fatalf("provider calls after deadline hold = %d; want 1", len(calls))
	}
}

// TestContextGuardCompactionInFlightGate pins the effect-window predicate that
// composerSend and the supervisor consult: true from the durable executing
// record until confirmed/held/latched, false everywhere else.
func TestContextGuardCompactionInFlightGate(t *testing.T) {
	r := newDispatchRig(t)
	if r.manager.compactionInFlight("session") {
		t.Fatal("armed guard reports a compaction in flight")
	}
	gate := make(chan struct{})
	r.conn.script[0] = fakeDispatchBehavior{replyGate: gate}
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.waitPhase(t, contextguard.StateAwaitingConfirmation)
	if !r.manager.compactionInFlight("session") {
		t.Fatal("awaiting_confirmation must gate the composer: the compaction is running")
	}
	close(gate)
	r.ingestLifecycle(t, 2, "item/started")
	r.waitPhase(t, contextguard.StateProviderCompacting)
	if !r.manager.compactionInFlight("session") {
		t.Fatal("provider_compacting must gate the composer")
	}
	r.ingestLifecycle(t, 3, "item/completed")
	r.waitPhase(t, contextguard.StateLatched)
	if r.manager.compactionInFlight("session") {
		t.Fatal("latched guard still gates the composer")
	}
	if r.manager.compactionInFlight("unknown-session") {
		t.Fatal("an unregistered session reports a compaction in flight")
	}
}

// TestContextGuardRestoreFromExecutingSidecarHolds closes the crash window at
// the skeleton level: a daemon that died between the durable executing record
// and the outcome restores to outcome_unknown_hold and never resends (D5).
func TestContextGuardRestoreFromExecutingSidecarHolds(t *testing.T) {
	for _, state := range []contextguard.State{contextguard.StateExecuting, contextguard.StateAwaitingConfirmation} {
		manager, _ := contextGuardTestManager(t, true, 80)
		doc, err := json.Marshal(contextGuardStateDocument{
			SchemaVersion: contextGuardStateSchemaVersion, SessionInstance: "instance",
			SettingsRevision: 1, State: state, TriggerThreshold: 80,
		})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(manager.stateDir, "session")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, contextGuardStateFile), doc, 0o600); err != nil {
			t.Fatal(err)
		}
		source, _ := adapter.AsContextGuardSource(codex.New())
		action, _ := source.ContextGuardAction("0.151.0")
		conn := &fakeDispatchConn{script: map[int]fakeDispatchBehavior{}}
		lane := &composerLane{}
		lane.ready = sync.NewCond(&lane.mu)
		wire := func(s *contextGuardSession) {
			s.conn = conn
			s.lane = func() *composerLane { return lane }
			s.quiet = func() bool { return true }
		}
		if !manager.registerCurrentWired("session", contextGuardTestKey("instance", "epoch"), source, action, nil, wire) {
			t.Fatalf("registration failed for restored state %q", state)
		}
		view, ok := manager.view("session")
		if !ok || view.Phase != string(contextguard.StateOutcomeUnknownHold) {
			t.Fatalf("restored %q phase = %q; want outcome_unknown_hold", state, view.Phase)
		}
		manager.ingest("session", contextGuardTestKey("instance", "epoch"), 1, contextGuardUsageMethod, contextGuardUsageFrame(95, 95, 100), time.Now())
		time.Sleep(100 * time.Millisecond)
		if calls := conn.snapshot(); len(calls) != 0 {
			t.Fatalf("restored %q resent the compaction: %d calls", state, len(calls))
		}
		manager.close()
	}
}

// TestContextGuardStopWhileQueuedHandsTheTicketBack: a session closed while its
// dispatch waits in the lane returns promptly, and the eventual ticket is
// handed straight back -- no stuck lane, no leaked slot.
func TestContextGuardStopWhileQueuedHandsTheTicketBack(t *testing.T) {
	r := newDispatchRig(t)
	r.lane.enter()
	r.ingestUsage(t, 1, 85, 100)
	r.requireNoCallsFor(t, 100*time.Millisecond) // worker parked in the lane wait
	closed := make(chan struct{})
	go func() {
		r.manager.stopSession("session")
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("stopSession waited behind a busy composer lane")
	}
	r.lane.leave()
	// The orphaned ticket must pass through; a fresh entrant then proceeds.
	reentered := make(chan struct{})
	go func() {
		r.lane.enter()
		r.lane.leave()
		close(reentered)
	}()
	select {
	case <-reentered:
	case <-time.After(2 * time.Second):
		t.Fatal("the stopped worker's lane ticket was never handed back")
	}
	if calls := r.conn.snapshot(); len(calls) != 0 {
		t.Fatalf("a stopped session dispatched anyway: %d calls", len(calls))
	}
}

// TestContextGuardCloseDuringReplyWaitReturnsPromptly: an alive-but-unresponsive
// provider must not hold close() -- and everything serialized behind it -- for
// the full dispatch timeout. The canceled call after the write is an unknown
// outcome, exactly what crash recovery would conclude.
func TestContextGuardCloseDuringReplyWaitReturnsPromptly(t *testing.T) {
	r := newDispatchRig(t)
	gate := make(chan struct{})
	defer close(gate)
	r.conn.script[0] = fakeDispatchBehavior{replyGate: gate}
	r.ingestUsage(t, 1, 85, 100)
	r.waitPhase(t, contextguard.StateAwaitingConfirmation) // parked in the reply wait
	closed := make(chan struct{})
	go func() {
		r.manager.stopSession("session")
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("close waited for the provider reply; the dispatch context must die with the worker")
	}
}

// TestContextGuardLossRetainsTheQueuedSettingsEdge: D4 says settings edges may
// not disappear. An overflow loss discards evidence (that is what the hold
// records) but the queued config still advances the machine's revision.
func TestContextGuardLossRetainsTheQueuedSettingsEdge(t *testing.T) {
	r := newDispatchRig(t)
	s := r.session(t)
	// Queue a config edge, then a loss, without letting the worker drain between:
	// both are visible in one drain, the loss first.
	s.queueMu.Lock()
	s.nextQueueOrder++
	cfg := contextguard.Config{Enabled: false, Threshold: 70, Revision: 2}
	s.latestConfig = &contextGuardPending{order: s.nextQueueOrder, at: time.Now(), config: &cfg}
	s.settingsRevision.Store(2)
	s.lost = true
	s.lossSequence = 9
	s.lossAt = time.Now()
	s.queueMu.Unlock()
	s.signal()
	r.waitPhase(t, contextguard.StateEventLossHold)
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.stateMu.Lock()
		revision := s.machine.Config.Revision
		s.stateMu.Unlock()
		if revision == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("machine revision = %d after loss; the settings edge disappeared (D4)", revision)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestContextGuardStopBarrierVetoesAtTheQueueHead: a Stop admitted while the
// dispatch waited in the lane changed the world the promotion was decided in;
// the stale ticket degrades to pending_idle and a later edge retries.
func TestContextGuardStopBarrierVetoesAtTheQueueHead(t *testing.T) {
	r := newDispatchRig(t)
	r.lane.enter()
	r.ingestUsage(t, 1, 85, 100)
	r.requireNoCallsFor(t, 100*time.Millisecond) // queued; its admitted barrier is captured
	r.lane.mu.Lock()
	r.lane.barrier++ // interruptTurn's Stop discipline, emulated directly
	r.lane.mu.Unlock()
	r.lane.leave()
	r.waitPhase(t, contextguard.StatePendingIdle)
	r.requireNoCallsFor(t, 150*time.Millisecond)
	// A later quiet edge retries against the post-Stop world.
	r.nudge()
	r.waitCalls(t, 1)
}

// TestContextGuardStalledReplyStillLatchesOnQueuedCompletion (audit finding):
// the reply fails after a stall while the compaction's own lifecycle events
// already sit in the queue. The definitive completion must latch -- draining
// runs before the hold decision -- never be discarded into a false unknown.
func TestContextGuardStalledReplyStillLatchesOnQueuedCompletion(t *testing.T) {
	r := newDispatchRig(t)
	gate := make(chan struct{})
	r.conn.script[0] = fakeDispatchBehavior{replyGate: gate, replyErr: errors.New("timeout waiting for reply")}
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.waitPhase(t, contextguard.StateAwaitingConfirmation)
	// The compaction runs and completes while the reply is stalled; the worker
	// is parked in the reply wait, so both frames queue.
	r.ingestLifecycle(t, 2, "item/started")
	r.ingestLifecycle(t, 3, "item/completed")
	close(gate) // now the reply comes back as an error
	r.waitPhase(t, contextguard.StateLatched)
	// The internal drain may be this cycle's last activity: the continuation
	// must still be enqueued, not forfeited waiting for a wake that never comes.
	r.waitPlainCalls(t, 1)
	// Latched, not held: below the re-arm gap the guard re-arms normally.
	r.ingestUsage(t, 4, 60, 100)
	r.waitPhase(t, contextguard.StateArmed)
}

func (r *dispatchRig) setAttended(v bool) { r.mu.Lock(); r.attendedNow = v; r.mu.Unlock() }

func (r *dispatchRig) waitPlainCalls(t *testing.T, want int) []fakeDispatchCall {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		calls := r.conn.plainSnapshot()
		if len(calls) >= want {
			return calls
		}
		if time.Now().After(deadline) {
			t.Fatalf("plain calls = %d; want %d", len(calls), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *dispatchRig) requireNoPlainCallsFor(t *testing.T, d time.Duration) {
	t.Helper()
	time.Sleep(d)
	if calls := r.conn.plainSnapshot(); len(calls) != 0 {
		t.Fatalf("plain calls = %d; want none", len(calls))
	}
}

// TestContextGuardContinuationStartsTheResumptionTurn (ADR-023 amendment 2):
// the continuation is an ordinary turn/start, sent ONLY once the guard's own
// compaction has verifiably completed -- never while it runs (that would
// cancel it) -- exactly once per cycle, with the daemon-authored prompt; a
// second cycle earns a second continuation.
func TestContextGuardContinuationStartsTheResumptionTurn(t *testing.T) {
	r := newDispatchRig(t)
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.ingestLifecycle(t, 2, "item/started")
	r.waitPhase(t, contextguard.StateProviderCompacting)
	// While the compaction runs, NOTHING may be sent: a turn/start here would
	// cancel it, and a queued message could not be revoked from a human.
	r.requireNoPlainCallsFor(t, 150*time.Millisecond)
	r.ingestLifecycle(t, 3, "item/completed")
	r.waitPhase(t, contextguard.StateLatched)
	plain := r.waitPlainCalls(t, 1)
	if plain[0].method != "turn/start" {
		t.Fatalf("continuation method = %q; want turn/start", plain[0].method)
	}
	if plain[0].params["threadId"] != contextGuardTestThread {
		t.Fatalf("continuation thread = %v; want the session's thread", plain[0].params["threadId"])
	}
	input, _ := plain[0].params["input"].([]map[string]any)
	if len(input) != 1 || input[0]["text"] != contextGuardContinuationPrompt {
		t.Fatalf("continuation input = %+v; want the daemon-authored prompt", plain[0].params["input"])
	}
	// The consumed cycle never sends again.
	time.Sleep(100 * time.Millisecond)
	if calls := r.conn.plainSnapshot(); len(calls) != 1 {
		t.Fatalf("continuations after latch = %d; want 1", len(calls))
	}
	// A full second cycle earns its own continuation.
	r.ingestUsage(t, 4, 60, 100)
	r.waitPhase(t, contextguard.StateArmed)
	r.ingestUsage(t, 5, 90, 100)
	r.waitCalls(t, 2)
	r.ingestLifecycle(t, 6, "item/started")
	r.ingestLifecycle(t, 7, "item/completed")
	r.waitPlainCalls(t, 2)
}

// TestContextGuardContinuationWaitsOutFoldLagThenSends: at latch the folded
// status may still trail the compaction turn's end. Latched is stable and
// status edges wake the worker, so the armed attempt waits for quiet instead
// of forfeiting -- and fires on the settling edge.
func TestContextGuardContinuationWaitsOutFoldLagThenSends(t *testing.T) {
	r := newDispatchRig(t)
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.setQuiet(false) // the fold has not settled to idle yet
	r.ingestLifecycle(t, 2, "item/started")
	r.ingestLifecycle(t, 3, "item/completed")
	r.waitPhase(t, contextguard.StateLatched)
	r.requireNoPlainCallsFor(t, 150*time.Millisecond)
	r.setQuiet(true)
	r.nudge() // the status edge
	r.waitPlainCalls(t, 1)
}

// TestContextGuardContinuationForfeitsOnStopBarrierAtTheLaneHead: a Stop
// admitted while the continuation waited for the lane changed the world; the
// send forfeits at the head, one-shot, no retry.
func TestContextGuardContinuationForfeitsOnStopBarrierAtTheLaneHead(t *testing.T) {
	r := newDispatchRig(t)
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.lane.enter() // a composer holds the lane through the latch
	r.ingestLifecycle(t, 2, "item/started")
	r.ingestLifecycle(t, 3, "item/completed")
	r.waitPhase(t, contextguard.StateLatched)
	time.Sleep(100 * time.Millisecond) // the worker is parked in the lane wait
	r.lane.mu.Lock()
	r.lane.barrier++
	r.lane.mu.Unlock()
	r.lane.leave()
	r.requireNoPlainCallsFor(t, 200*time.Millisecond)
	r.nudge()
	r.requireNoPlainCallsFor(t, 150*time.Millisecond) // consumed, never retried
}

// TestContextGuardContinuationAtLatchWhenCompletionArrivesFirst: a compaction
// so fast its completion is the first evidence seen still gets its
// continuation -- enqueued at latch, where the idle thread starts it directly.
func TestContextGuardContinuationAtLatchWhenCompletionArrivesFirst(t *testing.T) {
	r := newDispatchRig(t)
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.ingestLifecycle(t, 2, "item/completed") // conclusive, straight from awaiting
	r.waitPhase(t, contextguard.StateLatched)
	r.waitPlainCalls(t, 1)
}

// TestContextGuardNeverContinuesACompactionItDidNotDispatch: a native or
// manual compaction is somebody else's flow; the guard observes and latches
// but enqueues nothing.
func TestContextGuardNeverContinuesACompactionItDidNotDispatch(t *testing.T) {
	r := newDispatchRig(t)
	r.ingestUsage(t, 1, 50, 100) // below threshold: no dispatch, just armed
	r.ingestLifecycle(t, 2, "item/started")
	r.waitPhase(t, contextguard.StateProviderCompacting)
	r.ingestLifecycle(t, 3, "item/completed")
	r.waitPhase(t, contextguard.StateLatched)
	r.requireNoPlainCallsFor(t, 150*time.Millisecond)
}

// TestContextGuardContinuationSkipsAttendedSessions: someone took the
// controls during the compaction; they continue the task themselves. The
// forfeit is consumed -- their later departure earns no surprise turn.
func TestContextGuardContinuationSkipsAttendedSessions(t *testing.T) {
	r := newDispatchRig(t)
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.setAttended(true)
	r.ingestLifecycle(t, 2, "item/started")
	r.ingestLifecycle(t, 3, "item/completed")
	r.waitPhase(t, contextguard.StateLatched)
	r.requireNoPlainCallsFor(t, 150*time.Millisecond)
	r.setAttended(false)
	r.nudge()
	r.requireNoPlainCallsFor(t, 150*time.Millisecond) // consumed, not deferred
}

// TestContextGuardContinuationFailureIsForfeitNotRetry: a send error leaves
// the lifecycle untouched and is never retried -- ambiguity would risk a
// duplicated surprise turn.
func TestContextGuardContinuationFailureIsForfeitNotRetry(t *testing.T) {
	r := newDispatchRig(t)
	r.conn.mu.Lock()
	r.conn.plainErr = errors.New("turn refused")
	r.conn.mu.Unlock()
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.ingestLifecycle(t, 2, "item/started")
	r.ingestLifecycle(t, 3, "item/completed")
	r.waitPhase(t, contextguard.StateLatched) // lifecycle unharmed
	r.waitPlainCalls(t, 1)                    // attempted once
	r.nudge()
	time.Sleep(100 * time.Millisecond)
	if calls := r.conn.plainSnapshot(); len(calls) != 1 {
		t.Fatalf("send attempts = %d; want exactly 1 (forfeit, never retry)", len(calls))
	}
}

// TestContextGuardContinuationForfeitsOnHold: a compaction whose outcome went
// ambiguous holds -- and takes its continuation with it.
func TestContextGuardContinuationForfeitsOnHold(t *testing.T) {
	r := newDispatchRig(t)
	r.conn.script[0] = fakeDispatchBehavior{replyErr: errors.New("timeout waiting for reply")}
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.waitPhase(t, contextguard.StateOutcomeUnknownHold)
	r.nudge()
	r.requireNoPlainCallsFor(t, 150*time.Millisecond)
}

// TestContextGuardReconcileWindowKeepsTheFeedAlive: a backend that registers
// before the engine is published (the reconcile window) must NOT deafen its
// feed -- discard is one-way, and startContextGuardsForRunning re-registers the
// same feed once the core exists. Frames captured in between stay buffered.
func TestContextGuardReconcileWindowKeepsTheFeedAlive(t *testing.T) {
	manager, _ := contextGuardTestManager(t, true, 80)
	d := &Daemon{contextGuards: manager}
	feed := &backendFeed{epoch: "epoch"}
	backend := &sessionBackend{threadID: contextGuardTestThread, sessionInstance: "instance", feed: feed}
	d.registerContextGuardBackend("session", backend) // d.core == nil: the window
	feed.guardMu.Lock()
	discarded := feed.guardDiscarded
	feed.guardMu.Unlock()
	if discarded {
		t.Fatal("the reconcile window permanently deafened the guard feed")
	}
	d.captureContextGuardFrame("session", "instance", feed, contextGuardUsageMethod, contextGuardUsageFrame(85, 85, 100), time.Now())
	feed.guardMu.Lock()
	buffered := feed.guardLatestUsage != nil
	feed.guardMu.Unlock()
	if !buffered {
		t.Fatal("window frames were dropped instead of buffered")
	}
}

func TestContextGuardUnwiredRegistrationsStayObserveOnly(t *testing.T) {
	// Every register() caller without the backend wiring -- and every disabled
	// or pre-dispatch test -- keeps the exact old behavior: pressure observes
	// and latches, nothing dispatches.
	manager, _ := contextGuardTestManager(t, true, 80)
	source, _ := adapter.AsContextGuardSource(codex.New())
	action, _ := source.ContextGuardAction("0.151.0")
	key := contextGuardTestKey("instance", "epoch")
	if !manager.register("session", key, source, action) {
		t.Fatal("registration failed")
	}
	manager.ingest("session", key, 1, contextGuardUsageMethod, contextGuardUsageFrame(85, 85, 100), time.Now())
	deadline := time.Now().Add(time.Second)
	for {
		view, ok := manager.view("session")
		if ok && view.Phase == string(contextguard.StatePendingIdle) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("phase = %q; want pending_idle", view.Phase)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestContextGuardContinuationForfeitsWhenTheWindowPasses: a continuation
// that could not go out shortly after the compaction (persistent activity,
// fold never settling) is stale context maintenance, not a message to
// deliver later.
func TestContextGuardContinuationForfeitsWhenTheWindowPasses(t *testing.T) {
	r := newDispatchRig(t)
	r.session(t).continuationFreshness = 50 * time.Millisecond
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.setQuiet(false) // the fold never settles inside the window
	r.ingestLifecycle(t, 2, "item/started")
	r.ingestLifecycle(t, 3, "item/completed")
	r.waitPhase(t, contextguard.StateLatched)
	time.Sleep(120 * time.Millisecond)
	r.setQuiet(true)
	r.nudge()
	r.requireNoPlainCallsFor(t, 150*time.Millisecond)
}
