package status

import "testing"

func TestAttentionErrorDerivation(t *testing.T) {
	for _, process := range []Process{ProcessRunning, ProcessExited, ProcessLost} {
		for _, turn := range []Turn{TurnIdle, TurnActive, TurnUnknown} {
			t.Run(string(process)+"/"+string(turn), func(t *testing.T) {
				want := GroupCompleted
				if process == ProcessRunning {
					want = GroupWorking
					if turn == TurnIdle {
						want = GroupNeedsInput
					}
				}
				got := Derive(Status{Process: process, Turn: turn, Interaction: Interaction("error")})
				if got != want {
					t.Errorf("provider error with process=%s turn=%s: group=%s, want %s", process, turn, got, want)
				}
			})
		}
	}
}
