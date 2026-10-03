package skeleton

import (
	"fmt"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestCLIRefreshReviewDiscoveryFairness(t *testing.T) {
	f := newAuthFake("account")
	w := testWatcher(t, f)
	w.state = emptyAuthWatchState()
	baseline := persist.CLIIdentity{Path: "/abs/codex", Version: "1.0.0", Fingerprint: "old"}
	for i := 0; i < maxCLIRefreshProbesPerTick+2; i++ {
		m := runningCodex(fmt.Sprintf("s%02d", i), "account", status.TurnIdle, "thread")
		m.CLIIdentity = &baseline
		f.add(m)
	}
	seen := map[string]int{}
	w.cliProbe = func(_, _ string, _ []string, cwd string) (*persist.CLIIdentity, error) {
		seen[cwd]++
		return &baseline, nil
	}
	w.cliObserve = func(string) *persist.CLIIdentity { return &baseline }
	for i := 0; i < 4; i++ {
		w.discoverCLIRefreshes()
	}
	if len(seen) != len(f.sessions) {
		t.Fatalf("only %d/%d session environments probed; first budget cohort starves rest", len(seen), len(f.sessions))
	}
}

func TestCLIRefreshReviewProbeBeforeClaimAndRecheckActivity(t *testing.T) {
	f := newAuthFake("account")
	w := testWatcher(t, f)
	w.state = emptyAuthWatchState()
	baseline := persist.CLIIdentity{Path: "/abs/codex", Version: "1.0.0", Fingerprint: "old"}
	target := persist.CLIIdentity{Path: "/abs/codex", Version: "1.1.0", Fingerprint: "new"}
	m := runningCodex("source", "account", status.TurnIdle, "thread")
	m.CLIIdentity = &baseline
	f.add(m)
	w.state.CLI[m.ID] = cliRefreshRecord{AgentType: "codex", Target: target, State: cliRefreshPending}
	w.cliObserve = func(string) *persist.CLIIdentity { return &baseline }
	w.cliProbe = func(_, _ string, _ []string, _ string) (*persist.CLIIdentity, error) {
		st, err := loadAuthWatchStateChecked(w.stateDir)
		if err != nil {
			t.Fatal(err)
		}
		if st.Killed[m.ID] {
			t.Error("durable kill claim exists while feasibility probe still runs")
		}
		f.sessions[m.ID].Status.Turn = status.TurnActive
		return &target, nil
	}
	w.recycle("codex", m)
	if len(f.killed) != 0 {
		t.Fatal("killed source that became active during version probe")
	}
}
