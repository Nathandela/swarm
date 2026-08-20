package engine

// FAILING-FIRST (TDD RED, GG-5) for Mirror M4.5's ENGINE SEAM: a direct
// Engine.ApplyTypedEvent, so the Codex typed-status producer does not have to authenticate to
// the daemon it lives in. Bead: agents-tracker-hggx.8. ADR-013 §R7.3's last section.
//
// WHY NOT HandleCallback, and the retraction that goes with it. The first draft routed M4.5's
// typed events through HandleCallback and justified the reuse with a sequence-namespace claim
// that INVERTS the actual property: sharing one counter between two allocators is safe only
// because hookclient.nextSequence takes LOCK_EX (hookclient.go:116-128), not because the file
// is the same. And getting it wrong is a SILENT DROP, not a warning -- hookSeqDuplicate
// discards the callback and ingestHookBytes errors (skeleton/hookdrain.go:73-81) -- while
// markHookSeqIngested FSYNCS a durable seen-set PER CALLBACK (hookdrain.go:289-315), which is
// tolerable at turn-lifecycle rates and fatal if item frames ever reach it.
//
// More basically: the daemon does not need to authenticate to itself. The token check, the
// durable replay set and the on-disk counter buy nothing an in-process producer does not
// already have.
//
// THE CONTRACT:
//
//	func (e *Engine) ApplyTypedEvent(sessionID, event string, payload map[string]string) error
//
// It performs exactly what HandleCallback does AFTER the token check -- deriveDims ->
// withChildrenHoldingTheTurn -> withoutPostStopReactivation -> applyTyped -> commit -> emit --
// with the sequence drawn from a per-session IN-MEMORY monotonic counter the engine allocates
// under e.mu. applyTyped's per-dimension high-water is RETAINED (it is what rejects a stale
// reorder and is real value); the fsync, the token and the durable seen-set are not. Frames
// arrive in order on ONE WebSocket connection, which is what makes an in-memory counter
// sufficient.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/status"
)

// TestR7ApplyTypedEvent_DrivesCodexStatusFromTheAdaptersOwnDeclaredMapping is M4.5's headline
// and the payment of the D1 debt this repo has carried since Epic 14: codex.go's own header
// records that "the live app-server typed-event producer is deferred ... the typed mapping
// here is fixture-proven pending that live wiring". The MAPPING was always right. Nothing ever
// called it.
func TestR7ApplyTypedEvent_DrivesCodexStatusFromTheAdaptersOwnDeclaredMapping(t *testing.T) {
	r := r7NewCodexSession(t)

	if err := r.apply(t, "turn/started"); err != nil {
		t.Fatalf("ApplyTypedEvent(turn/started): %v", err)
	}
	if got := r.turn(t); got != status.TurnActive {
		t.Errorf("after turn/started the turn is %q, want %q -- the row the codex adapter has "+
			"declared since Epic 11 (codex.go:42)", got, status.TurnActive)
	}

	if err := r.apply(t, "turn/completed"); err != nil {
		t.Fatalf("ApplyTypedEvent(turn/completed): %v", err)
	}
	if got := r.turn(t); got != status.TurnIdle {
		t.Errorf("after turn/completed the turn is %q, want %q", got, status.TurnIdle)
	}
}

// TestR7ApplyTypedEvent_AnApprovalRequestRaisesPermissionAndResolvedClearsIt is the pair. The
// second row is new and it matters: without serverRequest/resolved, `permission` sticks until
// turn/completed, so a session the OWNER approved at the terminal shows an awaiting-input badge
// on the phone while it is working.
func TestR7ApplyTypedEvent_AnApprovalRequestRaisesPermissionAndResolvedClearsIt(t *testing.T) {
	r := r7NewCodexSession(t)

	if err := r.apply(t, "item/fileChange/requestApproval"); err != nil {
		t.Fatalf("ApplyTypedEvent(requestApproval): %v", err)
	}
	if got := r.interaction(t); got != status.InteractionPermission {
		t.Errorf("after item/fileChange/requestApproval the interaction is %q, want %q. This is the "+
			"approval the R1 gate actually CAPTURED (approval-request.json) while the adapter "+
			"declares only the commandExecution sibling", got, status.InteractionPermission)
	}

	if err := r.apply(t, "serverRequest/resolved"); err != nil {
		t.Fatalf("ApplyTypedEvent(serverRequest/resolved): %v", err)
	}
	if got := r.interaction(t); got != status.InteractionNone {
		t.Errorf("after serverRequest/resolved the interaction is %q, want %q", got, status.InteractionNone)
	}
}

