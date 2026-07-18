// Package idempotency is the daemon's two-phase durable idempotency store (ADR-007
// D6, plan R-IDP, amendment D.0-A3): a crash-safe `prepared -> executing ->
// completed/failed` request record keyed by operation_id, fsync'd BEFORE the side
// effect, so a duplicate request replays the cached outcome and executes nothing,
// and a crash between the side effect and the commit never double-executes.
// `interrupt` is at-most-once: a mid-op crash resolves to a terminal
// `outcome_unknown`, never a claimed exactly-once.
//
// These are FAILING-FIRST white-box tests (package idempotency). NO implementation
// exists: `go test ./internal/idempotency/` fails to COMPILE (undefined symbols) —
// the GG-5 RED. No production code/stubs here.
//
// FROZEN API these tests expect:
//
//	type Phase string
//	const ( PhasePrepared, PhaseExecuting, PhaseCompleted, PhaseFailed, PhaseOutcomeUnknown Phase )
//	const MaxOperationIDLen = 128
//	type Record struct {
//	    OperationID string; Action string; SessionID string; Phase Phase
//	    Outcome json.RawMessage; CreatedAt, UpdatedAt time.Time
//	}
//	type Options struct { TTL time.Duration; MaxEntries int; Clock func() time.Time }
//	type Store struct{ ... }
//	func Open(dir string) (*Store, error)
//	func OpenWithOptions(dir string, opts Options) (*Store, error)
//	// Prepare durably records `prepared` (fsync) BEFORE any side effect. If a record
//	// for operationID already exists it is returned with existed=true (the replay
//	// path): the caller MUST execute nothing and return the cached outcome.
//	func (s *Store) Prepare(operationID, action, sessionID string) (rec Record, existed bool, err error)
//	func (s *Store) Begin(operationID string) error                 // prepared -> executing (fsync), immediately before the side effect
//	func (s *Store) Complete(operationID string, outcome []byte) error // -> completed (fsync), after the side effect commits
//	func (s *Store) Fail(operationID string, outcome []byte) error     // -> failed (fsync)
//	// ResolveOutcomeUnknown terminates an unresolved at-most-once op (interrupt) whose
//	// side effect cannot be verified after a crash (A3).
//	func (s *Store) ResolveOutcomeUnknown(operationID string) error
//	func (s *Store) Get(operationID string) (Record, bool)
//	func (s *Store) Compact() error                                 // drops TTL-expired / over-cap records (R-IDP.4)
//
// Clocks are injected; no real sleeps for TTL.
package idempotency

import (
	"os"
	"testing"
	"time"
)

// sdir returns a fresh, auto-cleaned store directory.
func sdir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "idmp")
	if err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// runOp models one operation's side effect guarded by the two-phase store: Prepare
// (replay short-circuits), Begin, side effect (count++), Complete. It returns
// whether the side effect actually ran, so a test can assert the exactly-once count.
func runOp(t *testing.T, s *Store, op, action, sess string, sideEffect func()) (ran bool) {
	t.Helper()
	rec, existed, err := s.Prepare(op, action, sess)
	if err != nil {
		t.Fatalf("Prepare(%s): %v", op, err)
	}
	if existed {
		// Replay: return the cached outcome, execute NOTHING (R-IDP.3).
		if rec.Phase != PhaseCompleted && rec.Phase != PhaseFailed && rec.Phase != PhaseOutcomeUnknown &&
			rec.Phase != PhaseExecuting && rec.Phase != PhasePrepared {
			t.Fatalf("replayed record has unknown phase %q", rec.Phase)
		}
		return false
	}
	if err := s.Begin(op); err != nil {
		t.Fatalf("Begin(%s): %v", op, err)
	}
	sideEffect()
	if err := s.Complete(op, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("Complete(%s): %v", op, err)
	}
	return true
}

// ---------------------------------------------------------------------------
// R-IDP.2/.3 — two-phase record; replay executes nothing; survives restart.
// ---------------------------------------------------------------------------

