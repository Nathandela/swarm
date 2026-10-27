package status

import "testing"

func TestDetachedNativeWorkIsNotCompleted(t *testing.T) {
	if got := Derive(Status{Process: ProcessLost, Turn: TurnUnknown, Interaction: InteractionError, NativeWorkUnknown: true}); got != GroupNeedsInput {
		t.Fatalf("native work reported %s", got)
	}
	if got := Derive(Status{Process: ProcessLost}); got != GroupCompleted {
		t.Fatalf("legacy lost status changed: %s", got)
	}
}
