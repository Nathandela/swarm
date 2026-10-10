package engine

import (
	"os"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

func TestClaudeCapturedSpinnerSurvivesTypedFreshnessExpiry(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "UserPromptSubmit"}); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Minute)
	for _, tc := range []struct {
		fixture string
		want    status.Turn
	}{
		{"neg-working-2.1.231", status.TurnActive},
		{"neg-composer-idle-2.1.231", status.TurnIdle},
	} {
		data, err := os.ReadFile("../adapter/claude/testdata/permdialog/" + tc.fixture + ".snap.json")
		if err != nil {
			t.Fatal(err)
		}
		snap, err := vt.DecodeSnapshot(data)
		if err != nil {
			t.Fatal(err)
		}
		e.OnOutput("s1", snap)
		if got, _ := rec.last(); got.s.Turn != tc.want {
			t.Fatalf("%s after freshness expiry: %+v, want %s", tc.fixture, got.s, tc.want)
		}
	}
}

func TestClaudeSpinnerDoesNotMistakeCompletionFooterForWork(t *testing.T) {
	for _, row := range []string{"✻ Crunched for 5s", "✶ Blanching… (nonsense · ↓ 74 tokens)", "The output quotes ✶ Blanching… (4s · ↓ 74 tokens)"} {
		snap := snapFromLines(100, 2, 2, true, []string{row, "──────────────────", "❯ ", "──────────────────"})
		turn, _, conclusive := evaluateGridSig(snap, sigClaude)
		if !conclusive || turn != status.TurnIdle {
			t.Fatalf("%q classified %s, conclusive=%t", row, turn, conclusive)
		}
	}
}
