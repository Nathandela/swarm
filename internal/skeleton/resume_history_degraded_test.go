package skeleton

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shim"
)

// TestResumeHistory_CodexMatchesADegradedLaunchStartedAfterTheBackendBudget pins recovery of a
// session whose backend never became servable. The shim then starts the agent only after its
// readiness budget plus its go-ahead budget, so the TUI's own rollout is created that much
// later than created_at. zmvdy4aj7fs57uru ran under the earlier 20 s + 20 s budgets: its thread
// started 51 s after created_at against a 40 s degraded delay. The measured [-2 s, +30 s] window
// must also hold around the degraded start, for both the earlier and the current budgets, or
// every degraded Codex launch is unresumable (swarm-e8j).
func TestResumeHistory_CodexMatchesADegradedLaunchStartedAfterTheBackendBudget(t *testing.T) {
	const earlierDelay = 40 * time.Second
	cases := []struct {
		name  string
		delta time.Duration
		want  resumeHistoryOutcome
	}{
		{"zmvdy4aj7fs57uru: +51 s under the earlier 40 s delay", 51 * time.Second, resumeHistoryFound},
		{"earlier degraded start lower bound inclusive", earlierDelay - 2*time.Second, resumeHistoryFound},
		{"earlier degraded start upper bound inclusive", earlierDelay + 30*time.Second, resumeHistoryFound},
		{"between the normal and the earlier degraded window", 35 * time.Second, resumeHistoryNoMatch},
		{"current degraded start plus the measured TUI startup", shim.DegradedAgentDelay + 11*time.Second, resumeHistoryFound},
		{"current degraded start lower bound inclusive", shim.DegradedAgentDelay - 2*time.Second, resumeHistoryFound},
		{"current degraded start upper bound inclusive", shim.DegradedAgentDelay + 30*time.Second, resumeHistoryFound},
		{"after the current degraded window", shim.DegradedAgentDelay + 30*time.Second + time.Nanosecond, resumeHistoryNoMatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cwd := filepath.Join(home, "work")
			writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(tc.delta), "", "cli", "")
			got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
			wantID := ""
			if tc.want == resumeHistoryFound {
				wantID = legacyCodexRootID
			}
			requireHistoryResult(t, got, tc.want, wantID)
		})
	}
}
