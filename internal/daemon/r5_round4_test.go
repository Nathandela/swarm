package daemon

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-4 review fix-pack (bead
// agents-tracker-hggx.6), daemon half. Three findings, all proven by probe in review:
//
//   BLOCKER 1  A remote launch composes with ClientEnv nil (correct: no phone-supplied
//              env, ADR-007 D8) and the daemon then handed the agent
//              persist.FilterEnv(nil) -- an EMPTY environment. D8's other half ("env
//              comes from daemon policy") did not exist, so no real provider could
//              ever run: no PATH, no HOME. The daemon-policy env IS the daemon's own
//              process environment through the SAME allowlist (ADR-006's billing
//              inheritance: the daemon process is the user's machine environment).
//
//   MAJOR 2    The concurrent double-driver's LOSER was handed the WINNER's phase-1
//              reservation (Running, ShimPID==0, no process) as an AUTHORITATIVE
//              idempotent success. Round 3 applied the phantom rule to the status
//              READ but left it off the primary launch reply. If the winner then rolls
//              back or fails waitShimServing, that "success" named a session that never
//              existed. resolveReplay must answer the loser UNDECIDABLY.
//
//   LOW 4      A COMPLETED operation whose session later went LOST still re-drove
//              (spawning a second agent under the same signed operation id) and still
//              read back outcome_unknown. Round 3's terminal-record rule covered only
//              the session-row-MISSING branch. The rule is applied consistently here:
//              a terminal record is proof the launch applied, whatever became of the
//              session afterwards -- LOST is not evidence the launch did not happen,
//              it is evidence the machine lost track of the PROCESS.
//
// This file must fail (undefined: ErrLaunchOutcomeUnknown; behavioral on the rest)
// until the round-4 GREEN slice lands.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// TestR5Round4_NilClientEnvGetsTheDaemonPolicyEnvironment (BLOCKER 1): the launch a
// signed session_launch composes carries NO client env by design. The agent must still
// receive a WORKING environment, and the only honest source for it is the daemon's own
// process environment through the existing allowlist -- never the phone.
func TestR5Round4_NilClientEnvGetsTheDaemonPolicyEnvironment(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := announceSpec(t, pidFile)
	spec.ClientEnv = nil // exactly what internal/protocol/remote_launch.go composes
	spec.OperationID = "devA:01JR4ENV"

	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch with nil ClientEnv: %v", err)
	}
	t.Cleanup(func() { killTree(readPIDFile(t, pidFile)); killTree(m.ShimPID) })

	want := "PATH=" + os.Getenv("PATH")
	if !envHas(m.Env, want) {
		t.Errorf("launch env = %v, want it to carry the daemon's own %q: with ClientEnv nil the "+
			"agent got persist.FilterEnv(nil) -- an EMPTY environment, so no bare adapter argv0 "+
			"could ever resolve and no real provider could run (ADR-007 D8: env comes from daemon "+
			"policy)", m.Env, want)
	}
	if home := os.Getenv("HOME"); home != "" && !envHas(m.Env, "HOME="+home) {
		t.Errorf("launch env = %v, want it to carry the daemon's HOME=%q: an agent CLI with no "+
			"HOME cannot find its own credentials or config", m.Env, home)
	}
}

// TestR5Round4_ClientSuppliedEnvIsStillTheOneUsed (BLOCKER 1, the other direction):
// the daemon-policy fill is a FALLBACK for the no-env launch, not a replacement. A
// local launch that forwarded its shell's env keeps exactly that env (allowlisted), so
// the fix cannot silently re-point a local session at the daemon's environment.
func TestR5Round4_ClientSuppliedEnvIsStillTheOneUsed(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := announceSpec(t, pidFile)
	spec.ClientEnv = []string{"PATH=" + os.Getenv("PATH"), "LANG=en_US.UTF-8", "SECRET_TOKEN=nope"}

	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { killTree(readPIDFile(t, pidFile)); killTree(m.ShimPID) })

	if !envHas(m.Env, "LANG=en_US.UTF-8") {
		t.Errorf("launch env = %v, want the CLIENT's own LANG: a client that supplied an env "+
			"must keep it", m.Env)
	}
	for _, kv := range m.Env {
		if strings.HasPrefix(kv, "SECRET_TOKEN=") {
			t.Errorf("launch env leaked %q; the allowlist (S-2) still governs both sources", kv)
		}
	}
}

