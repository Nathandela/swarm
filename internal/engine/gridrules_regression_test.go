package engine

// R-C4 (Phase C): regression freeze. The claude and codex fixtures committed in
// prior phases are replayed at byte granularity through the OLD generic
// evaluator (evaluateGrid) and the NEW rules-aware evaluator
// (evaluateGridWithRules) configured with only "prompt-marker" declared — no
// busy/idle rules, matching every existing adapter's real SignalSources as of
// this phase. Every intermediate verdict must match exactly, proving
// evaluateGridWithRules's fallback path is byte-for-byte identical to
// evaluateGrid when an adapter declares no grid rules of its own.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/vt"
)

func TestGridRulesRegressionFreeze_ClaudeAndCodexFixtures(t *testing.T) {
	fixtures := []string{
		"../adapter/claude/testdata/claude.json",
		"../adapter/codex/testdata/codex.json",
	}
	for _, path := range fixtures {
		path := path
		t.Run(path, func(t *testing.T) {
			fx, err := fixtureio.LoadFixture(path)
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			if len(fx.PTYCapture) < 100 {
				t.Fatalf("fixture %s too small for a >=100-step byte replay: %d bytes", path, len(fx.PTYCapture))
			}
			rules := parseGridRules([]adapter.SignalSource{
				{Kind: "heuristic", Descriptor: map[string]string{"grid": "prompt-marker"}},
			})

			emu := vt.NewEmulator(100, 30)
			defer emu.Close()
			for i, b := range fx.PTYCapture {
				emu.Feed([]byte{b})
				snap := decodeSnap(t, emu)
				oldTurn, oldInter := evaluateGrid(snap)
				newTurn, newInter := evaluateGridWithRules(snap, rules)
				if oldTurn != newTurn || oldInter != newInter {
					t.Fatalf("step %d/%d: old=(%s,%s) new=(%s,%s), verdicts diverged",
						i+1, len(fx.PTYCapture), oldTurn, oldInter, newTurn, newInter)
				}
			}
		})
	}
}
