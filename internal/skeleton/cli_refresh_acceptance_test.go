package skeleton

import (
	"fmt"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func acceptanceRefreshRig(t *testing.T) (*authWatcher, *authFake, *persist.CLIIdentity, *persist.CLIIdentity) {
	t.Helper()
	f := newAuthFake(identityA)
	w := testWatcher(t, f)
	w.state = emptyAuthWatchState()
	old := &persist.CLIIdentity{Path: "/abs/codex", Version: "0.100.0", Fingerprint: "old"}
	next := &persist.CLIIdentity{Path: "/abs/codex", Version: "0.101.0", Fingerprint: "new"}
	m := runningCodex("source", identityA, status.TurnIdle, migratedConversationID)
	m.CLIIdentity = old
	f.add(m)
	w.cliProbe = func(string, string, []string, string) (*persist.CLIIdentity, error) { return next, nil }
	w.cliObserve = func(id string) *persist.CLIIdentity {
		if id == "source" {
			return old
		}
		return next
	}
	w.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		m, err := f.launch(spec)
		if err != nil {
			return m, err
		}
		m.ResumedFrom = "source"
		m.ConversationID = migratedConversationID
		m.CLIIdentity = spec.ExpectedCLIIdentity
		f.add(m)
		return m, nil
	}
	return w, f, old, next
}

func TestCLIRefreshAcceptanceDiscoveryFairness(t *testing.T) {
	w, f, old, _ := acceptanceRefreshRig(t)
	delete(f.sessions, "source")
	seen := map[string]bool{}
	for i := 0; i < maxCLIRefreshProbesPerTick+3; i++ {
		m := runningCodex(fmt.Sprintf("s%02d", i), identityA, status.TurnIdle, migratedConversationID)
		m.CLIIdentity = old
		f.add(m)
	}
	w.cliProbe = func(_, _ string, _ []string, cwd string) (*persist.CLIIdentity, error) {
		seen[cwd] = true
		return old, nil
	}
	w.cliObserve = func(string) *persist.CLIIdentity { return old }
	for i := 0; i < 4; i++ {
		w.tick()
	}
	if len(seen) != len(f.sessions) {
		t.Fatalf("probed %d of %d env-distinct sessions after repeated ticks; discovery starved later sessions", len(seen), len(f.sessions))
	}
}

func TestCLIRefreshAcceptancePendingProbeFairness(t *testing.T) {
	w, f, old, target := acceptanceRefreshRig(t)
	delete(f.sessions, "source")
	w.state = emptyAuthWatchState()
	seen := map[string]bool{}
	for i := 0; i < maxCLIRefreshProbesPerTick+3; i++ {
		id := fmt.Sprintf("p%02d", i)
		m := runningCodex(id, identityA, status.TurnActive, migratedConversationID)
		m.CLIIdentity = old
		f.add(m)
		w.state.CLI[id] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshPending}
	}
	w.cliProbe = func(_, _ string, _ []string, cwd string) (*persist.CLIIdentity, error) {
		seen[cwd] = true
		return nil, fmt.Errorf("probe unavailable")
	}
	for i := 0; i < 3; i++ {
		w.workCLIRefresh(true)
	}
	if len(seen) != len(f.sessions) {
		t.Fatalf("pending work probed %d of %d sessions; exhausted budget starved later records", len(seen), len(f.sessions))
	}
}

func TestCLIRefreshAcceptanceChangedCandidateBeforeKill(t *testing.T) {
	w, f, _, next := acceptanceRefreshRig(t)
	f.sessions["source"].Status.Turn = status.TurnActive
	w.tick()
	w.tick()
	newer := &persist.CLIIdentity{Path: next.Path, Version: "0.102.0", Fingerprint: "newer"}
	w.cliProbe = func(string, string, []string, string) (*persist.CLIIdentity, error) { return newer, nil }
	w.cliObserve = func(id string) *persist.CLIIdentity {
		if id == "source" {
			return f.sessions[id].CLIIdentity
		}
		return newer
	}
	f.sessions["source"].Status.Turn = status.TurnIdle
	for i := 0; i < 6; i++ {
		w.tick()
	}
	if len(f.launched) != 1 || f.launched[0].ExpectedCLIIdentity == nil || *f.launched[0].ExpectedCLIIdentity != *newer {
		t.Fatalf("new stable installation not adopted: launches=%+v record=%+v", f.launched, w.state.CLI["source"])
	}
}

