package engine

// E10.2 (S6/G5) negative authentication and E10.5 idempotence. A status callback
// mutates a session only with that session's live token and a strictly
// increasing sequence; tokens die with the session.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// E10.2: a callback with no token is a no-op — HandleCallback returns an error
// and Emit is never called.
func TestHandleCallbackTokenlessIsNoOp(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1234, kinds("hook"))

	before := rec.count()
	err := e.HandleCallback(Callback{SessionID: "s1", Token: "", Sequence: 1, Event: "Stop", Payload: turnSignal(status.TurnIdle)})
	if err == nil {
		t.Fatalf("tokenless callback: got nil error, want rejection")
	}
	if rec.count() != before {
		t.Fatalf("tokenless callback emitted %d change(s), want 0 (S6 no-op)", rec.count()-before)
	}
}

// E10.2: a callback carrying another session's live token is a no-op against
// this session.
func TestHandleCallbackForeignTokenIsNoOp(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))
	e.RegisterSession("s2", "tok2", 2, kinds("hook"))

	before := rec.count()
	err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok2", Sequence: 1, Event: "Stop", Payload: turnSignal(status.TurnIdle)})
	if err == nil {
		t.Fatalf("foreign-token callback: got nil error, want rejection")
	}
	if rec.count() != before {
		t.Fatalf("foreign-token callback emitted a change, want 0 (S6 no-op)")
	}
}

// E10.2: a replayed or otherwise non-increasing sequence is a no-op and does not
// regress the applied status.
func TestHandleCallbackReplayedSequenceIsNoOp(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 5, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("first callback seq=5: unexpected error %v", err)
	}
	applied := rec.count()

	// Replay the exact sequence and a lower one: both non-increasing -> rejected.
	for _, seq := range []uint64{5, 4} {
		err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: seq, Event: "idle", Payload: turnSignal(status.TurnIdle)})
		if err == nil {
			t.Fatalf("replayed/non-increasing seq=%d: got nil error, want rejection", seq)
		}
	}
	if rec.count() != applied {
		t.Fatalf("replayed callbacks emitted %d extra change(s), want 0 (S6 no-op)", rec.count()-applied)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("after replay, turn=%s, want active preserved (no regression)", got.s.Turn)
	}
}

// E10.2: after EndSession the token dies with the session, so a subsequent
// callback with that (now-dead) token is a no-op.
func TestHandleCallbackAfterEndSessionIsNoOp(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))
	e.EndSession("s1")

	before := rec.count()
	err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)})
	if err == nil {
		t.Fatalf("post-EndSession callback: got nil error, want rejection (token died)")
	}
	if rec.count() != before {
		t.Fatalf("post-EndSession callback emitted a change, want 0 (S6 no-op)")
	}
}

// E10.2: a callback for a session that was never registered has no live token to
// match, so it is a no-op.
func TestHandleCallbackUnregisteredSessionIsNoOp(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)

	err := e.HandleCallback(Callback{SessionID: "ghost", Token: "whatever", Sequence: 1, Event: "active", Payload: turnSignal(status.TurnActive)})
	if err == nil {
		t.Fatalf("unregistered-session callback: got nil error, want rejection")
	}
	if rec.count() != 0 {
		t.Fatalf("unregistered-session callback emitted a change, want 0")
	}
}

// E10.5: the engine is idempotent under duplicate and out-of-order deliveries.
// A duplicate (same sequence) does not double-apply; a lower sequence does not
// regress; a strictly newer sequence still applies.
func TestHandleCallbackIdempotentUnderDuplicateAndReorder(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 10, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
		t.Fatalf("seq=10: unexpected error %v", err)
	}
	afterFirst := rec.count()
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("seq=10 turn=%s, want active", got.s.Turn)
	}

	// Duplicate (same seq, same content) and out-of-order (lower seq, different
	// content): neither may double-apply or regress. Return value is asserted by
	// the replay test above; here the load-bearing property is no observable effect.
	_ = e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 10, Event: "active", Payload: turnSignal(status.TurnActive)})
	_ = e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 7, Event: "idle", Payload: turnSignal(status.TurnIdle)})
	if rec.count() != afterFirst {
		t.Fatalf("duplicate/out-of-order deliveries emitted %d extra change(s), want 0 (idempotent)", rec.count()-afterFirst)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("after duplicate/reorder, turn=%s, want active (no regression)", got.s.Turn)
	}

	// A strictly newer sequence still applies.
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 11, Event: "idle", Payload: turnSignal(status.TurnIdle)}); err != nil {
		t.Fatalf("seq=11: unexpected error %v", err)
	}
	if rec.count() != afterFirst+1 {
		t.Fatalf("newer seq=11 emitted %d change(s), want 1", rec.count()-afterFirst)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnIdle {
		t.Fatalf("seq=11 turn=%s, want idle", got.s.Turn)
	}
}
