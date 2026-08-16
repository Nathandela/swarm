package daemon

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-3 review fix-pack (bead
// agents-tracker-hggx.6), daemon half. Two MAJOR findings, both proven by probe in
// review:
//
//  1. FALSE AUTHORITATIVE MID-FLIGHT: OperationOutcome tested only `present &&
//     Process != Lost`, but the phase-1 reservation inserts a Running meta with
//     ShimPID==0 BEFORE any shim exists -- so a status read fired between reserve
//     and spawn answered "applied" naming a session whose process does not exist
//     and whose launch can still fail and roll back. resolveReplay already calls
//     that exact shape a PHANTOM; status must apply the same rule (the file's own
//     claim: "status and replay can never disagree").
//
//  2. REPLAY AFTER DELETE SPAWNS A SECOND PROCESS: launch never recorded a
//     terminal phase, so once Delete removed the session row, resolveReplay could
//     not distinguish "crashed mid-launch, no usable session" (re-drive is right)
//     from "the launch definitively applied and the user deleted it" (re-driving
//     silently spawns a SECOND agent under a signature that stays valid for the
//     whole maxCommandValidity hour, and gateway crash-shaped redelivery of
//     session_launch is a PINNED behavior of this wave). The fix: a launch that
//     returns success completes its idempotency record (PhaseCompleted), replay of
//     a completed operation whose session is GONE refuses with the stable
//     ErrLaunchOpConsumed sentinel instead of spawning, and OperationOutcome
//     answers the completed record authoritatively (applied) rather than the
//     round-1 outcome_unknown -- closing the review's LOW finding in the same
//     stroke (a deliberately deleted launch no longer reads "may create a second
//     session" about an operation the machine completed).
//
// This file must fail (undefined: ErrLaunchOpConsumed; behavioral on the rest)
// until the round-3 GREEN slice lands.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

// TestR5Round3_MidFlightReservationIsNeverAuthoritativeApplied: an operation_status
// read that lands between reserve and spawn (phase-1 meta persisted, Running,
// ShimPID==0 -- no process exists) must answer outcome_unknown, never an
// authoritative "applied" naming a session that may yet roll back. The probe fires
// the read at the exact boundary the review's probe did.
func TestR5Round3_MidFlightReservationIsNeverAuthoritativeApplied(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const op = "devA:01JMIDFLIGHT"
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := launchOpSpec(t, pidFile, op)

	var during OpOutcome
	var duringOK bool
	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseReserved {
			during, duringOK = d.OperationOutcome(op)
			return errInjectedCrash
		}
		return nil
	}
	if _, err := d.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want injected crash", err)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatal("agent was spawned despite the crash before spawn")
	}

	if !duringOK {
		t.Fatal("mid-flight OperationOutcome was silent (no record); the reservation is fsync'd first")
	}
	if during.State != OpStateOutcomeUnknown {
		t.Fatalf("mid-flight OperationOutcome state = %q (session %q), want %q: the phase-1 "+
			"reservation has ShimPID==0 -- the exact shape resolveReplay calls a PHANTOM -- and an "+
			"applied answer here names a session whose process does not exist and whose launch can "+
			"still fail and roll back", during.State, during.SessionID, OpStateOutcomeUnknown)
	}

	// The phantom left behind by the modelled crash reads the same way afterwards.
	after, ok := d.OperationOutcome(op)
	if !ok || after.State != OpStateOutcomeUnknown {
		t.Fatalf("post-crash OperationOutcome = (%+v, %v), want outcome_unknown for the phantom "+
			"reservation (Running, ShimPID==0)", after, ok)
	}
}

