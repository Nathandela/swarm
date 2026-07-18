package engine

// Shared byte-granularity fixture-replay scaffolding for R-C4 (regression
// freeze) and R-C5 (full-timeline rule proof). Deliberately self-contained
// (does not depend on trio_exploration_test.go's snapAt/lastLine/gridLines,
// which is temporary exploration-phase code slated for deletion in Phase H).

import (
	"testing"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// decodeSnap snapshots and decodes emu's current grid.
func decodeSnap(t *testing.T, emu *vt.Emulator) *vt.Snap {
	t.Helper()
	b, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap, err := vt.DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return snap
}

// snapAtOffset feeds capture[:n] (clamped to len(capture)) into a FRESH
// emulator and returns the resulting snapshot — an isolated, unambiguous
// "offset N" read (offset = prefix length fed, the same convention the Phase B
// evidence memo uses: "capture end" equals the total byte count).
func snapAtOffset(t *testing.T, capture []byte, n int) *vt.Snap {
	t.Helper()
	if n > len(capture) {
		n = len(capture)
	}
	if n < 0 {
		n = 0
	}
	emu := vt.NewEmulator(100, 30)
	defer emu.Close()
	emu.Feed(capture[:n])
	return decodeSnap(t, emu)
}

// replayFixture feeds capture through a single fresh 100x30 emulator, in
// fineStep-byte steps while the cumulative offset already fed falls inside any
// span in fineSpans (each [start,end) half-open, offsets = prefix length), and
// in coarse <=1KB steps elsewhere (clamped so a coarse step never reads past a
// fine span's start). fineStep=1 is the byte-exact mode required where an
// idle rule is declared (false-idle is the safety property); a coarser
// fineStep is legitimate for CLIs with no idle rule, where idle emissions are
// impossible by construction and the sweep only regression-checks busy
// coverage. After every step it decodes the snapshot, evaluates it
// via evaluateGridWithRules(snap, rules), and calls visit(offset, turn,
// interaction) where offset is the cumulative byte count fed so far.
func replayFixture(t *testing.T, capture []byte, rules gridRules, fineSpans [][2]int, fineStep int, visit func(offset int, turn status.Turn, inter status.Interaction)) {
	if fineStep < 1 {
		fineStep = 1
	}
	t.Helper()
	emu := vt.NewEmulator(100, 30)
	defer emu.Close()

	inFine := func(off int) bool {
		for _, sp := range fineSpans {
			if off >= sp[0] && off < sp[1] {
				return true
			}
		}
		return false
	}

	const coarseStep = 1024
	n := len(capture)
	off := 0
	for off < n {
		step := coarseStep
		if inFine(off) {
			step = fineStep
		} else {
			for _, sp := range fineSpans {
				if sp[0] > off && sp[0] < off+step {
					step = sp[0] - off
				}
			}
		}
		end := off + step
		if end > n {
			end = n
		}
		emu.Feed(capture[off:end])
		off = end
		snap := decodeSnap(t, emu)
		turn, inter := evaluateGridWithRules(snap, rules)
		visit(off, turn, inter)
	}
}
