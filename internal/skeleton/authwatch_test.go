package skeleton

// The ADR-024 auth watcher's sweep logic, pinned pure: every seam is a fake and
// ticks are driven by hand (the run goroutine is never started), so each test
// is one deterministic pass over an authored roster.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	identityA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	identityB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// authFake is the roster + action recorder behind every watcher seam. kill
// marks the session exited immediately (the daemon monitor's record, without
// the daemon); launch registers a fresh session stamped with the CURRENT
// identity, exactly as the production stamp does.
type authFake struct {
	identity  string
	sessions  map[string]*persist.Meta
	killed    []string
	launched  []daemon.LaunchSpec
	deleted   []string
	launchErr error
	killErr   error
	launchN   int
}

func newAuthFake(identity string) *authFake {
	return &authFake{identity: identity, sessions: map[string]*persist.Meta{}}
}

func (f *authFake) add(m persist.Meta) { c := m; f.sessions[m.ID] = &c }

func (f *authFake) list() []persist.Meta {
	var out []persist.Meta
	for _, m := range f.sessions {
		out = append(out, *m)
	}
	return out
}

func (f *authFake) get(local string) (persist.Meta, bool) {
	m, ok := f.sessions[local]
	if !ok {
		return persist.Meta{}, false
	}
	return *m, true
}

func (f *authFake) kill(local string) error {
	if f.killErr != nil {
		return f.killErr
	}
	f.killed = append(f.killed, local)
	if m, ok := f.sessions[local]; ok {
		m.Status.Process = status.ProcessExited
	}
	return nil
}

func (f *authFake) launch(spec daemon.LaunchSpec) (persist.Meta, error) {
	if f.launchErr != nil {
		return persist.Meta{}, f.launchErr
	}
	f.launched = append(f.launched, spec)
	f.launchN++
	m := persist.Meta{
		ID: "fresh" + string(rune('0'+f.launchN)), AgentType: spec.AgentType,
		Name: spec.Name, Cwd: spec.Cwd, AuthIdentity: f.identity,
		Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle},
	}
	f.add(m)
	return m, nil
}

func (f *authFake) remove(local string) error {
	f.deleted = append(f.deleted, local)
	delete(f.sessions, local)
	return nil
}

// testWatcher builds a watcher over the fake WITHOUT starting the run
// goroutine; tests call tick() by hand.
func testWatcher(t *testing.T, f *authFake) *authWatcher {
	t.Helper()
	return &authWatcher{
		stateDir:   t.TempDir(),
		endpointID: "ep-test01",
		agents:     []string{"codex"},
		identity:   func(string) string { return f.identity },
		list:       f.list, get: f.get, kill: f.kill, launch: f.launch, remove: f.remove,
		sleep:    func(time.Duration) {},
		exitWait: time.Second, exitPoll: time.Millisecond,
		state:  authWatchState{Identities: map[string]string{}, Pending: map[string][]string{}},
		warned: map[string]bool{},
		stop:   make(chan struct{}),
	}
}

func runningCodex(id, stamp string, turn status.Turn, convID string) persist.Meta {
	return persist.Meta{
		ID: id, AgentType: "codex", Name: "n-" + id, Cwd: "/work/" + id,
		ConversationID: convID, AuthIdentity: stamp,
		Status: status.Status{Process: status.ProcessRunning, Turn: turn},
	}
}

func TestFirstObservationBaselinesAndRecyclesNothing(t *testing.T) {
	f := newAuthFake(identityA)
	f.add(runningCodex("s1", "", status.TurnIdle, "01a05600-0000-7000-8000-000000000001"))
	w := testWatcher(t, f)
	w.tick()
	if len(f.killed) != 0 || len(f.launched) != 0 {
		t.Fatalf("first observation acted (killed %v, launched %v); it must only baseline", f.killed, f.launched)
	}
	if w.state.Identities["codex"] != identityA {
		t.Fatalf("baseline = %q, want %q", w.state.Identities["codex"], identityA)
	}
	// And the baseline is durable: a fresh load sees it.
	if got := loadAuthWatchState(w.stateDir).Identities["codex"]; got != identityA {
		t.Fatalf("persisted baseline = %q, want %q", got, identityA)
	}
}

