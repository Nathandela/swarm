package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestCLIRefreshCompletionWriteFailureRetainsAuthority(t *testing.T) {
	w, f, _, target := acceptanceRefreshRig(t)
	f.sessions["source"].Status.Process = status.ProcessExited
	child := runningCodex("child", identityA, status.TurnIdle, migratedConversationID)
	child.ResumedFrom, child.CLIIdentity = "source", target
	f.add(child)
	w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshWaiting, ReplacementID: child.ID}
	w.state.Candidates["source"] = child.ID
	w.state.Killed["source"] = true
	cleared := 0
	w.clearRecycle = func(string) { cleared++ }
	w.writeState = func(string, []byte) (bool, error) { return false, errors.New("disk full before rename") }
	if w.checkCLIReplacement("source", *f.sessions["source"], child) {
		t.Fatal("declared completion without persisting it")
	}
	if !w.state.Killed["source"] || w.state.CLI["source"].State != cliRefreshWaiting || cleared != 0 {
		t.Fatalf("lost recovery authority on failed commit: %+v, cleared=%d", w.state, cleared)
	}
	w.writeState = nil
	if !w.checkCLIReplacement("source", *f.sessions["source"], child) || cleared != 1 {
		t.Fatal("recovered persistence did not complete exactly once")
	}
	loaded, err := loadAuthWatchStateChecked(w.stateDir)
	if err != nil || loaded.CLI["source"].State != cliRefreshComplete || loaded.Killed["source"] {
		t.Fatalf("completion not durable: %+v / %v", loaded, err)
	}
}

func TestCLIRefreshBlockedWriteFailureRetainsAuthority(t *testing.T) {
	w, _, _, target := acceptanceRefreshRig(t)
	w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshWaiting}
	w.state.Killed["source"] = true
	cleared := 0
	w.clearRecycle = func(string) { cleared++ }
	w.writeState = func(string, []byte) (bool, error) { return false, errors.New("disk full before rename") }
	w.blockCLIRefresh("source", "replacement failed")
	if !w.state.Killed["source"] || w.state.CLI["source"].State != cliRefreshWaiting || cleared != 0 {
		t.Fatalf("failed blocked checkpoint lost recovery authority: %+v / %d", w.state, cleared)
	}
	w.writeState = nil
	w.blockCLIRefresh("source", "replacement failed")
	if w.state.Killed["source"] || w.state.CLI["source"].State != cliRefreshBlocked || cleared != 1 {
		t.Fatal("successful blocked checkpoint did not release authority")
	}
}

func TestCLIRefreshFailedLaunchBackoffRetainsSource(t *testing.T) {
	w, f, _, target := acceptanceRefreshRig(t)
	f.sessions["source"].Status.Process = status.ProcessExited
	w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshPending}
	w.state.Killed["source"] = true
	now := time.Now()
	w.now = func() time.Time { return now }
	calls := 0
	w.launch = func(daemon.LaunchSpec) (persist.Meta, error) {
		calls++
		return persist.Meta{}, errors.New("installer temporarily removed executable")
	}
	for i := 0; i < 10; i++ {
		w.tick()
	}
	if calls != 1 || !w.state.Killed["source"] || len(f.deleted) != 0 {
		t.Fatalf("retry not paced or source lost: calls=%d state=%+v deleted=%v", calls, w.state, f.deleted)
	}
	now = now.Add(refreshRetryBase + time.Second)
	w.tick()
	if calls != 2 || w.state.Retries["source"].Attempts != 2 {
		t.Fatalf("paced retry did not occur: calls=%d retry=%+v", calls, w.state.Retries["source"])
	}
}

func TestCLIRefreshDisableStillCompletesOwedReplacement(t *testing.T) {
	w, f, _, target := acceptanceRefreshRig(t)
	f.sessions["source"].Status.Process = status.ProcessExited
	w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshPending}
	w.state.Killed["source"] = true
	if err := SetCLIRefreshDisabled(w.stateDir, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		w.tick()
	}
	if len(f.launched) != 1 || w.state.CLI["source"].State != cliRefreshComplete || len(f.deleted) != 0 {
		t.Fatalf("disabled setting stranded owned recovery: launches=%d record=%+v", len(f.launched), w.state.CLI["source"])
	}
}

func TestCLIRefreshStartupObservationDeadlineStopsWithoutRespawn(t *testing.T) {
	w, f, _, target := acceptanceRefreshRig(t)
	f.sessions["source"].Status.Process = status.ProcessExited
	child := runningCodex("child", identityA, status.TurnIdle, migratedConversationID)
	child.ResumedFrom, child.CLIIdentity = "source", target
	f.add(child)
	w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshWaiting, ReplacementID: child.ID, HealthDeadline: time.Now().Add(-time.Minute)}
	w.state.Candidates["source"] = child.ID
	w.state.Killed["source"] = true
	w.cliObserve = func(string) *persist.CLIIdentity { return nil }
	for i := 0; i < 4; i++ {
		w.tick()
	}
	if w.state.CLI["source"].State != cliRefreshBlocked || len(f.launched) != 0 || len(f.deleted) != 0 {
		t.Fatalf("uncertain startup did not hold for manual recovery: %+v", w.state.CLI["source"])
	}
}

func TestCLIRefreshCorruptSettingsAndStateFailClosed(t *testing.T) {
	for _, raw := range []string{"null", "{}", `{"disabled":"false"}`, "{broken"} {
		t.Run("settings="+raw, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, cliRefreshSettingsFile), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if !CLIRefreshDisabled(dir) {
				t.Fatal("ambiguous settings enabled destruction")
			}
		})
	}
	for _, raw := range []string{"null", "{broken", `{"identities":{},"cli_refresh":{"source":{"agent_type":"codex","state":"invented"}}}`} {
		t.Run("state="+raw, func(t *testing.T) {
			w, f, _, _ := acceptanceRefreshRig(t)
			if err := os.WriteFile(filepath.Join(w.stateDir, authWatchStateFile), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			w.state, err = loadAuthWatchStateChecked(w.stateDir)
			if err == nil {
				t.Fatal("corrupt state was silently reset")
			}
			w.stateErr = err
			w.tick()
			if len(f.killed) != 0 || len(f.launched) != 0 {
				t.Fatal("corrupt state authorized lifecycle effects")
			}
			report, err := CLIRefreshStatus(w.stateDir)
			if err == nil || !report.Frozen {
				t.Fatal("status did not explain frozen coordinator")
			}
		})
	}
}
