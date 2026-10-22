package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shim"
)

// writeDegradedMarker persists the structured-degraded marker markSessionDegradedFor writes.
func writeDegradedMarker(t *testing.T, stateDir, id, reason string) {
	t.Helper()
	raw, err := json.Marshal(historyTornMarker{Version: 1, Generation: backendGapGeneration(reason)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, id, sessionDegradedFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestResumeHistory_CodexMatchesADegradedLaunchStartedAfterTheBackendBudget pins recovery of a
// session whose backend never became servable. The shim then starts the agent only after its
// readiness budget, so the TUI's own rollout is created that much later than created_at.
// zmvdy4aj7fs57uru ran under the earlier 20 s + 20 s budgets: its thread started 51 s after
// created_at against a 40 s degraded delay. The measured [-2 s, +30 s] window must also hold
// around the degraded start, for both the earlier and the current delay, or every degraded
// Codex launch is unresumable (swarm-e8j). It applies only to a row with durable evidence of
// the degraded start: widening every row would let sessions started 38-90 s apart in one cwd
// see each other's rollouts.
func TestResumeHistory_CodexMatchesADegradedLaunchStartedAfterTheBackendBudget(t *testing.T) {
	const earlierDelay = 40 * time.Second
	cases := []struct {
		name   string
		reason string // the persisted degraded marker's gap reason; "" is no marker
		delta  time.Duration
		want   resumeHistoryOutcome
	}{
		{"zmvdy4aj7fs57uru: +51 s under the earlier 40 s delay", "backend_never_served", 51 * time.Second, resumeHistoryFound},
		{"earlier degraded start lower bound inclusive", "backend_never_served", earlierDelay - 2*time.Second, resumeHistoryFound},
		{"earlier degraded start upper bound inclusive", "backend_never_served", earlierDelay + 30*time.Second, resumeHistoryFound},
		{"between the normal and the earlier degraded window", "backend_never_served", 35 * time.Second, resumeHistoryNoMatch},
		{"current degraded start lower bound inclusive", "backend_never_served", shim.DegradedAgentDelay - 2*time.Second, resumeHistoryFound},
		{"current degraded start upper bound inclusive", "backend_never_served", shim.DegradedAgentDelay + 30*time.Second, resumeHistoryFound},
		{"after the current degraded window", "backend_never_served", shim.DegradedAgentDelay + 30*time.Second + time.Nanosecond, resumeHistoryNoMatch},
		{"a row with no degraded evidence 50 s apart", "", 50 * time.Second, resumeHistoryNoMatch},
		{"a row with no degraded evidence at +51 s", "", 51 * time.Second, resumeHistoryNoMatch},
		{"a row that lost a served backend is not a degraded start", gapBackendLost, 51 * time.Second, resumeHistoryNoMatch},
		// Rejoin and no-thread failures also write backend_unavailable although the agent
		// started on time, so that reason (including every pre-existing marker) is not evidence.
		{"a backend_unavailable marker is not a degraded start", gapBackendUnavailable, 51 * time.Second, resumeHistoryNoMatch},
		{"a degraded row keeps the original window", "backend_never_served", time.Second, resumeHistoryFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, stateDir := t.TempDir(), t.TempDir()
			cwd := filepath.Join(home, "work")
			src := legacySource("codex", cwd, legacyCreatedAt)
			if tc.reason != "" {
				writeDegradedMarker(t, stateDir, src.ID, tc.reason)
			}
			writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(tc.delta), "", "cli", "")
			resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())
			resolver.stateDir = stateDir
			got := resolver.Resolve(src)
			wantID := ""
			if tc.want == resumeHistoryFound {
				wantID = legacyCodexRootID
			}
			requireHistoryResult(t, got, tc.want, wantID)
		})
	}
}

// TestResumeHistory_ProductionResolversReadTheDegradedMarker pins the wiring: both the
// unmanaged resolver and the provider-native ones look for the marker under the state dir.
func TestResumeHistory_ProductionResolversReadTheDegradedMarker(t *testing.T) {
	sk := assemble(t)
	native, ok := sk.api.historyResolver.(*openCodeHistoryResolver)
	if !ok || native.core != sk.core {
		t.Fatalf("historyResolver is %T, want the connected native resolver", sk.api.historyResolver)
	}
	login, ok := native.fallback.(*loginResumeHistoryResolver)
	if !ok {
		t.Fatalf("native fallback is %T, want the provider login resolver", native.fallback)
	}
	unmanaged, ok := login.fallback.(*filesystemResumeHistoryResolver)
	if !ok {
		t.Fatalf("login fallback is %T, want filesystem resolver", login.fallback)
	}
	if unmanaged.stateDir == "" || unmanaged.stateDir != login.stateDir {
		t.Fatalf("unmanaged resolver stateDir = %q, want the state dir %q", unmanaged.stateDir, login.stateDir)
	}
}
