package engine

// R-C4 (Phase C): regression freeze. The claude and codex fixtures committed in
// prior phases are replayed at byte granularity through the OLD generic
// evaluator (evaluateGrid) and the NEW rules-aware evaluator
// (evaluateGridWithRules), configured from each adapter's OWN real
// registry.New(name).SignalSources() (R-H4 committee finding: a hand-copied
// rule list would not catch a future claude/codex SignalSources change) — as
// of this phase that is just "prompt-marker", no busy/idle rules. Every
// intermediate verdict must match exactly, proving evaluateGridWithRules's
// fallback path is byte-for-byte identical to evaluateGrid when an adapter
// declares no grid rules of its own; a future adapter change that adds a
// busy/idle rule would surface here as a real divergence, not a silent gap.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/vt"
)

func TestGridRulesRegressionFreeze_ClaudeAndCodexFixtures(t *testing.T) {
	fixtures := []struct {
		adapterName string
		path        string
	}{
		{"claude", "../adapter/claude/testdata/claude.json"},
		{"codex", "../adapter/codex/testdata/codex.json"},
	}
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.path, func(t *testing.T) {
			a, ok := registry.New(fx.adapterName)
			if !ok {
				t.Fatalf("registry: unknown adapter %q", fx.adapterName)
			}

			loaded, err := fixtureio.LoadFixture(fx.path)
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			if len(loaded.PTYCapture) < 100 {
				t.Fatalf("fixture %s too small for a >=100-step byte replay: %d bytes", fx.path, len(loaded.PTYCapture))
			}
			rules := parseGridRules(a.SignalSources())

			emu := vt.NewEmulator(100, 30)
			defer emu.Close()
			for i, b := range loaded.PTYCapture {
				emu.Feed([]byte{b})
				snap := decodeSnap(t, emu)
				oldTurn, oldInter := evaluateGrid(snap)
				newTurn, newInter := evaluateGridWithRules(snap, rules)
				if oldTurn != newTurn || oldInter != newInter {
					t.Fatalf("step %d/%d: old=(%s,%s) new=(%s,%s), verdicts diverged",
						i+1, len(loaded.PTYCapture), oldTurn, oldInter, newTurn, newInter)
				}
			}
		})
	}
}