// TestIdempotency_ReplayKillNoSecondSignal asserts a duplicate operation_id returns
// the cached outcome and runs the side effect exactly once (side-effect count == 1).
func TestIdempotency_ReplayKillNoSecondSignal(t *testing.T) {
	s, err := Open(sdir(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	signals := 0
	const op = "devA:01JREPLAYKILL"
	if !runOp(t, s, op, "kill", "sess1", func() { signals++ }) {
		t.Fatalf("first op did not run its side effect")
	}
	if ran := runOp(t, s, op, "kill", "sess1", func() { signals++ }); ran {
		t.Fatalf("replayed op ran the side effect again")
	}
	if signals != 1 {
		t.Fatalf("kill side-effect count = %d; want exactly 1 (idempotent replay)", signals)
	}
}

// TestIdempotency_OutcomeSurvivesRestart asserts a completed record — and its cached
// outcome — survives a Close/reopen, so a reconnect after a daemon restart replays
// the outcome rather than re-executing.
func TestIdempotency_OutcomeSurvivesRestart(t *testing.T) {
	dir := sdir(t)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const op = "devA:01JRESTART"
	if _, _, err := s.Prepare(op, "launch", "sess1"); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := s.Begin(op); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := s.Complete(op, []byte(`{"session":"sess1"}`)); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rec, ok := s2.Get(op)
	if !ok {
		t.Fatalf("record %q did not survive restart", op)
	}
	if rec.Phase != PhaseCompleted {
		t.Fatalf("restored phase = %q; want completed", rec.Phase)
	}
	if len(rec.Outcome) == 0 {
		t.Fatalf("restored record lost its cached outcome; a replay could not return it")
	}
	// A replay after restart must short-circuit (existed=true), never re-Prepare-new.
	if _, existed, err := s2.Prepare(op, "launch", "sess1"); err != nil || !existed {
		t.Fatalf("post-restart Prepare(%q): existed=%v err=%v; want existed=true", op, existed, err)
	}
}

// TestIdempotency_CrashBetweenExecuteAndCommitNoDoubleExecute is the A3 CRITICAL
// property: the two-phase record is fsync'd BEFORE the side effect, so a crash
// AFTER the side effect but BEFORE the completion commit leaves an `executing`
// record — and recovery resolves it WITHOUT a second execution (side-effect count
// == 1). Modelled by dropping the store handle mid-op (no Complete) and reopening.
func TestIdempotency_CrashBetweenExecuteAndCommitNoDoubleExecute(t *testing.T) {
	dir := sdir(t)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const op = "devA:01JCRASHKILL"
	effects := 0

	// Phase up to executing (both fsync'd), run the side effect, then CRASH before
	// Complete — the record is durably `executing` with no cached outcome.
	if _, existed, err := s.Prepare(op, "kill", "sess1"); err != nil || existed {
		t.Fatalf("Prepare: existed=%v err=%v", existed, err)
	}
	if err := s.Begin(op); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	effects++ // the side effect landed
	// no Complete: simulate kill -9 by dropping the handle.
	s = nil

	// Recovery: a fresh store sees the unresolved `executing` record. A replay MUST
	// NOT run the side effect again — Prepare reports the in-flight record as existing.
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after crash: %v", err)
	}
	rec, ok := s2.Get(op)
	if !ok {
		t.Fatalf("in-flight record did not survive the crash; the record must be fsync'd BEFORE the side effect")
	}
	if rec.Phase != PhaseExecuting {
		t.Fatalf("recovered phase = %q; want executing (fsync'd before the side effect, no completion)", rec.Phase)
	}
	if _, existed, err := s2.Prepare(op, "kill", "sess1"); err != nil {
		t.Fatalf("recovery Prepare: %v", err)
	} else if !existed {
		effects++ // WRONG: a fresh record would let the caller re-execute
	}
	if effects != 1 {
		t.Fatalf("side-effect count = %d after crash+recovery; want exactly 1 (no double execute)", effects)
	}
}

// ---------------------------------------------------------------------------
// A3 — interrupt is at-most-once: a mid-op crash resolves to outcome-unknown.
// ---------------------------------------------------------------------------

// TestIdempotency_InterruptOutcomeUnknownOnCrash asserts SIGINT delivery is not
// verifiable from terminal state, so an interrupt whose record is `executing` after
// a crash resolves to a terminal outcome_unknown the phone surfaces — never a
// claimed exactly-once completion.
func TestIdempotency_InterruptOutcomeUnknownOnCrash(t *testing.T) {
	dir := sdir(t)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const op = "devA:01JINTERRUPT"
	if _, _, err := s.Prepare(op, "interrupt", "sess1"); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := s.Begin(op); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// Crash mid-interrupt: drop the handle with the record in `executing`.
	s = nil

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rec, ok := s2.Get(op)
	if !ok || rec.Phase != PhaseExecuting {
		t.Fatalf("interrupt record not recovered as executing: ok=%v phase=%q", ok, rec.Phase)
	}
	// Recovery resolves an unverifiable interrupt to outcome_unknown, NOT completed.
	if err := s2.ResolveOutcomeUnknown(op); err != nil {
		t.Fatalf("ResolveOutcomeUnknown: %v", err)
	}
	rec, _ = s2.Get(op)
	if rec.Phase != PhaseOutcomeUnknown {
		t.Fatalf("resolved interrupt phase = %q; want outcome_unknown (at-most-once, never a claimed exactly-once)", rec.Phase)
	}
	if rec.Phase == PhaseCompleted {
		t.Fatalf("interrupt was claimed completed after a mid-op crash; SIGINT delivery is not verifiable")
	}
}

// ---------------------------------------------------------------------------
// R-IDP.1/.2 — operation_id is a bounded opaque key.
// ---------------------------------------------------------------------------

// TestIdempotency_OperationIDBounded asserts the key contract: an empty key is
// refused, and a key over MaxOperationIDLen bytes is refused (opaque, <=128 bytes).
func TestIdempotency_OperationIDBounded(t *testing.T) {
	s, err := Open(sdir(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, _, err := s.Prepare("", "kill", "sess1"); err == nil {
		t.Fatalf("Prepare accepted an empty operation_id; want a refusal")
	}
	oversize := make([]byte, MaxOperationIDLen+1)
	for i := range oversize {
		oversize[i] = 'a'
	}
	if _, _, err := s.Prepare(string(oversize), "kill", "sess1"); err == nil {
		t.Fatalf("Prepare accepted an operation_id of %d bytes; want a refusal (<=%d)", len(oversize), MaxOperationIDLen)
	}
}

// ---------------------------------------------------------------------------
// R-IDP.4 — retention (TTL) compaction, mock clock.
// ---------------------------------------------------------------------------

// TestIdempotency_TTLCompaction asserts Compact drops records past the TTL under an
// injected clock (no real sleep), while an in-window record is retained.
func TestIdempotency_TTLCompaction(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	now := base
	s, err := OpenWithOptions(sdir(t), Options{TTL: time.Hour, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	const old, fresh = "devA:01JOLD", "devA:01JFRESH"

	now = base
	if _, _, err := s.Prepare(old, "kill", "s1"); err != nil {
		t.Fatalf("Prepare old: %v", err)
	}
	if err := s.Complete(old, nil); err != nil {
		t.Fatalf("Complete old: %v", err)
	}

	// Advance beyond the TTL, then add a fresh record and compact.
	now = base.Add(2 * time.Hour)
	if _, _, err := s.Prepare(fresh, "kill", "s2"); err != nil {
		t.Fatalf("Prepare fresh: %v", err)
	}
	if err := s.Complete(fresh, nil); err != nil {
		t.Fatalf("Complete fresh: %v", err)
	}
	if err := s.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if _, ok := s.Get(old); ok {
		t.Fatalf("TTL-expired record %q survived Compact", old)
	}
	if _, ok := s.Get(fresh); !ok {
		t.Fatalf("in-window record %q was dropped by Compact", fresh)
	}
}
