package shim

// FAILING-FIRST (TDD RED, GG-5) for the R7 containment RACE the audit committee's round-2
// codex finding 4 names: a Kill/Delete that lands during BACKEND STARTUP is LOST.
//
// The shim's signal plane starts BEFORE either contained process group exists (shim.go:
// startPlanes runs before startBackend; the agent spawns only after the go-ahead), and
// killGroups deliberately never signals a zero pgid -- kill(-0, sig) would signal the shim's
// OWN group. Correct on its own, but nothing REMEMBERED the request: a termination observed
// while both pgids were zero signalled nothing, was never replayed, and the escalation
// worker's grace KILL (production grace: 2s) fired into the same zero pgids long before a
// startup stage (up to 20s each: ReadyTimeout, GoAheadTimeout) produced one. Net effect:
// the backend and then the agent SPAWN AFTER their termination was requested and survive
// it indefinitely, on a session the daemon believes it killed.
//
// The rule these tests freeze: a termination request observed before a group exists is
// REMEMBERED (strongest signal wins, so a group born after the escalation worker already
// fired gets the escalated KILL, not the stale TERM) and REPLAYED the moment that group is
// recorded. A killed shim must never leave a newly-born process group running.

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// r7r3Group spawns a process group for the replay-unit test and returns its leader pid plus
// a channel closed once the leader is REAPED (kill(pid, 0) on a zombie still succeeds, so
// only the Wait proves death). The script runs under /bin/sh -c in its own group and MUST
// write a byte to stdout once its setup (e.g. a trap) is in place; r7r3Group blocks on that
// byte, so a signal disposition the script arms is provably armed before this returns -- a
// replayed TERM racing an unarmed `trap '' TERM` would kill the leader and vacuously pass
// the escalation fence.
func r7r3Group(t *testing.T, script string) (int, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn group: %v", err)
	}
	pid := cmd.Process.Pid
	ready := make([]byte, 1)
	if _, err := out.Read(ready); err != nil {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		t.Fatalf("the group leader never reported ready: %v", err)
	}
	reaped := make(chan struct{})
	go func() { _ = cmd.Wait(); close(reaped) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-reaped
	})
	return pid, reaped
}

// r7r3AwaitReaped asserts the group leader dies within the bound.
func r7r3AwaitReaped(t *testing.T, reaped <-chan struct{}, within time.Duration, why string) {
	t.Helper()
	select {
	case <-reaped:
	case <-time.After(within):
		t.Error(why)
	}
}

// TestR7R3StartupKill_ATerminationObservedBeforeAGroupExistsIsReplayed is the unit fence on
// the remember-and-replay rule, on BOTH recorded groups.
//
// Part 1: a KILL observed while no group exists must reap a backend group recorded later.
// Part 2: a TERM observed while no group exists, whose grace window fully elapses (the
// escalation worker fires its KILL into zero pgids) before the AGENT group is recorded,
// must replay as the escalated KILL -- the leader deliberately ignores TERM, so a mutant
// that replays the original TERM fails this part.
//
// MUTATION: revert killGroups' remembering, or either setter's replay. This test fails.
func TestR7R3StartupKill_ATerminationObservedBeforeAGroupExistsIsReplayed(t *testing.T) {
	// Part 1 -- kill first, backend group second.
	s1 := &server{graceTimeout: time.Second, escStop: make(chan struct{}), escDone: make(chan struct{})}
	s1.onSignal(shimwire.SigKill) // both pgids are zero: nothing is signalled -- yet
	pid1, reaped1 := r7r3Group(t, "echo r; exec sleep 300")
	s1.setBackendPgid(pid1)
	r7r3AwaitReaped(t, reaped1, 5*time.Second,
		"the backend group was created AFTER the kill was observed and survived it: the kill "+
			"was silently lost in the startup window (killGroups skips zero pgids and nothing "+
			"replayed it), which is exactly how a killed session births a live app-server")

	// Part 2 -- TERM first, grace elapses into empty groups, agent group third.
	s2 := &server{graceTimeout: 50 * time.Millisecond, escStop: make(chan struct{}), escDone: make(chan struct{})}
	s2.onSignal(shimwire.SigTerm) // arms the escalation worker; both pgids still zero
	select {
	case <-s2.escDone: // the worker fired its grace KILL into the empty groups
	case <-time.After(5 * time.Second):
		t.Fatal("the escalation worker never fired; the fixture cannot reach the stale-TERM window")
	}
	pid2, reaped2 := r7r3Group(t, `trap '' TERM; echo r; while :; do sleep 1; done`)
	s2.setAgentPgid(pid2)
	r7r3AwaitReaped(t, reaped2, 5*time.Second,
		"the agent group recorded AFTER the whole TERM->grace->KILL escalation ran into empty "+
			"pgids survived: the replay must carry the ESCALATED KILL (strongest signal wins), "+
			"because the one-shot escalation worker is already spent and will never fire again")
	s2.finishEscalation() // worker already joined; makes the final KILL explicit before cleanup
}

// TestR7R3StartupKill_AKillDuringBackendStartupLeavesNoSurvivors drives the race through
// shim.Run itself, on the exact interleaving the finding names: the daemon's kill verb is
// observed BEFORE the backend spawns (the test seam delivers it synchronously through
// onSignal, precisely what serveConn's TypeSignal dispatch does), then the backend AND the
// agent are both born after it.
//
// The agent is modeIdle -- it never exits on its own -- so a lost kill is a session that
// runs forever: Run parks in waitAgentOrBackend and finalization's own group KILL (which
// reaps everything once Run returns) is never reached. Run returning within the bound is
// therefore itself the discriminator, not a formality.
func TestR7R3StartupKill_AKillDuringBackendStartupLeavesNoSurvivors(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendBind, nil)
	cfg.Env = helperEnv(modeIdle) // an agent that NEVER exits on its own
	cfg.Backend.GoAheadTimeout = 300 * time.Millisecond
	cfg.GraceTimeout = time.Second

	killed := make(chan struct{})
	testHookBeforeBackendSpawn = func(s *server) {
		s.onSignal(shimwire.SigKill) // the kill verb, while BOTH pgids are zero
		close(killed)
	}
	t.Cleanup(func() { testHookBeforeBackendSpawn = nil })

	done := runShimAsync(cfg)
	select {
	case <-killed:
	case <-time.After(10 * time.Second):
		t.Fatal("the pre-spawn seam never ran; this is not a backend session")
	}

	// The backend is spawned AFTER the kill was observed; its identity is still recorded.
	info := r7WaitBackendInfo(t, cfg.SessionDir, 10*time.Second)
	t.Cleanup(func() { _ = syscall.Kill(-info.PGID, syscall.SIGKILL) })

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		// Reap the leak so the failed test leaves nothing behind: killing the backend
		// group fires the backend-died edge, which contains the idle agent and lets Run
		// return before the test exits.
		_ = syscall.Kill(-info.PGID, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
		t.Fatal("shim.Run is still running 15s after its session was KILLED: the kill arrived " +
			"during backend startup, hit two zero pgids, and was never replayed -- the backend " +
			"and the idle agent both spawned AFTER termination was requested and survived it")
	}
	// Run returned, so the agent was reaped (cmd.Wait) and the backend joined. The leader
	// must be dead, not merely signalled.
	if r7Alive(info.PID) {
		_ = syscall.Kill(-info.PGID, syscall.SIGKILL)
		t.Errorf("the backend (pid %d) outlived shim.Run on a session killed before it spawned", info.PID)
	}
}
