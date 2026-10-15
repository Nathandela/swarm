package skeleton

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// ADR-032 D6: rotation alone never moves an unmanaged discussion onto an
// account, including the automated relaunch cli-refresh owes after its kill.
func TestCLIRefresh_OwnerRelaunchStaysUnboundWhenRotationIsOn(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	accountTestAdmit(t, m, accountTestCandidate(t, m, "claude", "rotation-account"))
	if _, err := m.store.SetEnabled(accountTestRegistry(t, m).Revision, "claude", true); err != nil {
		t.Fatal(err)
	}
	old := &persist.CLIIdentity{Path: "/abs/claude", Version: "2.1.299", Fingerprint: "old"}
	target := &persist.CLIIdentity{Path: "/abs/claude", Version: "2.1.300", Fingerprint: "new"}
	source := runningCodex("source", identityA, status.TurnIdle, migratedConversationID)
	source.AgentType, source.CLIIdentity, source.Cwd, source.CreatedAt = "claude", old, m.stateRoot, time.Now()
	source.Status.Process = status.ProcessExited
	store, err := persist.NewStore(m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(source); err != nil {
		t.Fatal(err)
	}
	api := &coreAPI{core: accountTestCore(t, m, nil), accounts: m, endpointID: testEndpoint}
	f := newAuthFake(identityA)
	f.add(source)
	w := testWatcher(t, f)
	w.state = emptyAuthWatchState()
	w.state.CLI["source"] = cliRefreshRecord{AgentType: "claude", Target: *target, State: cliRefreshPending}
	w.state.Killed["source"] = true
	w.cliProbe = func(string, string, []string, string) (*persist.CLIIdentity, error) { return target, nil }
	var bound []daemon.LaunchSpec
	w.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		if err := api.bindAccountLaunch(&spec); err != nil {
			return persist.Meta{}, err
		}
		bound = append(bound, spec)
		return persist.Meta{}, errors.New("fixture stops before spawn")
	}
	w.resumeCLIEnded("claude", source)
	if len(bound) != 1 || bound[0].ExpectedCLIIdentity == nil || *bound[0].ExpectedCLIIdentity != *target {
		t.Fatalf("cli-refresh relaunch did not reach the account seam with its target: %+v", bound)
	}
	if bound[0].AccountBinding != nil {
		t.Fatalf("cli-refresh owner relaunch was enrolled on account %s", bound[0].AccountBinding.AccountID)
	}
}

// A relaunch that keeps failing must stop, release the input lock and leave
// the ended source for the user instead of retrying forever.
func TestCLIRefresh_RelaunchFailuresAreCappedAndReleaseTheLock(t *testing.T) {
	w, f, _, target := acceptanceRefreshRig(t)
	f.sessions["source"].Status.Process = status.ProcessExited
	w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshPending}
	w.state.Killed["source"] = true
	now := time.Now()
	w.now = func() time.Time { return now }
	released := 0
	w.clearRecycle = func(id string) {
		if id == "source" {
			released++
		}
	}
	calls := 0
	w.launch = func(daemon.LaunchSpec) (persist.Meta, error) {
		calls++
		return persist.Meta{}, errors.New("launch: CLI installation changed before refresh")
	}
	for i := 0; i < 2; i++ {
		w.tick()
		now = now.Add(refreshRetryMax + time.Second)
	}
	if calls != 2 || w.state.CLI["source"].State == cliRefreshBlocked || released != 0 {
		t.Fatalf("blocked before the cap: calls=%d record=%+v released=%d", calls, w.state.CLI["source"], released)
	}
	w.tick()
	rec := w.state.CLI["source"]
	if calls != 3 || rec.State != cliRefreshBlocked || !strings.Contains(rec.LastError, "CLI installation changed before refresh") {
		t.Fatalf("third consecutive failure did not block with its reason: calls=%d record=%+v", calls, rec)
	}
	if released != 1 || w.state.Killed["source"] {
		t.Fatalf("blocked refresh kept the input lock or kill claim: released=%d killed=%v", released, w.state.Killed["source"])
	}
	loaded, err := loadAuthWatchStateChecked(w.stateDir)
	if err != nil || loaded.CLI["source"].State != cliRefreshBlocked || loaded.CLI["source"].LastError != rec.LastError {
		t.Fatalf("blocked state not durable: %+v / %v", loaded.CLI["source"], err)
	}
	for i := 0; i < 3; i++ {
		now = now.Add(refreshRetryMax + time.Second)
		w.tick()
	}
	if calls != 3 || len(f.deleted) != 0 {
		t.Fatalf("blocked refresh kept relaunching or removed the source: calls=%d deleted=%v", calls, f.deleted)
	}
	if src, ok := f.get("source"); !ok || src.RosterHidden || src.Status.Process != status.ProcessExited {
		t.Fatalf("ended source is no longer resumable by the user: %+v", src)
	}
}

