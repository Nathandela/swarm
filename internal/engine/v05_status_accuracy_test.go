package engine

// v0.5 P1 status-accuracy regression tests (beads agents-tracker-dqh /
// agents-tracker-q65, ADR-007). Two defects, both rooted in the apply-unknown
// rule and the last-line-only generic heuristic, proven by replaying the users'
// live transcripts through the production vt emulator:
//
//   - dqh: a typed Stop commits idle; 30s later the grid tap reads Claude's idle
//     screen (composer above a "Brewed for Ns" footer), the generic last-line rule
//     cannot classify the footer, commits unknown, and idle renders as Working.
//   - q65: Codex has no typed signal, so the grid is its sole driver; its idle
//     screen is a composer ("> "/"› …") with the model footer BELOW it, so the
//     last-line rule reads the footer, returns unknown, and it sticks on Working.
//
// The fixtures below reconstruct those real screens (footer BELOW the composer)
// and the engine-level decay scenario. They drive the public OnOutput/HandleCallback
// seam with a session whose heuristic SignalSource declares a per-adapter grid
// signature (Descriptor["grid"]), exactly as the daemon wires each adapter.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// gridSig builds the SignalSources a session declares for a given grid signature
// ("codex"/"claude"/generic), mirroring an adapter's heuristic SignalSource.
func gridSig(sig string) []adapter.SignalSource {
	return []adapter.SignalSource{{Kind: "heuristic", Descriptor: map[string]string{"grid": sig}}}
}

// --- reconstructed real screens (footer rendered BELOW the composer) ---

// codexIdleScreen: Codex idle at its composer ("› …", U+203A) with the model/cwd
// footer as the LAST content line and the cursor parked on the composer row.
func codexIdleScreen() *vt.Snap {
	return snapFromLines(40, 26, 2, true, []string{
		"● Ran cargo test — 3 passed",
		"",
		"› Write tests for @auth.go",
		"  gpt-5.6-sol medium · ~/code",
	})
}

// codexBusyScreen: Codex working — "esc to interrupt" in the status region ABOVE
// the footer (the last content line).
func codexBusyScreen() *vt.Snap {
	return snapFromLines(48, 0, 0, false, []string{
		"● Running cargo test",
		"",
		"  Working (0s • esc to interrupt)",
		"  gpt-5.6-sol medium · ~/code",
	})
}

// codexBusyBrailleScreen: Codex working with a braille spinner ABOVE the footer,
// proving the region scan catches a spinner that is not on the last line.
func codexBusyBrailleScreen() *vt.Snap {
	return snapFromLines(40, 0, 0, false, []string{
		"● Thinking",
		"  ⠹ pondering the problem",
		"  gpt-5.6-sol medium · ~/code",
	})
}

// claudeIdleScreen: Claude idle at its composer ("❯", U+276F) with the
// "✻ Brewed for Ns" footer as the LAST content line.
func claudeIdleScreen() *vt.Snap {
	return snapFromLines(48, 2, 2, true, []string{
		"⏺ Done. What would you like to do next?",
		"",
		"❯ ",
		"  ✻ Brewed for 2m 18s",
	})
}

// claudeBusyScreen: Claude working — "esc to interrupt" in the status region above
// a non-idle footer.
func claudeBusyScreen() *vt.Snap {
	return snapFromLines(56, 0, 0, false, []string{
		"⏺ Let me check the auth flow",
		"",
		"  Baking… (12s · esc to interrupt)",
		"  claude-opus · ~/code",
	})
}