// TestR7ApplyTypedEvent_RequiresNoTokenAndNoDurableSequence is the seam's whole justification.
// A backend session is registered with NO hook token, so HandleCallback is structurally
// unusable for it (engine.go:273-275 refuses an empty token); ApplyTypedEvent must therefore
// need neither.
func TestR7ApplyTypedEvent_RequiresNoTokenAndNoDurableSequence(t *testing.T) {
	r := r7NewCodexSessionWithToken(t, "")

	if err := r.apply(t, "turn/started"); err != nil {
		t.Fatalf("ApplyTypedEvent on a session with NO hook token: %v", err)
	}
	if got := r.turn(t); got != status.TurnActive {
		t.Fatalf("the typed event did not apply: turn is %q", got)
	}

	// And the old path still refuses, which is what makes single-writer ENFORCED rather than
	// assumed: a backend session cannot have two typed producers competing for one high-water
	// namespace.
	err := r.e.HandleCallback(Callback{SessionID: r.id, Event: "turn/started", Sequence: 1})
	if err == nil {
		t.Error("HandleCallback ACCEPTED a tokenless callback for a backend session; the two typed " +
			"producers must be mutually exclusive (§R7.3's named mutation fence: mint a hook token " +
			"for a backend session and this must fail)")
	}
}

// TestR7ApplyTypedEvent_TheInMemorySequenceIsMonotonicSoNothingIsRejectedAsAReplay. The
// per-dimension high-water in applyTyped is RETAINED, so a producer that reused a sequence
// would have its second event silently rejected -- the drop shape §R7.3 refuses.
func TestR7ApplyTypedEvent_TheInMemorySequenceIsMonotonicSoNothingIsRejectedAsAReplay(t *testing.T) {
	r := r7NewCodexSession(t)

	for i, ev := range []string{"turn/started", "turn/completed", "turn/started", "turn/completed"} {
		if err := r.apply(t, ev); err != nil {
			t.Fatalf("event %d (%s) was rejected: %v; a repeated sequence makes every second frame "+
				"a silent no-op and the phone's status freezes mid-turn", i, ev, err)
		}
	}
	if got := r.turn(t); got != status.TurnIdle {
		t.Errorf("after four alternating events the turn is %q, want idle", got)
	}
}

// TestR7ApplyTypedEvent_AnUnmappedEventIsABenignNoOp mirrors HandleCallback's own posture: an
// event the adapter maps to nothing is accepted and changes nothing, so the grid heuristic
// still governs. This is what makes a Codex CLI upgrade safe -- an unrecognized method degrades
// rather than erroring into a log nobody reads.
func TestR7ApplyTypedEvent_AnUnmappedEventIsABenignNoOp(t *testing.T) {
	r := r7NewCodexSession(t)
	if err := r.apply(t, "thread/tokenUsage/updated"); err != nil {
		t.Errorf("an unmapped event errored: %v; it must be a benign no-op", err)
	}
	if err := r.apply(t, "some/future/method"); err != nil {
		t.Errorf("an unrecognized method errored: %v", err)
	}
}

// TestR7ApplyTypedEvent_AnUnregisteredSessionIsRejected keeps the seam from being a way to
// invent status for a session the engine does not run.
func TestR7ApplyTypedEvent_AnUnregisteredSessionIsRejected(t *testing.T) {
	r := r7NewCodexSession(t)
	if err := r.e.ApplyTypedEvent("no-such-session", "turn/started", nil); err == nil {
		t.Error("ApplyTypedEvent accepted an event for an unregistered session")
	}
}

// ---------------------------------------------------------------------------
// rig
// ---------------------------------------------------------------------------

// r7CodexRig registers a session whose SignalSources are the REAL codex adapter's, so the
// mapping under test is the shipped declaration and not a double. Status is read off the
// injected Emit recorder, which is how every other test in this package reads it.
type r7CodexRig struct {
	e   *Engine
	rec *emitRecorder
	id  string
}

func r7NewCodexSession(t *testing.T) *r7CodexRig {
	t.Helper()
	return r7NewCodexSessionWithToken(t, "")
}

func r7NewCodexSessionWithToken(t *testing.T, token string) *r7CodexRig {
	t.Helper()
	rec := &emitRecorder{}
	e := newEngine(newClock(), constCPU(0), rec, time.Minute, time.Hour)
	const id = "01JCODEXSESSION"
	e.RegisterSession(id, token, 0, codex.New().SignalSources())
	t.Cleanup(func() { e.EndSession(id) })
	return &r7CodexRig{e: e, rec: rec, id: id}
}

func (r *r7CodexRig) apply(t *testing.T, event string) error {
	t.Helper()
	return r.e.ApplyTypedEvent(r.id, event, nil)
}

func (r *r7CodexRig) turn(t *testing.T) status.Turn {
	t.Helper()
	call, ok := r.rec.last()
	if !ok {
		t.Fatalf("the engine emitted no status for %s", r.id)
	}
	return call.s.Turn
}

func (r *r7CodexRig) interaction(t *testing.T) status.Interaction {
	t.Helper()
	call, ok := r.rec.last()
	if !ok {
		t.Fatalf("the engine emitted no status for %s", r.id)
	}
	return call.s.Interaction
}
