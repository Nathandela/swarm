package status

import (
	"encoding/json"
	"testing"
)

func TestAttentionRemainsVisibleDuringSiblingWork(t *testing.T) {
	for _, turn := range []Turn{TurnActive, TurnIdle, TurnUnknown} {
		s := Status{Process: ProcessRunning, Turn: turn, Interaction: InteractionPermission, Attention: true}
		if Derive(s) != GroupNeedsInput {
			t.Fatalf("pending request hidden by %s", turn)
		}
		s.Process = ProcessExited
		if Derive(s) != GroupCompleted {
			t.Fatal("attention resurrected an ended process")
		}
	}
}

func TestAttentionZeroPreservesLegacyStatusJSON(t *testing.T) {
	s := Status{Process: ProcessRunning, Turn: TurnActive, Interaction: InteractionNone}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"Process":"running","Turn":"active","Interaction":"none"}` {
		t.Fatalf("legacy status changed: %s", raw)
	}
	s.Attention = true
	raw, _ = json.Marshal(s)
	var got Status
	if err := json.Unmarshal(raw, &got); err != nil || !got.Attention || got.Turn != TurnActive {
		t.Fatalf("attention roundtrip: %+v %v", got, err)
	}
}