func TestRoutineTicksUnderOneIdentityRecycleNothing(t *testing.T) {
	f := newAuthFake(identityA)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000001"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	w.tick()
	if len(f.killed) != 0 || len(f.launched) != 0 || len(f.deleted) != 0 {
		t.Fatalf("nothing changed, yet the watcher acted: killed %v launched %v deleted %v", f.killed, f.launched, f.deleted)
	}
}

func TestReloginRecyclesAnIdleStaleSession(t *testing.T) {
	const conv = "01a05600-0000-7000-8000-000000000001"
	f := newAuthFake(identityB) // the disk now says account B
	f.add(runningCodex("s1", identityA, status.TurnIdle, conv))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA // ...but we last saw A
	w.tick()

	if len(f.killed) != 1 || f.killed[0] != "s1" {
		t.Fatalf("killed %v; want exactly s1", f.killed)
	}
	if len(f.launched) != 1 {
		t.Fatalf("launched %d sessions; want 1", len(f.launched))
	}
	spec := f.launched[0]
	if spec.Options[protocol.OptionResumeFrom] != "ep-test01/s1" {
		t.Errorf("resume_from = %q; want ep-test01/s1", spec.Options[protocol.OptionResumeFrom])
	}
	if spec.Name != "n-s1" || spec.Cwd != "/work/s1" || spec.AgentType != "codex" {
		t.Errorf("the resumed session lost its identity: %+v", spec)
	}
	if spec.ClientEnv != nil {
		t.Errorf("ClientEnv = %v; must stay nil so coreAPI resolves the daemon-saved env", spec.ClientEnv)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "s1" {
		t.Errorf("deleted %v; want the stale row s1 (the owner's one-row-per-conversation rule)", f.deleted)
	}
	if len(w.state.Pending["codex"]) != 0 {
		t.Errorf("pending %v after a completed recycle; want empty", w.state.Pending["codex"])
	}
	if w.state.Identities["codex"] != identityB {
		t.Errorf("baseline = %q, want %q", w.state.Identities["codex"], identityB)
	}
}

func TestReloginIncludesPreFeatureUnstampedSessions(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("old1", "", status.TurnIdle, "01a05600-0000-7000-8000-000000000002"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if len(f.killed) != 1 || f.killed[0] != "old1" {
		t.Fatalf("killed %v; an unstamped session predates an OBSERVED change and must recycle", f.killed)
	}
}

func TestUnstampedSessionsAreNeverTouchedWithoutAnObservedChange(t *testing.T) {
	f := newAuthFake(identityA)
	f.add(runningCodex("old1", "", status.TurnIdle, "01a05600-0000-7000-8000-000000000002"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA // no change this tick
	w.tick()
	if len(f.killed) != 0 {
		t.Fatalf("killed %v; an unstamped session with no observed change is ambiguous and must hold", f.killed)
	}
}

func TestStampedMismatchIsSweptEvenWithoutAnObservedChange(t *testing.T) {
	// A re-login that happened while the daemon was down: the persisted baseline
	// already matches the disk, but a session's stamp disagrees. The stamp is
	// ground truth.
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000003"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityB
	w.tick()
	if len(f.killed) != 1 || f.killed[0] != "s1" {
		t.Fatalf("killed %v; a stamped mismatch must recycle even when the change itself was never observed", f.killed)
	}
}

func TestAWorkingSessionIsDeferredUntilIdle(t *testing.T) {
	const conv = "01a05600-0000-7000-8000-000000000004"
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnActive, conv))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if len(f.killed) != 0 {
		t.Fatalf("killed %v; a mid-turn session must be deferred, never interrupted", f.killed)
	}
	if got := w.state.Pending["codex"]; len(got) != 1 || got[0] != "s1" {
		t.Fatalf("pending = %v; the deferred session must stay pending", got)
	}
	// The turn ends; the next tick recycles.
	f.sessions["s1"].Status.Turn = status.TurnIdle
	w.tick()
	if len(f.killed) != 1 || len(f.launched) != 1 {
		t.Fatalf("after the turn ended: killed %v launched %d; want the recycle", f.killed, len(f.launched))
	}
}

func TestAnUnknownTurnDefersLikeAWorkingOne(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnUnknown, "01a05600-0000-7000-8000-000000000005"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if len(f.killed) != 0 {
		t.Fatalf("killed %v; an unclassified turn gates conservatively", f.killed)
	}
}

func TestNoConversationIDIsLeftRunningAndWarnedOnce(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, ""))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	w.tick()
	if len(f.killed) != 0 {
		t.Fatalf("killed %v; without a conversation id a kill destroys what a MANUAL resume needs too", f.killed)
	}
	if got := w.state.Pending["codex"]; len(got) != 1 || got[0] != "s1" {
		t.Fatalf("pending = %v; the unresumable session stays visible in state", got)
	}
	if !w.warned["s1"] {
		t.Fatal("the once-only warning was never recorded")
	}
}