// TestR5Round4_ConcurrentLoserIsAnsweredUndecidablyNotPhantomApplied (MAJOR 2): the
// reviewer's own probe shape. Crash at phaseReserved leaves the winner's phase-1
// reservation in place (Running, ShimPID==0, record 'prepared'); resolveReplay for the
// SAME operation id -- what a redelivered session_launch racing the in-flight original
// executes -- returned redrive=false, err=nil and the reservation's meta, which the
// wire replies as OpSessionLaunch and the phone renders APPLIED ("the session was
// created on the machine"). It must answer undecidably instead.
func TestR5Round4_ConcurrentLoserIsAnsweredUndecidablyNotPhantomApplied(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const op = "devA:01JR4LOSER"
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := launchOpSpec(t, pidFile, op)

	var reservedID string
	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseReserved {
			reservedID = m.ID
			return errInjectedCrash
		}
		return nil
	}
	if _, err := d.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want the injected crash at phaseReserved", err)
	}
	if reservedID == "" {
		t.Fatal("probe never observed phaseReserved")
	}

	redrive, _, _, cached, err := d.resolveReplay(op, "01JR4FRESHID")
	if err == nil {
		t.Fatalf("resolveReplay for the in-flight reservation = (redrive %v, cached %q shim %d, "+
			"err nil): the loser receives the WINNER's phase-1 meta as an authoritative idempotent "+
			"success, and the winner's launch can still roll back -- an APPLIED reply naming a "+
			"session that never existed", redrive, cached.ID, cached.ShimPID)
	}
	if !errors.Is(err, ErrLaunchOutcomeUnknown) {
		t.Errorf("resolveReplay error = %v, want errors.Is(_, ErrLaunchOutcomeUnknown): a stable "+
			"sentinel the wire can map to the outcome_unknown delivery state, not an incidental "+
			"message the phone renders as a flat refusal", err)
	}
	if redrive {
		t.Error("resolveReplay chose to RE-DRIVE past an in-flight reservation; that is the second " +
			"live process 'at most one process per operation' forbids")
	}

	// And the same undecidable answer through the real entry point: a replayed Launch
	// must neither spawn nor claim success.
	replayPID := filepath.Join(t.TempDir(), "replay.pid")
	m2, rerr := d.Launch(launchOpSpec(t, replayPID, op))
	if rerr == nil {
		killTree(m2.ShimPID)
		t.Fatalf("replayed Launch during the in-flight original returned session %q (shim %d) "+
			"with no error", m2.ID, m2.ShimPID)
	}
	if !errors.Is(rerr, ErrLaunchOutcomeUnknown) {
		t.Errorf("replayed Launch error = %v, want ErrLaunchOutcomeUnknown", rerr)
	}
	if _, err := os.Stat(replayPID); err == nil {
		t.Error("the undecidable replay still spawned an agent")
	}
}

// TestR5Round4_CompletedRecordWhoseSessionWentLostIsAppliedAndRefusesTheRedrive
// (LOW 4): after a launch that definitively APPLIED (terminal idempotency record), a
// session that later goes LOST must not reopen the second-process window, and must not
// read back outcome_unknown. Round 3 applied that rule to the row-MISSING branch only;
// LOST is the same fact with a corpse row beside it.
func TestR5Round4_CompletedRecordWhoseSessionWentLostIsAppliedAndRefusesTheRedrive(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const op = "devA:01JR4LOST"
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	m, err := d.Launch(launchOpSpec(t, pidFile, op))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	agentPID := readPIDFile(t, pidFile)
	t.Cleanup(func() { killTree(agentPID); killTree(m.ShimPID) })

	// Force the LOST shape the reviewer's probe forced (reconcile's answer for a
	// session whose shim it could not re-identify after a restart).
	d.mu.Lock()
	s, ok := d.sessions[m.ID]
	if ok {
		s.meta.Status.Process = status.ProcessLost
	}
	d.mu.Unlock()
	if !ok {
		t.Fatalf("session %s vanished from the registry", m.ID)
	}

	out, known := d.OperationOutcome(op)
	if !known {
		t.Fatalf("OperationOutcome(%s) unknown after a COMPLETED launch", op)
	}
	if out.State != OpStateApplied || out.SessionID != m.ID {
		t.Errorf("OperationOutcome = %+v, want applied naming %s: the terminal record is PROOF the "+
			"launch happened; the session going LOST is a later fact about the PROCESS, not evidence "+
			"the launch did not apply (and resolveReplay refuses the re-drive for this same shape, "+
			"so status and replay must not disagree)", out, m.ID)
	}

	replayPID := filepath.Join(t.TempDir(), "replay.pid")
	m2, rerr := d.Launch(launchOpSpec(t, replayPID, op))
	if rerr == nil {
		killTree(m2.ShimPID)
		t.Fatalf("replay of the COMPLETED operation spawned a NEW session %q (shim %d) because its "+
			"session had gone LOST; the terminal record must refuse the re-drive on this branch too",
			m2.ID, m2.ShimPID)
	}
	if !errors.Is(rerr, ErrLaunchOpConsumed) {
		t.Errorf("replay refusal = %v, want errors.Is(_, ErrLaunchOpConsumed)", rerr)
	}
	if _, err := os.Stat(replayPID); err == nil {
		t.Error("the refused replay still spawned an agent")
	}
}

// envHas reports whether env carries the exact KEY=VALUE entry.
func envHas(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}