func TestCLIRefreshAcceptanceRequiresObservedBaseline(t *testing.T) {
	w, f, _, _ := acceptanceRefreshRig(t)
	w.cliObserve = func(string) *persist.CLIIdentity { return nil }
	for i := 0; i < 5; i++ {
		w.tick()
	}
	if len(f.killed) != 0 {
		t.Fatalf("unobserved baseline was killed: %v", f.killed)
	}
}

func TestCLIRefreshAcceptanceCrashBeforeReplacementCheckpoint(t *testing.T) {
	for _, process := range []status.Process{status.ProcessRunning, status.ProcessExited} {
		t.Run(string(process), func(t *testing.T) {
			w, f, _, target := acceptanceRefreshRig(t)
			f.sessions["source"].Status.Process = status.ProcessExited
			w.state.Killed["source"] = true
			w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshPending}
			child := runningCodex("already-created", identityA, status.TurnIdle, migratedConversationID)
			child.ResumedFrom = "source"
			child.CLIIdentity = target
			child.Status.Process = process
			f.add(child)
			if err := w.saveState(); err != nil {
				t.Fatal(err)
			}
			w.state = loadAuthWatchState(w.stateDir)
			for i := 0; i < 4; i++ {
				w.tick()
			}
			if len(f.launched) != 0 || len(f.deleted) != 0 {
				t.Fatalf("replay repeated/destructively cleaned launch: %+v %+v", f.launched, f.deleted)
			}
			rec := w.state.CLI["source"]
			if rec.ReplacementID != child.ID {
				t.Fatalf("lost recovered replacement checkpoint: %+v", rec)
			}
			want := cliRefreshComplete
			if process != status.ProcessRunning {
				want = cliRefreshBlocked
			}
			if rec.State != want {
				t.Fatalf("state=%s want %s", rec.State, want)
			}
		})
	}
}

func TestCLIRefreshAcceptanceUsesSessionScopedAuthForCLIOnlyReason(t *testing.T) {
	w, f, _, _ := acceptanceRefreshRig(t)
	// The daemon HOME is account A while this saved session HOME is account B.
	// Authwatch may retain a scoped mismatch explanation, but it must not make a
	// CLI-only refresh compare B to A and hold forever.
	f.sessions["source"].AuthIdentity = identityB
	w.sessionIdentity = func(string, []string) string { return identityB }
	for i := 0; i < 6; i++ {
		w.tick()
	}
	if len(f.killed) != 1 {
		t.Fatalf("session-scoped credentials matching the source were compared to daemon HOME: killed=%v", f.killed)
	}
}

func TestCLIRefreshAcceptanceAuthReasonCanClearWithoutDroppingCLIKillClaim(t *testing.T) {
	w, f, _, target := acceptanceRefreshRig(t)
	f.identity = identityB
	w.sessionIdentity = func(string, []string) string { return f.identity }
	w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshPending}
	w.state.Pending["codex"] = []string{"source"}
	f.killNoExit = true
	w.exitWait = -time.Second
	if retry := w.recycle("codex", *f.sessions["source"]); !retry || !w.state.Killed["source"] {
		t.Fatal("initial coalesced kill did not leave an owed claim")
	}
	f.identity = identityA
	w.workPending("codex", identityA, true)
	if !w.state.Killed["source"] {
		t.Fatal("clearing the auth reason dropped the active CLI kill claim")
	}
	f.sessions["source"].Status.Process = status.ProcessExited
	f.killNoExit = false
	w.tickCLIRefresh(true)
	if len(f.launched) != 1 {
		t.Fatalf("late exit was not recovered after auth reason cleared: launches=%d", len(f.launched))
	}
}

func TestCLIRefreshAcceptanceRejectsMismatchedReplacementStamp(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			w, f, old, target := acceptanceRefreshRig(t)
			f.sessions["source"].Status.Process = status.ProcessExited
			w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshWaiting}
			child := runningCodex("child", identityA, status.TurnIdle, migratedConversationID)
			child.ResumedFrom = "source"
			if !missing {
				child.CLIIdentity = old
			}
			f.add(child)
			for i := 0; i < 3; i++ {
				w.tick()
			}
			if w.state.CLI["source"].State == cliRefreshComplete {
				t.Fatalf("sidefile alone validated replacement with wrong launch stamp: %+v", child.CLIIdentity)
			}
			if len(f.launched) != 0 {
				t.Fatal("mismatched replacement was respawned")
			}
		})
	}
}

