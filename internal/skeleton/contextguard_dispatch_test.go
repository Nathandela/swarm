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
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/contextguard"
)

// fakeDispatchConn honors the CallAtWriteBoundary contract exactly: a
// beforeWrite error aborts with provably no bytes and no afterWrite; otherwise
// afterWrite runs after the write attempt, then the scripted reply decides.
type fakeDispatchConn struct {
	mu    sync.Mutex
	calls []fakeDispatchCall
	// script[i] shapes call i; a missing entry is the happy path.
	script map[int]fakeDispatchBehavior
}

type fakeDispatchBehavior struct {
	refuseBeforeCallbacks bool  // the closed-conn shape: error before beforeWrite runs
	replyErr              error // returned AFTER the write (afterWrite has run)
}

type fakeDispatchCall struct {
	method string
	params map[string]any
	wrote  bool
}

var errFakeConnClosed = errors.New("fake conn closed")

func (f *fakeDispatchConn) CallAtWriteBoundary(_ context.Context, method string, params, _ any, beforeWrite func() error, afterWrite func()) error {
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

	mu        sync.Mutex
	quietNow  bool
	uncertain bool
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