func TestLoggedOutWindowHoldsEverything(t *testing.T) {
	f := newAuthFake("") // credentials missing or unparseable: identity unknown
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000006"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if len(f.killed) != 0 {
		t.Fatalf("killed %v during the logged-out window; unknown identity must hold", f.killed)
	}
	if w.state.Identities["codex"] != identityA {
		t.Fatalf("baseline moved to %q on an unknown identity", w.state.Identities["codex"])
	}
}

func TestAFreshSessionUnderTheNewIdentityIsUntouched(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("stale", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000007"))
	f.add(runningCodex("fresh", identityB, status.TurnIdle, "01a05600-0000-7000-8000-000000000008"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if len(f.killed) != 1 || f.killed[0] != "stale" {
		t.Fatalf("killed %v; only the stale session may recycle", f.killed)
	}
}

func TestDisabledSettingsHoldTheSweep(t *testing.T) {
	for name, body := range map[string]string{
		"explicit": `{"disabled": true}`,
		"garbage":  `{"disabled": tru`, // ambiguous config fails toward inaction
	} {
		t.Run(name, func(t *testing.T) {
			f := newAuthFake(identityB)
			f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000009"))
			w := testWatcher(t, f)
			w.state.Identities["codex"] = identityA
			if err := os.WriteFile(filepath.Join(w.stateDir, authWatchSettingsFile), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			w.tick()
			if len(f.killed) != 0 {
				t.Fatalf("killed %v with the watcher disabled", f.killed)
			}
		})
	}
}

func TestAFailedResumeLeavesTheEndedRowAndStopsRetrying(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-00000000000a"))
	f.launchErr = errors.New("agent binary codex not found")
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if len(f.killed) != 1 {
		t.Fatalf("killed %v; want the kill to have happened", f.killed)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("deleted %v after a FAILED resume; the ended row must remain for a manual resume", f.deleted)
	}
	if got := w.state.Pending["codex"]; len(got) != 0 {
		t.Fatalf("pending = %v; a failed resume must not kill-loop", got)
	}
}

func TestAFailedKillIsRetriedNextTick(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-00000000000b"))
	f.killErr = errors.New("transient")
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if got := w.state.Pending["codex"]; len(got) != 1 || got[0] != "s1" {
		t.Fatalf("pending = %v; a failed kill must stay pending", got)
	}
	f.killErr = nil
	w.tick()
	if len(f.launched) != 1 {
		t.Fatalf("launched %d after the kill recovered; want the recycle to complete", len(f.launched))
	}
}

func TestAPendingSweepSurvivesARestart(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnActive, "01a05600-0000-7000-8000-00000000000c"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick() // deferred: mid-turn -- but the pending set is already durable

	// A new incarnation over the same state dir picks the sweep back up.
	f.sessions["s1"].Status.Turn = status.TurnIdle
	w2 := testWatcher(t, f)
	w2.stateDir = w.stateDir
	w2.state = loadAuthWatchState(w.stateDir)
	if got := w2.state.Pending["codex"]; len(got) != 1 || got[0] != "s1" {
		t.Fatalf("reloaded pending = %v; the sweep must survive a restart", got)
	}
	w2.tick()
	if len(f.launched) != 1 {
		t.Fatalf("launched %d after restart; want the deferred recycle to complete", len(f.launched))
	}
}

func TestStateFileIsPrivate(t *testing.T) {
	f := newAuthFake(identityA)
	w := testWatcher(t, f)
	w.tick() // baselines, which persists
	info, err := os.Stat(filepath.Join(w.stateDir, authWatchStateFile))
	if err != nil {
		t.Fatalf("state file missing after a baseline: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state file mode = %o, want 600", perm)
	}
	// And it is valid JSON with the identity in it.
	raw, _ := os.ReadFile(filepath.Join(w.stateDir, authWatchStateFile))
	var st authWatchState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("state file unparseable: %v", err)
	}
}