func TestCLIRefreshAcceptanceAuthAndVersionShareReplacement(t *testing.T) {
	w, f, _, _ := acceptanceRefreshRig(t)
	w.tick()
	w.tick()
	f.identity = identityB
	for i := 0; i < 4; i++ {
		w.tick()
	}
	if len(f.killed) != 1 || len(f.launched) != 1 {
		t.Fatalf("coincident auth/version replacements kills=%v launches=%d", f.killed, len(f.launched))
	}
	if len(f.deleted) != 0 {
		t.Fatalf("auth path deleted CLI refresh recovery source: %v", f.deleted)
	}
	if w.state.CLI["source"].State != cliRefreshComplete {
		t.Fatalf("coalesced refresh not complete: %+v", w.state.CLI["source"])
	}
}

func TestCLIRefreshAcceptanceDraftDefersOnlyCLIRefresh(t *testing.T) {
	w, f, _, _ := acceptanceRefreshRig(t)
	draft := true
	w.cliUnsafe = func(string) bool { return draft }
	// Account-recovery semantics from latest main intentionally permit reloading
	// unsent input; CLI upgrades must continue preserving it.
	if w.sessionUnsafe("source") {
		t.Fatal("CLI-specific draft predicate leaked into auth-only recovery")
	}
	for i := 0; i < 5; i++ {
		w.tick()
	}
	if len(f.killed) != 0 || len(f.launched) != 0 {
		t.Fatal("CLI refresh interrupted a terminal draft")
	}
	if !w.sessionUnsafe("source") {
		t.Fatal("pending CLI refresh did not include terminal draft gate")
	}
	draft = false
	for i := 0; i < 3; i++ {
		w.tick()
	}
	if len(f.killed) != 1 || len(f.launched) != 1 {
		t.Fatalf("cleared draft did not permit one replacement: kills=%v launches=%d", f.killed, len(f.launched))
	}
}

func TestCLIRefreshAcceptanceArchivedSourceCancelsOwedReplacement(t *testing.T) {
	for _, atFence := range []bool{false, true} {
		t.Run(fmt.Sprintf("archive_at_fence=%v", atFence), func(t *testing.T) {
			w, f, _, target := acceptanceRefreshRig(t)
			f.sessions["source"].Status.Process = status.ProcessExited
			w.state.Killed["source"] = true
			w.state.CLI["source"] = cliRefreshRecord{AgentType: "codex", Target: *target, State: cliRefreshPending}
			if atFence {
				w.withResumeFence = func(_ string, attempt func() bool) (bool, bool) {
					f.sessions["source"].RosterHidden = true
					return true, attempt()
				}
			} else {
				f.sessions["source"].RosterHidden = true
			}
			for i := 0; i < 3; i++ {
				w.tick()
			}
			if len(f.launched) != 0 {
				t.Fatal("archived source resurrected by CLI refresh")
			}
			if w.state.Killed["source"] {
				t.Fatal("archived source retained live kill obligation")
			}
		})
	}
}

func TestCLIRefreshAcceptanceRequiresCodexTransportReadiness(t *testing.T) {
	w, f, _, _ := acceptanceRefreshRig(t)
	ready := false
	w.ready = func(persist.Meta) bool { return ready }
	for i := 0; i < 5; i++ {
		w.tick()
	}
	if len(f.launched) != 1 {
		t.Fatalf("expected one replacement, got %d", len(f.launched))
	}
	if rec := w.state.CLI["source"]; rec.State == cliRefreshComplete || rec.State == cliRefreshBlocked {
		t.Fatalf("matching observation bypassed transport wait: %+v", rec)
	}
	ready = true
	w.tick()
	if rec := w.state.CLI["source"]; rec.State != cliRefreshComplete {
		t.Fatalf("ready replacement did not complete: %+v", rec)
	}
	if len(f.launched) != 1 {
		t.Fatal("transport wait spawned another replacement")
	}
}