// TestR5Round3_ReplayAfterDeleteRefusesInsteadOfSpawningASecondProcess: the review's
// probe -- Launch(op) -> Delete(session) -> Launch(same signed op) -- produced a NEW
// session with a new shim PID. The replay of a COMPLETED operation whose session is
// gone must refuse with ErrLaunchOpConsumed, spawn nothing, and leave the registry
// empty: "replay re-drives to exactly one process" must count the deliberately
// deleted one.
func TestR5Round3_ReplayAfterDeleteRefusesInsteadOfSpawningASecondProcess(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const op = "devA:01JDELREPLAY"
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	m, err := d.Launch(launchOpSpec(t, pidFile, op))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	agentPID := readPIDFile(t, pidFile)
	t.Cleanup(func() { killTree(agentPID); killTree(m.ShimPID) })

	if err := d.Delete(m.ID); err != nil {
		t.Fatalf("Delete(%s): %v", m.ID, err)
	}

	replayPidFile := filepath.Join(t.TempDir(), "replay.pid")
	m2, rerr := d.Launch(launchOpSpec(t, replayPidFile, op))
	if rerr == nil {
		killTree(m2.ShimPID)
		t.Fatalf("replayed Launch after Delete succeeded with NEW session %q (shim %d); the signed "+
			"operation already applied and its deletion was deliberate -- the replay must refuse, "+
			"not spawn a second agent", m2.ID, m2.ShimPID)
	}
	if !errors.Is(rerr, ErrLaunchOpConsumed) {
		t.Errorf("replay refusal = %v, want errors.Is(_, ErrLaunchOpConsumed): a stable sentinel, "+
			"not an incidental message", rerr)
	}
	if n := len(d.List()); n != 0 {
		t.Errorf("registry holds %d sessions after the refused replay, want 0", n)
	}
	if _, err := os.Stat(replayPidFile); err == nil {
		t.Error("the refused replay still spawned an agent (pid file written)")
	}
}

// TestR5Round3_AppliedThenDeletedReadsBackAuthoritativeApplied: the review's LOW --
// a launch that definitively applied and was then deliberately deleted read back
// outcome_unknown, so the shipped phone copy would warn "confirming again may create
// a second session" about an operation the machine completed. The completed record
// survives Delete; the status read answers it authoritatively.
func TestR5Round3_AppliedThenDeletedReadsBackAuthoritativeApplied(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const op = "devA:01JDELSTATUS"
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	m, err := d.Launch(launchOpSpec(t, pidFile, op))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	agentPID := readPIDFile(t, pidFile)
	t.Cleanup(func() { killTree(agentPID); killTree(m.ShimPID) })

	if err := d.Delete(m.ID); err != nil {
		t.Fatalf("Delete(%s): %v", m.ID, err)
	}

	out, ok := d.OperationOutcome(op)
	if !ok {
		t.Fatalf("OperationOutcome(%s) silent after Delete; the completed record must survive "+
			"the session row", op)
	}
	if out.State != OpStateApplied || out.SessionID != m.ID {
		t.Errorf("OperationOutcome after deliberate Delete = %+v, want applied/%s: the machine "+
			"CAN prove what happened (the launch completed; the deletion was a later, separate "+
			"verb), so outcome_unknown here is a false undecidability", out, m.ID)
	}
}

// TestR5Round3_AdoptedReplayThenDeleteAlsoRefusesRedrive: the crash-after-spawn
// adoption path (pinned in TestR5Fault_CrashAfterSpawnBeforeConfirm) must ALSO leave
// a terminal record: replay adopts the one live shim, the user deletes the session,
// and a second replay of the same operation id refuses instead of re-driving --
// otherwise the delete window reopens for exactly the launches that crashed once.
func TestR5Round3_AdoptedReplayThenDeleteAlsoRefusesRedrive(t *testing.T) {
	cfg := daemonConfig(t)
	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const op = "devA:01JADOPTDEL"
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := launchOpSpec(t, pidFile, op)

	var shimPID int
	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseSpawned {
			shimPID = m.ShimPID
			return errInjectedCrash
		}
		return nil
	}
	if _, err := d1.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want injected crash", err)
	}
	agentPID := readPIDFile(t, pidFile)
	t.Cleanup(func() { killTree(agentPID); killTree(shimPID) })

	d1.abandon()
	d2 := openDaemon(t, cfg)

	m, err := d2.Launch(spec) // the replay that ADOPTS the one live shim
	if err != nil {
		t.Fatalf("replay Launch: %v", err)
	}
	if err := d2.Delete(m.ID); err != nil {
		t.Fatalf("Delete(%s): %v", m.ID, err)
	}

	replayPidFile := filepath.Join(t.TempDir(), "replay.pid")
	m2, rerr := d2.Launch(launchOpSpec(t, replayPidFile, op))
	if rerr == nil {
		killTree(m2.ShimPID)
		t.Fatalf("post-adoption replay after Delete spawned session %q; the adoption resolved the "+
			"operation (a live, spawned shim was returned as its result), so its later deletion "+
			"must refuse the re-drive exactly as a normal launch's does", m2.ID)
	}
	if !errors.Is(rerr, ErrLaunchOpConsumed) {
		t.Errorf("post-adoption replay refusal = %v, want errors.Is(_, ErrLaunchOpConsumed)", rerr)
	}
	if n := len(d2.List()); n != 0 {
		t.Errorf("registry holds %d sessions after the refused replay, want 0", n)
	}
}
