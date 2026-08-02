package conformance_test

// Slice S11 REVIEW ROUND 3 -- FAILING-FIRST (TDD RED, GG-5) for R-1, a dedupe that can never
// dedupe.
//
// mobile/relay.go reportSkew emits a clock event only when the verdict CHANGED, and its own
// comment says why: "Only a CHANGE is emitted, or a two-minute-slow phone would raise an
// event per reply for the life of the session." The change is detected by comparing
// err.Error() strings -- and phonecore/skew.go builds that string from the measured offset at
// full time.Duration precision, out of two wall-clock reads bracketing a network round trip.
// No two measurements of one CONSTANT skew produce the same string, so the guard never fires
// and the phone raises an event on every reply: verbatim the behaviour the comment claims it
// prevents. Bounded by the reply rate, so spam rather than a storm -- but a decorative guard
// is worse than none, because the next reader believes it.
//
// This file contains NO implementation.

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	swarmmobile "github.com/Nathandela/swarm/mobile"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// s11r3Recorder captures the events the app raises across the bound surface.
type s11r3Recorder struct {
	mu   sync.Mutex
	seen []swarmmobile.Event
}

func (r *s11r3Recorder) OnEvent(e *swarmmobile.Event) {
	if e == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, *e)
}

// of returns every recorded event of one kind.
func (r *s11r3Recorder) of(kind string) []swarmmobile.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []swarmmobile.Event
	for _, e := range r.seen {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// sawOutcome reports whether the app has resolved the given operation id.
func (r *s11r3Recorder) sawOutcome(operationID string) bool {
	for _, e := range r.of("outcome") {
		if e.Message == operationID {
			return true
		}
	}
	return false
}

// s11r3SkewedReply seals one command reply whose AUTHENTICATED machine timestamp is offset
// from real time, and appends it to the phone's mailbox.
//
// It mirrors remotegw.SealControlReply exactly -- same kind tag, same promoted
// protocol.Control, same header with a zero sender key id (the command-reply bucket) -- and
// differs in the one field the test needs to control. SealControlReply stamps time.Now()
// internally, so a skewed machine clock is not otherwise expressible from outside the
// gateway.
func s11r3SkewedReply(t *testing.T, h *harness, ctrl protocol.Control, offset time.Duration) {
	t.Helper()

	plaintext, err := json.Marshal(struct {
		Kind             string `json:"kind"`
		protocol.Control        // promoted, so the frozen json tags stay the source of truth
	}{Kind: "command_reply", Control: ctrl})
	if err != nil {
		t.Fatalf("marshal reply frame: %v", err)
	}

	h.mu.Lock()
	h.replySeq++
	seq := h.replySeq
	h.mu.Unlock()

	env, err := crypto.SealMailbox(h.Keys.ContentKey, crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  h.EpochID,
		Seq:      seq,
		IssuedAt: time.Now().Add(offset).UnixMilli(),
	}, plaintext)
	if err != nil {
		t.Fatalf("seal skewed reply: %v", err)
	}
	if _, err := h.machineRelay.MailboxAppend(h.ctx, h.phoneTarget, env.Marshal()); err != nil {
		t.Fatalf("append skewed reply: %v", err)
	}
}

// TestS11R3_AConstantClockSkewRaisesOneEventNotOnePerReply is PB-TIME-1's report half held
// to what its own guard claims.
//
// Every reply closes a fresh bracket, so a phone whose clock is off by a constant amount
// re-measures a slightly different offset each time -- the difference is jitter in the round
// trip, not a change in the user's clock. Keyed on the rendered message, the guard sees a
// change every time and the user's event stream fills with restatements of one fact.
func TestS11R3_AConstantClockSkewRaisesOneEventNotOnePerReply(t *testing.T) {
	h := newHarness(t)
	rec := &s11r3Recorder{}
	if err := h.App.SetEventListener(rec); err != nil {
		t.Fatalf("SetEventListener: %v", err)
	}
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	// Well outside phonecore.MaxClockSkew, and CONSTANT: one broken clock, not a drifting one.
	const skew = 2 * time.Minute
	const measurements = 6

	for i := 0; i < measurements; i++ {
		op, err := h.App.Kill(testSession)
		if err != nil {
			t.Fatalf("Kill #%d: %v", i+1, err)
		}
		s11r3SkewedReply(t, h, protocol.Control{
			Op:          protocol.OpOK,
			EndpointID:  h.Machine,
			SessionID:   testSession,
			OperationID: op.OperationID,
		}, skew)
		eventually(t, "the phone never resolved the reply, so no bracket closed", func() bool {
			return rec.sawOutcome(op.OperationID)
		})
	}

	clock := rec.of("clock")
	if len(clock) == 0 {
		t.Fatalf("a phone %v out of step raised NO clock event across %d measurements. PB-TIME-1's "+
			"criterion is a distinct, user-legible error, and the daemon's own refusal reads \"not "+
			"authorized\" -- which sends the user to re-pair when the fix is to correct their clock",
			skew, measurements)
	}
	if len(clock) > 1 {
		t.Errorf("one constant %v skew raised %d clock events across %d measurements, want 1.\n"+
			"reportSkew dedupes on the rendered message, and phonecore/skew.go builds that from the "+
			"measured offset at full time.Duration precision out of two wall-clock reads -- so every "+
			"bracket renders a different string and the guard can never fire. The messages were: %v",
			skew, len(clock), measurements, s11r3Messages(clock))
	}

	// MUTATION CONTROL: the report must not be latched either. A clock the user FIXES has to
	// be able to raise the event again if it breaks a second time, or the guard has simply
	// been replaced by a one-shot.
	op, err := h.App.Kill(testSession)
	if err != nil {
		t.Fatalf("Kill after the skew was reported: %v", err)
	}
	s11r3SkewedReply(t, h, protocol.Control{
		Op: protocol.OpOK, EndpointID: h.Machine, SessionID: testSession, OperationID: op.OperationID,
	}, 0) // the clock is corrected
	eventually(t, "the phone never resolved the corrected reply", func() bool {
		return rec.sawOutcome(op.OperationID)
	})

	op, err = h.App.Kill(testSession)
	if err != nil {
		t.Fatalf("Kill after the clock was corrected: %v", err)
	}
	s11r3SkewedReply(t, h, protocol.Control{
		Op: protocol.OpOK, EndpointID: h.Machine, SessionID: testSession, OperationID: op.OperationID,
	}, skew) // ... and breaks again
	eventually(t, "the second skew was never reported", func() bool {
		return len(rec.of("clock")) > len(clock)
	})
}

func s11r3Messages(events []swarmmobile.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Message)
	}
	return out
}
