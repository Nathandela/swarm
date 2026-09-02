package skeleton

// The ADR-024 auth watcher's sweep logic, pinned pure: every seam is a fake and
// ticks are driven by hand (the run goroutine is never started), so each test
// is one deterministic pass over an authored roster. The audit-round guarantees
// are pinned by name: a kill the watcher performed is a resume OWED across
// timeouts, restarts and crashes (H1); the resume launches from the session's
// own environment and lineage (H3/M2); feasibility precedes destruction (H3);
// worktree sessions are never auto-recycled (C1); interactions defer like
// turns (M3); the first tick after start never begins a kill (codex 6).

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
// marks the session exited immediately unless killNoExit models a slow monitor;
// launch registers a fresh session stamped with the CURRENT identity, exactly
// as the production stamp does.
type authFake struct {
	identity   string
	sessions   map[string]*persist.Meta
	killed     []string
	launched   []daemon.LaunchSpec
	deleted    []string
	launchErr  error
	killErr    error
	killNoExit bool
	launchN    int
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
	if m, ok := f.sessions[local]; ok && !f.killNoExit {
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
		Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone},
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
// goroutine; tests call tick() by hand. settled defaults to true (the sweep
// under test is the steady state); the first-tick hold has its own test.
func testWatcher(t *testing.T, f *authFake) *authWatcher {
	t.Helper()
	return &authWatcher{
		stateDir:   t.TempDir(),
		endpointID: "ep-test01",
		agents:     []string{"codex"},
		identity:   func(string) string { return f.identity },
		list:       f.list, get: f.get, kill: f.kill, launch: f.launch, remove: f.remove,
		resolve:  func(string, []string) (string, error) { return "/abs/codex", nil },
		pause:    func(time.Duration) {},
		exitWait: time.Second, exitPoll: time.Millisecond,
		state:   authWatchState{Identities: map[string]string{}, Pending: map[string][]string{}, Killed: map[string]bool{}},
		settled: true,
		warned:  map[string]bool{},
		stop:    make(chan struct{}),
	}
}

func runningCodex(id, stamp string, turn status.Turn, convID string) persist.Meta {
	return persist.Meta{
		ID: id, AgentType: "codex", Name: "n-" + id, Cwd: "/work/" + id,
		Env:            []string{"PATH=/agent/bin", "HOME=/agent/home"},
		ConversationID: convID, AuthIdentity: stamp,
		SpawnedFrom: "parent-" + id, SpawnIntent: "delegate",
		Status: status.Status{Process: status.ProcessRunning, Turn: turn, Interaction: status.InteractionNone},
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
	if len(f.deleted) != 1 || f.deleted[0] != "s1" {
		t.Errorf("deleted %v; want the stale row s1 (the owner's one-row-per-conversation rule)", f.deleted)
	}
	if len(w.state.Pending["codex"]) != 0 {
		t.Errorf("pending %v after a completed recycle; want empty", w.state.Pending["codex"])
	}
	if len(w.state.Killed) != 0 {
		t.Errorf("killed marks %v after a completed recycle; want none", w.state.Killed)
	}
	if w.state.Identities["codex"] != identityB {
		t.Errorf("baseline = %q, want %q", w.state.Identities["codex"], identityB)
	}
}

// TestARecycleCarriesTheSessionsEnvAndLineage pins audit H3/M2: the resume
// launches from the SESSION's saved environment (its PATH resolved this agent;
// its HOME/API key are what the conversation ran under), and its handoff
// lineage survives under the new id.
func TestARecycleCarriesTheSessionsEnvAndLineage(t *testing.T) {
	src := runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000002")
	src.Supervision = "passive"
	f := newAuthFake(identityB)
	f.add(src)
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if len(f.launched) != 1 {
		t.Fatalf("launched %d; want 1", len(f.launched))
	}
	spec := f.launched[0]
	if !reflect.DeepEqual(spec.ClientEnv, src.Env) {
		t.Errorf("ClientEnv = %v; want the session's own saved env %v", spec.ClientEnv, src.Env)
	}
	if spec.SpawnedFrom != src.SpawnedFrom || spec.SpawnIntent != src.SpawnIntent || spec.Supervision != src.Supervision {
		t.Errorf("lineage lost: %+v", spec)
	}
}

// TestAnUnresolvableBinaryHoldsBeforeTheKill pins audit H3's other half:
// feasibility precedes destruction -- no kill may happen when the resume's
// binary cannot resolve on the session's environment.
func TestAnUnresolvableBinaryHoldsBeforeTheKill(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000003"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.resolve = func(string, []string) (string, error) { return "", errors.New("codex not on PATH") }
	w.tick()
	w.tick()
	if len(f.killed) != 0 {
		t.Fatalf("killed %v although the resume could never launch; feasibility must precede destruction", f.killed)
	}
	if got := w.state.Pending["codex"]; len(got) != 1 {
		t.Fatalf("pending = %v; the held session stays owed", got)
	}
}

// TestAKillTimeoutKeepsTheResumeOwed pins audit H1: an exit-wait timeout is
// not a drop -- the killed mark makes the ended session a resume OWED, and a
// later tick completes it.
func TestAKillTimeoutKeepsTheResumeOwed(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000004"))
	f.killNoExit = true // the monitor is slow: the exit is not recorded in time
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.exitWait = -time.Second // the deadline is already past
	w.tick()
	if len(f.killed) != 1 {
		t.Fatalf("killed %v; want the kill to have happened", f.killed)
	}
	if len(f.launched) != 0 {
		t.Fatalf("launched %d during the timeout tick; want 0", len(f.launched))
	}
	if !w.state.Killed["s1"] {
		t.Fatal("the kill was not recorded as owed")
	}
	// The exit lands late; the next tick completes the owed resume.
	f.sessions["s1"].Status.Process = status.ProcessExited
	w.tick()
	if len(f.launched) != 1 || len(f.deleted) != 1 {
		t.Fatalf("after the late exit: launched %d deleted %v; want the owed resume to complete", len(f.launched), f.deleted)
	}
	if len(w.state.Killed) != 0 {
		t.Fatalf("killed marks %v after completion; want none", w.state.Killed)
	}
}

// TestACrashBetweenKillAndResumeIsCompletedByTheNextIncarnation pins the crash
// half of audit H1: the durable killed mark makes the next daemon's first tick
// complete the resume instead of dropping the session as "ended by other hands"
// -- even before that incarnation is settled (an owed resume is not a new kill).
func TestACrashBetweenKillAndResumeIsCompletedByTheNextIncarnation(t *testing.T) {
	f := newAuthFake(identityB)
	dead := runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000005")
	dead.Status.Process = status.ProcessExited // the prior incarnation's kill landed
	f.add(dead)
	w := testWatcher(t, f)
	w.settled = false // a fresh incarnation's very first tick
	w.state.Identities["codex"] = identityB
	w.state.Pending["codex"] = []string{"s1"}
	w.state.Killed["s1"] = true
	w.tick()
	if len(f.launched) != 1 {
		t.Fatalf("launched %d; the next incarnation must complete the owed resume", len(f.launched))
	}
	if len(f.deleted) != 1 || f.deleted[0] != "s1" {
		t.Fatalf("deleted %v; want the stale row removed after the owed resume", f.deleted)
	}
}

// TestASessionEndedByOtherHandsIsNotResurrected: without the killed mark, an
// ended pending session was ended by someone else and stays theirs.
func TestASessionEndedByOtherHandsIsNotResurrected(t *testing.T) {
	f := newAuthFake(identityB)
	dead := runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000006")
	dead.Status.Process = status.ProcessExited
	f.add(dead)
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityB
	w.state.Pending["codex"] = []string{"s1"}
	w.tick()
	if len(f.launched) != 0 {
		t.Fatalf("launched %d; a session ended by other hands is not ours to resurrect", len(f.launched))
	}
	if len(w.state.Pending["codex"]) != 0 {
		t.Fatalf("pending = %v; want the entry dropped", w.state.Pending["codex"])
	}
}

// TestTheFirstTickAfterStartNeverStartsAKill pins codex finding 6: reconciled
// sessions are seeded from persisted status, so the first pass never begins a
// kill; the second does.
func TestTheFirstTickAfterStartNeverStartsAKill(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000007"))
	w := testWatcher(t, f)
	w.settled = false
	w.state.Identities["codex"] = identityA
	w.tick()
	if len(f.killed) != 0 {
		t.Fatalf("killed %v on the first tick after start; persisted status may lag reality", f.killed)
	}
	w.tick()
	if len(f.killed) != 1 {
		t.Fatalf("killed %v on the settled tick; want the recycle", f.killed)
	}
}

func TestReloginIncludesPreFeatureUnstampedSessions(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("old1", "", status.TurnIdle, "01a05600-0000-7000-8000-000000000008"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if len(f.killed) != 1 || f.killed[0] != "old1" {
		t.Fatalf("killed %v; an unstamped session predates an OBSERVED change and must recycle", f.killed)
	}
}

func TestUnstampedSessionsAreNeverTouchedWithoutAnObservedChange(t *testing.T) {
	f := newAuthFake(identityA)
	f.add(runningCodex("old1", "", status.TurnIdle, "01a05600-0000-7000-8000-000000000009"))
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
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-00000000000a"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityB
	w.tick()
	if len(f.killed) != 1 || f.killed[0] != "s1" {
		t.Fatalf("killed %v; a stamped mismatch must recycle even when the change itself was never observed", f.killed)
	}
}

func TestAWorkingSessionIsDeferredUntilIdle(t *testing.T) {
	const conv = "01a05600-0000-7000-8000-00000000000b"
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
	f.add(runningCodex("s1", identityA, status.TurnUnknown, "01a05600-0000-7000-8000-00000000000c"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if len(f.killed) != 0 {
		t.Fatalf("killed %v; an unclassified turn gates conservatively", f.killed)
	}
}

// TestAnInteractionDefersLikeAWorkingTurn pins audit M3: a permission prompt
// rides an IDLE turn, and killing it would discard the pending decision.
func TestAnInteractionDefersLikeAWorkingTurn(t *testing.T) {
	m := runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-00000000000d")
	m.Status.Interaction = status.InteractionPermission
	f := newAuthFake(identityB)
	f.add(m)
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if len(f.killed) != 0 {
		t.Fatalf("killed %v; a session awaiting an approval must be deferred", f.killed)
	}
}

func TestAControllerOrContextGuardDefersAuthRecycle(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-00000000001d"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.unsafe = func(local string) bool { return local == "s1" }
	w.tick()
	if len(f.killed) != 0 {
		t.Fatalf("killed %v while the session had an active controller/effect window", f.killed)
	}
	if got := w.state.Pending["codex"]; len(got) != 1 || got[0] != "s1" {
		t.Fatalf("pending = %v; the unsafe session must remain deferred", got)
	}
}

func TestAuthRecycleRechecksUnsafeInsideTheComposerFence(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-00000000001e"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	checks := 0
	w.unsafe = func(string) bool {
		checks++
		return checks >= 2 // becomes controlled after the outer classification
	}
	fenced := false
	w.withRecycleFence = func(local string, attempt func() error) error {
		if local != "s1" {
			t.Fatalf("fenced %q, want s1", local)
		}
		fenced = true
		return attempt()
	}
	w.tick()
	if !fenced || checks < 2 {
		t.Fatalf("fenced=%v unsafe checks=%d; want a second check inside the fence", fenced, checks)
	}
	if len(f.killed) != 0 {
		t.Fatalf("killed %v after the session became unsafe at the final boundary", f.killed)
	}
	if w.state.Killed["s1"] {
		t.Fatal("the refused recycle left a durable kill claim behind")
	}
}

func TestAuthRecycleFenceSerializesWithTheRealComposerLane(t *testing.T) {
	d := &Daemon{}
	lane := d.composerLaneFor("s1")
	lane.enter() // an already-admitted phone send owns the queue head
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_ = d.withAuthRecycleFence("s1", func() error {
			close(entered)
			return nil
		})
		close(done)
	}()
	select {
	case <-entered:
		t.Fatal("auth recycle entered its kill boundary while a composer send owned the lane")
	case <-time.After(50 * time.Millisecond):
	}
	lane.leave()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("auth recycle did not acquire the lane after the composer send left")
	}
}

// TestWorktreeSessionsAreNeverAutoRecycled pins audit C1: the resume cannot
// follow the conversation into its checkout, and the auto-delete would remove
// the worktree -- with uncommitted agent work -- by force.
func TestWorktreeSessionsAreNeverAutoRecycled(t *testing.T) {
	m := runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-00000000000e")
	m.LaunchOptions = map[string]string{protocol.OptionWorktree: "true"}
	f := newAuthFake(identityB)
	f.add(m)
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	w.tick()
	if len(f.killed) != 0 {
		t.Fatalf("killed %v; a worktree-isolated session is manual-only", f.killed)
	}
	if got := w.state.Pending["codex"]; len(got) != 1 {
		t.Fatalf("pending = %v; the held session stays visible in state", got)
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
	if !w.warned["noconv:s1"] {
		t.Fatal("the once-only warning was never recorded")
	}
}

func TestLoggedOutWindowHoldsEverything(t *testing.T) {
	f := newAuthFake("") // credentials missing or unparseable: identity unknown
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-00000000000f"))
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
	f.add(runningCodex("stale", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000010"))
	f.add(runningCodex("fresh", identityB, status.TurnIdle, "01a05600-0000-7000-8000-000000000011"))
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
			f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000012"))
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
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000013"))
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
	if len(w.state.Killed) != 0 {
		t.Fatalf("killed marks %v after the drop; want none", w.state.Killed)
	}
}

func TestAFailedKillIsRetriedNextTick(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000014"))
	f.killErr = errors.New("transient")
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.tick()
	if got := w.state.Pending["codex"]; len(got) != 1 || got[0] != "s1" {
		t.Fatalf("pending = %v; a failed kill must stay pending", got)
	}
	if len(w.state.Killed) != 0 {
		t.Fatalf("killed marks %v after a kill that never happened; want none", w.state.Killed)
	}
	f.killErr = nil
	w.tick()
	if len(f.launched) != 1 {
		t.Fatalf("launched %d after the kill recovered; want the recycle to complete", len(f.launched))
	}
}

func TestAPendingSweepSurvivesARestart(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnActive, "01a05600-0000-7000-8000-000000000015"))
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

// TestCredentialsReadIsConservative pins codex finding 9: non-regular files
// and implausibly large files are refused before any read.
func TestCredentialsReadIsConservative(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, 8), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, authCredentialsMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentials(big); err == nil {
		t.Fatal("an implausibly large credentials file was read")
	}
	if _, err := readCredentials(dir); err == nil {
		t.Fatal("a directory was read as credentials")
	}
	ok := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(ok, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentials(ok); err != nil {
		t.Fatalf("a small regular file was refused: %v", err)
	}
}
