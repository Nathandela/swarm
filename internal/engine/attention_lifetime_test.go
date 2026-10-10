package engine

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/status"
)

func TestAttentionErrorSurvivesGridFreshnessUntilHealthyTypedEvent(t *testing.T) {
	for _, recovery := range []struct{ event, turn string }{
		{"turn/started", "active"},
		{"thread/status/changed", "idle"},
	} {
		t.Run(recovery.event, func(t *testing.T) {
			clk, rec := newClock(), &emitRecorder{}
			e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
			e.RegisterSession("s1", "", 1, codex.New().SignalSources())
			if err := e.ApplyTypedEvent("s1", "turn/completed", map[string]string{"turn": "idle", "interaction": "error"}); err != nil {
				t.Fatal(err)
			}
			clk.advance(time.Minute)
			e.OnOutput("s1", codexIdleScreen())
			e.Tick()
			if got, ok := rec.last(); !ok || got.s.Turn != status.TurnIdle || got.s.Interaction != status.Interaction("error") || status.Derive(got.s) != status.GroupNeedsInput {
				t.Fatalf("grid or freshness timer erased provider failure: %+v", got.s)
			}
			if err := e.ApplyTypedEvent("s1", recovery.event, map[string]string{"turn": recovery.turn, "interaction": "none"}); err != nil {
				t.Fatal(err)
			}
			if got, _ := rec.last(); got.s.Turn != status.Turn(recovery.turn) || got.s.Interaction != status.InteractionNone {
				t.Fatalf("explicit healthy provider state failed to clear error: %+v", got.s)
			}
		})
	}
}
