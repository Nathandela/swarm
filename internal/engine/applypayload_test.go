package engine

// F2 (E10.2): an authenticated callback whose payload carries an out-of-vocabulary
// turn/interaction string must be REJECTED like an auth failure — it must not
// apply, must not advance the sequence, and must not emit. A valid callback stores
// only vocabulary values, so a downstream Derive can never see a bogus dimension.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func TestHandleCallbackRejectsInvalidVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]string
	}{
		{"bogus-turn", map[string]string{payloadKeyTurn: "bogus"}},
		{"bogus-interaction", map[string]string{payloadKeyInteraction: "sideways"}},
		{"empty-turn", map[string]string{payloadKeyTurn: ""}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			rec := &emitRecorder{}
			e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
			e.RegisterSession("s1", "tok1", 1, kinds("hook"))

			err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 5, Event: "x", Payload: tc.payload})
			if err == nil {
				t.Fatalf("invalid-vocabulary callback: got nil error, want rejection")
			}
			if rec.count() != 0 {
				t.Fatalf("invalid-vocabulary callback emitted %d change(s), want 0", rec.count())
			}

			// The dimension high-water must NOT have advanced: a valid callback with a
			// LOWER sequence (3 < the rejected 5, but > the never-advanced 0) is accepted.
			if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 3, Event: "active", Payload: turnSignal(status.TurnActive)}); err != nil {
				t.Fatalf("valid seq=3 after rejected seq=5: %v (the rejected callback must not advance the sequence high-water)", err)
			}
			if got, _ := rec.last(); got.s.Turn != status.TurnActive {
				t.Fatalf("valid callback turn=%s, want active", got.s.Turn)
			}
		})
	}
}

// A valid callback that names both dimensions with in-vocabulary values applies
// normally (the tightened validation does not reject legitimate payloads).
func TestHandleCallbackAcceptsFullVocabulary(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, kinds("hook"))

	err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "x",
		Payload: map[string]string{payloadKeyTurn: string(status.TurnIdle), payloadKeyInteraction: string(status.InteractionPermission)}})
	if err != nil {
		t.Fatalf("valid full-vocabulary callback: %v", err)
	}
	got, ok := rec.last()
	if !ok || got.s.Turn != status.TurnIdle || got.s.Interaction != status.InteractionPermission {
		t.Fatalf("applied (turn=%s, interaction=%s), want (idle, permission)", got.s.Turn, got.s.Interaction)
	}
}
