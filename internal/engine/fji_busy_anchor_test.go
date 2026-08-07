package engine

// agents-tracker-fji FIX D: the grid busy phrase must be anchored to the SHAPE of
// a real status row, not matched as a bare substring anywhere in the bottom
// region.
//
// hasBusyMarker matched "esc to interrupt" ANYWHERE in the bottom 12 rows, so a
// session whose scrolled OUTPUT carries the phrase — a claude editing swarm's own
// heuristic.go, a quoted doc, a pasted transcript — read WORKING while it sat
// idle at its composer, and stuck there: the grid tap is the only thing that
// could correct it and it kept re-reading the same prose.
//
// The anchor comes from the live captures. In every frame where a real CLI is
// working, the hint is rendered inside a parenthesized group that opens and
// closes on the status row itself:
//
//	• Working (1s • esc to interrupt)
//	• Starting MCP servers (2/4): codex_apps, context7 (0s • esc to interrupt)
//
// (docs/verification/fixtures/spike-sa/codex-plain.fixture.json, replayed below).
// Prose that merely quotes the phrase has no such enclosure.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// codexBusyCapture is the live codex session whose status row carries the busy
// hint. It is the ONLY live corpus in which the phrase appears at all: the
// claude captures (spike-sa/claude-*, spike-sc/c1-*) render their working line as
// "✽ Sock-hopping… (12s · ↓ 147 tokens)" and never print the phrase.
const codexBusyCapture = "../../docs/verification/fixtures/spike-sa/codex-plain.fixture.json"

// codexBusyOffsets are prefix lengths of that capture at which codex is working
// and its status row shows the hint. 21851 is the two-parenthesis case
// ("Starting MCP servers (0/4): … (0s • esc to interrupt)"), which the anchor must
// not be confused by; the rest are the plain "• Working (Ns • esc to interrupt)"
// row. codexSettledOffset is the capture end, where the turn is over.
var codexBusyOffsets = []int{21851, 36418, 50985, 65553}

const codexSettledOffset = 72837

// claudeQuotedBusyPhraseScreen: claude sitting IDLE at its empty composer while
// its scrolled transcript quotes the busy hint — the bead's repro, an agent
// working on this very file. Shaped like the live claude frame
// (docs/verification/fixtures/spike-sc/c1-delay30s): composer box between two
// rules, model/context footer below it.
func claudeQuotedBusyPhraseScreen() *vt.Snap {
	return snapFromLines(100, 2, 8, true, []string{
		"⏺ I read internal/engine/heuristic.go:",
		"",
		`  const escToInterrupt = "esc to interrupt"`,
		"",
		`  hasBusyMarker matches that bare substring anywhere in the bottom 12 rows, so a`,
		"  session whose own output quotes it reads WORKING while it is idle.",
		"",
		"────────────────────────────────────────────────────────────────────────────────",
		"❯ ",
		"────────────────────────────────────────────────────────────────────────────────",
		"  Opus 4.8 | Ctx: 12% | swarm",
	})
}

// codexQuotedBusyPhraseScreen: the same defect on codex's screen shape — idle at
// the composer with the cursor parked on it, the phrase only ever in prose.
func codexQuotedBusyPhraseScreen() *vt.Snap {
	return snapFromLines(100, 2, 5, true, []string{
		"• Read internal/engine/heuristic.go",
		"",
		`  It matches "esc to interrupt" anywhere in the bottom region, so a session whose`,
		"  output quotes the hint reads busy while it sits idle.",
		"",
		"› ",
		"  gpt-5.6-sol medium · ~/code/swarm",
	})
}

// TestFJI_QuotedBusyPhraseInOutputReadsIdle: an idle composer with the phrase in
// scrolled output must read idle under both signatures. Red before the anchor:
// both read (active, none).
func TestFJI_QuotedBusyPhraseInOutputReadsIdle(t *testing.T) {
	cases := []struct {
		name string
		sig  string
		snap *vt.Snap
	}{
		{"claude", sigClaude, claudeQuotedBusyPhraseScreen()},
		{"codex", sigCodex, codexQuotedBusyPhraseScreen()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn, inter, conclusive := evaluateGridSig(tc.snap, tc.sig)
			if turn != status.TurnIdle || inter != status.InteractionNone || !conclusive {
				t.Fatalf("%s idle composer with the phrase quoted in output read (%s, %s, conclusive=%v); want (idle, none, true)",
					tc.name, turn, inter, conclusive)
			}
		})
	}
}

// TestFJI_RealBusyStatusRowStillReadsActive is the over-anchoring guard: the live
// codex busy frames must keep reading active, and the settled frame at the end of
// the capture must keep reading idle.
//
// The capture is replayed at 100x45 (its own terminal was 45 rows here; the
// shared 100x30 snapAtOffset panics inside the vt emulator on this capture's
// scroll-region sequences, so it cannot be used).
func TestFJI_RealBusyStatusRowStillReadsActive(t *testing.T) {
	fx, err := fixtureio.LoadFixture(codexBusyCapture)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, off := range codexBusyOffsets {
		snap := snapOfCapture(t, fx.PTYCapture[:off], 100, 45)
		turn, _, conclusive := evaluateGridSig(snap, sigCodex)
		if turn != status.TurnActive || !conclusive {
			t.Errorf("live codex busy frame at offset %d read (%s, conclusive=%v); want (active, true)", off, turn, conclusive)
		}
	}
	snap := snapOfCapture(t, fx.PTYCapture[:codexSettledOffset], 100, 45)
	if turn, _, conclusive := evaluateGridSig(snap, sigCodex); turn != status.TurnIdle || !conclusive {
		t.Errorf("live codex settled frame at offset %d read (%s, conclusive=%v); want (idle, true)", codexSettledOffset, turn, conclusive)
	}
}

// TestFJI_BusyHintRowShapes pins the rule itself, one row at a time: the live
// status-row shapes are busy, the prose shapes are not.
func TestFJI_BusyHintRowShapes(t *testing.T) {
	busy := []string{
		// Live codex status rows (spike-sa/codex-plain.fixture.json).
		"• Working (1s • esc to interrupt)",
		"• Starting MCP servers (2/4): codex_apps, context7 (0s • esc to interrupt)",
		// The reconstructed codex/claude status rows already pinned in
		// v05_status_accuracy_test.go.
		"  Working (0s • esc to interrupt)",
		"  Baking… (12s · esc to interrupt)",
	}
	prose := []string{
		`  const escToInterrupt = "esc to interrupt"`,
		"  hasBusyMarker matches the bare substring esc to interrupt anywhere in the region",
		"  The status line says esc to interrupt while a turn runs.",
		// A parenthesis that closes BEFORE the phrase, and one that opens after it:
		// neither encloses it.
		"  As documented (above), esc to interrupt is the hint (see heuristic.go)",
	}
	for _, row := range busy {
		if !hasBusyMarker(snapFromLines(100, 0, 0, false, []string{row})) {
			t.Errorf("status row %q read as not busy; want busy", row)
		}
	}
	for _, row := range prose {
		if hasBusyMarker(snapFromLines(100, 0, 0, false, []string{row})) {
			t.Errorf("prose row %q read as busy; want not busy", row)
		}
	}
}
