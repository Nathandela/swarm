package skeleton

import (
	"fmt"
	"testing"

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