// TestOnOutput_InconclusivePreservesCommittedTurn (ADR-007): an inconclusive grid
// tap is absence of evidence, not evidence of change — it must PRESERVE the prior
// committed turn (from active AND from idle), never overwrite it with unknown.
func TestOnOutput_InconclusivePreservesCommittedTurn(t *testing.T) {
	cases := []struct {
		name    string
		conc    *vt.Snap
		wantSet status.Turn
	}{
		{"active-held", spinnerSnap(), status.TurnActive},
		{"idle-held", idlePromptSnap(), status.TurnIdle},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			rec := &emitRecorder{}
			staleness := 30 * time.Second
			e := newEngine(clk, constCPU(0), rec, staleness, time.Second)
			e.RegisterSession("s1", "tok1", 1, gridSig("prompt-marker"))

			e.OnOutput("s1", tc.conc)
			if got, _ := rec.last(); got.s.Turn != tc.wantSet {
				t.Fatalf("setup turn=%s, want %s", got.s.Turn, tc.wantSet)
			}

			// Past the freshness/staleness window, an inconclusive read must not change it.
			clk.advance(2 * staleness)
			e.OnOutput("s1", ambiguousSnap())
			if got, _ := rec.last(); got.s.Turn != tc.wantSet {
				t.Fatalf("inconclusive grid overwrote committed turn: turn=%s, want %s held (ADR-007)", got.s.Turn, tc.wantSet)
			}
		})
	}
}

// TestOnOutput_TypedIdleHoldsPastStalenessCliff (dqh, engine level): a typed Stop
// commits idle; past the 30s typed-signal freshness cliff an inconclusive grid tap
// must leave idle standing (it used to commit unknown -> Working).
func TestOnOutput_TypedIdleHoldsPastStalenessCliff(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	staleness := 30 * time.Second
	e := newEngine(clk, constCPU(0), rec, staleness, time.Second)
	e.RegisterSession("s1", "tok1", 1, gridSig("prompt-marker"))

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "idle", Payload: turnSignal(status.TurnIdle)}); err != nil {
		t.Fatalf("Stop hook: %v", err)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnIdle {
		t.Fatalf("setup turn=%s, want idle", got.s.Turn)
	}

	clk.advance(2 * staleness) // typed signal is now stale; the grid governs
	e.OnOutput("s1", ambiguousSnap())
	if got, _ := rec.last(); got.s.Turn != status.TurnIdle {
		t.Fatalf("idle decayed to turn=%s past the staleness cliff, want idle held (dqh/ADR-007)", got.s.Turn)
	}
}

// TestOnOutput_CodexGridSignature (q65): the codex grid signature reads the real
// codex screens conclusively — idle at the composer despite the footer below it,
// active on a busy marker (esc-to-interrupt or a braille spinner).
func TestOnOutput_CodexGridSignature(t *testing.T) {
	cases := []struct {
		name string
		snap *vt.Snap
		want status.Turn
	}{
		{"idle-composer-above-footer", codexIdleScreen(), status.TurnIdle},
		{"busy-esc-to-interrupt", codexBusyScreen(), status.TurnActive},
		{"busy-braille-spinner", codexBusyBrailleScreen(), status.TurnActive},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			rec := &emitRecorder{}
			e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
			e.RegisterSession("s1", "tok1", 1, gridSig("codex"))

			e.OnOutput("s1", tc.snap)
			got, ok := rec.last()
			if !ok {
				t.Fatalf("codex %s screen emitted no status change from the unknown seed", tc.name)
			}
			if got.s.Turn != tc.want {
				t.Fatalf("codex %s -> turn=%s, want %s", tc.name, got.s.Turn, tc.want)
			}
		})
	}
}

// TestOnOutput_ClaudeGridSignature (dqh): the claude grid signature reads the real
// claude screens conclusively — idle at the composer despite the Brewed footer
// below it, active on a busy marker.
func TestOnOutput_ClaudeGridSignature(t *testing.T) {
	cases := []struct {
		name string
		snap *vt.Snap
		want status.Turn
	}{
		{"idle-composer-above-brewed-footer", claudeIdleScreen(), status.TurnIdle},
		{"busy-esc-to-interrupt", claudeBusyScreen(), status.TurnActive},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			rec := &emitRecorder{}
			e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
			e.RegisterSession("s1", "tok1", 1, gridSig("claude"))

			e.OnOutput("s1", tc.snap)
			got, ok := rec.last()
			if !ok {
				t.Fatalf("claude %s screen emitted no status change from the unknown seed", tc.name)
			}
			if got.s.Turn != tc.want {
				t.Fatalf("claude %s -> turn=%s, want %s", tc.name, got.s.Turn, tc.want)
			}
		})
	}
}