// A block that cannot be made durable is not a block: the failed relaunch
// keeps its backoff and the kill claim, and nothing claims it was blocked.
func TestCLIRefresh_UndurableRelaunchBlockKeepsBackoff(t *testing.T) {
	w, f, _, target := acceptanceRefreshRig(t)
	f.sessions["source"].Status.Process = status.ProcessExited
	w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshPending}
	w.state.Killed["source"] = true
	now := time.Now()
	w.now = func() time.Time { return now }
	calls := 0
	w.launch = func(daemon.LaunchSpec) (persist.Meta, error) {
		calls++
		return persist.Meta{}, errors.New("launch: CLI installation changed before refresh")
	}
	for i := 0; i < 2; i++ {
		w.tick()
		now = now.Add(refreshRetryMax + time.Second)
	}
	var sink bytes.Buffer
	log.SetOutput(&sink)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	w.writeState = func(string, []byte) (bool, error) { return false, errors.New("disk full before rename") }
	w.tick()
	if calls != 3 || w.state.CLI["source"].State == cliRefreshBlocked || !w.state.Killed["source"] {
		t.Fatalf("undurable block lost the obligation: calls=%d record=%+v killed=%v", calls, w.state.CLI["source"], w.state.Killed["source"])
	}
	if strings.Contains(sink.String(), "refresh blocked") {
		t.Fatalf("logged a block that was not persisted: %s", sink.String())
	}
	w.tick()
	if calls != 3 || !w.state.Retries["source"].NextAttempt.After(now) {
		t.Fatalf("undurable block dropped the backoff: calls=%d retry=%+v", calls, w.state.Retries["source"])
	}
	w.writeState = nil
	now = now.Add(refreshRetryMax + time.Second)
	w.tick()
	if calls != 4 || w.state.CLI["source"].State != cliRefreshBlocked || w.state.Killed["source"] {
		t.Fatalf("recovered persistence did not block: calls=%d record=%+v", calls, w.state.CLI["source"])
	}
}

// A launch that returned a replacement is validated by checkCLIReplacement,
// never blocked away by the relaunch cap, even when it also returned an error.
func TestCLIRefresh_RelaunchCapSparesALiveReplacement(t *testing.T) {
	w, f, _, target := acceptanceRefreshRig(t)
	f.sessions["source"].Status.Process = status.ProcessExited
	w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshPending}
	w.state.Killed["source"] = true
	w.state.Retries["source"] = recycleRetry{Attempts: maxCLIRefreshRelaunchFailures - 1}
	launch := w.launch
	w.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		m, err := launch(spec)
		if err != nil {
			return m, err
		}
		return m, errors.New("launch: CLI installation changed during refresh")
	}
	w.tick()
	rec := w.state.CLI["source"]
	if rec.State == cliRefreshBlocked || rec.ReplacementID == "" {
		t.Fatalf("relaunch cap blocked a live replacement: %+v", rec)
	}
}
