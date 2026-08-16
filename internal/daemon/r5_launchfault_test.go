package daemon

// FAILING-FIRST (TDD RED, GG-5) for Wave R5 deliverables 2+3's daemon half (bead
// agents-tracker-hggx.6): OPERATION-STATUS RECONCILIATION over the EXISTING two-phase
// launch reservation, under fault injection around reserve/spawn. The playbook's bar
// (exit evidence, :783-785): "fault injection around reservation/spawn produces at most
// one process with an authoritative or outcome_unknown result" -- and deliverable 2:
// "a launch that dies mid-flight resolves authoritative or outcome_unknown, never
// silent".
//
// The contract these tests freeze:
//
//   - type OpOutcome { State string; SessionID string } with the stable states
//     OpStateApplied = "applied" and OpStateOutcomeUnknown = "outcome_unknown"
//     (ADR-017 T9's delivery vocabulary, machine side; ADR-007 D6's two-phase record
//     is the source of truth).
//   - (*Daemon).OperationOutcome(operationID) (OpOutcome, bool): the READ-ONLY
//     reconciliation surface the operation_status op serves. ok=false for an id this
//     daemon has no record of (never an invented outcome); a completed/live launch is
//     authoritative (applied + session id); a launch whose crash left the record
//     undecidable is outcome_unknown -- never an error, never silence, and NEVER a
//     side effect (operation_status must not authorize a retry, playbook:449).
//
// The crashes themselves ride the EXISTING launchProbe seam (launch.go, E5.4/S11) and
// the EXISTING idempotent reservation (spec.OperationID -> idem.Prepare inside launch)
// -- deliberately: R5's rule is that the remote path reuses this exact machinery, so
// these tests drive Launch with an OperationID exactly as the session_launch handler
// will, and add ONLY the outcome-reading surface.
//
// This file must fail to compile (undefined: OpOutcome / OperationOutcome /
// OpStateApplied / OpStateOutcomeUnknown) until the GREEN slice adds the surface.

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
)

// launchOpSpec is announceSpec plus the signed remote operation id, i.e. the spec shape
// the session_launch handler hands to the daemon.
func launchOpSpec(t *testing.T, pidFile, opID string) LaunchSpec {
	t.Helper()
	spec := announceSpec(t, pidFile)
	spec.OperationID = opID
	return spec
}

// TestR5Fault_HappyLaunchIsAuthoritativeApplied: after an ordinary completed remote
// launch, OperationOutcome answers applied with the launched session id -- the
// authoritative result a phone's reconciliation converges on.
func TestR5Fault_HappyLaunchIsAuthoritativeApplied(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const op = "devA:01JHAPPY"
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	m, err := d.Launch(launchOpSpec(t, pidFile, op))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { killTree(m.ShimPID) })

	out, ok := d.OperationOutcome(op)
	if !ok {
		t.Fatalf("OperationOutcome(%s) unknown after a completed launch; the record was fsync'd "+
			"as part of the reservation and must be readable", op)
	}
	if out.State != OpStateApplied {
		t.Errorf("OperationOutcome state = %q, want %q (authoritative)", out.State, OpStateApplied)
	}
	if out.SessionID != m.ID {
		t.Errorf("OperationOutcome session = %q, want the launched session %q", out.SessionID, m.ID)
	}
}

// TestR5Fault_UnknownOperationIsNotInvented: an operation id this daemon never saw
// answers ok=false. The protocol layer turns that into a stable refusal; what the
// daemon must never do is fabricate an outcome for it.
func TestR5Fault_UnknownOperationIsNotInvented(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	if out, ok := d.OperationOutcome("devA:01JNEVERSEEN"); ok {
		t.Fatalf("OperationOutcome invented %+v for an id this daemon has no record of", out)
	}
}

// TestR5Fault_CrashBetweenReserveAndSpawn_OutcomeUnknownNeverSilent: the daemon dies
// after the reservation (meta + idempotency record persisted) and before any shim
// exists. On the next Open the restarted daemon cannot prove what happened to the
// side effect from the record alone, so OperationOutcome must answer outcome_unknown
// -- present (never silent/unknown-id), and never a claimed "applied" pointing at a
// session that is not running. Zero processes exist (the "at most one" bound holds
// with room to spare), and reading the outcome twice is side-effect-free: a
// subsequent replay of the same operation id still re-drives to exactly ONE process.
func TestR5Fault_CrashBetweenReserveAndSpawn_OutcomeUnknownNeverSilent(t *testing.T) {
	cfg := daemonConfig(t)
	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const op = "devA:01JRESERVECRASH"
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := launchOpSpec(t, pidFile, op)

	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseReserved {
			return errInjectedCrash
		}
		return nil
	}
	if _, err := d1.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want injected crash", err)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatal("agent was spawned despite the crash before spawn")
	}

	d1.abandon()
	d2 := openDaemon(t, cfg)

	out, ok := d2.OperationOutcome(op)
	if !ok {
		t.Fatalf("OperationOutcome(%s) after crash+restart = silent (no record); the playbook's "+
			"bar is authoritative or outcome_unknown, NEVER silent", op)
	}
	if out.State != OpStateOutcomeUnknown {
		t.Fatalf("OperationOutcome state after undecidable crash = %q, want %q -- claiming %q "+
			"for a session that never ran would be a false authoritative answer", out.State, OpStateOutcomeUnknown, out.State)
	}

	// Read-only: asking twice changes nothing (operation_status never authorizes or
	// performs a retry).
	if again, ok2 := d2.OperationOutcome(op); !ok2 || again.State != out.State {
		t.Fatalf("second OperationOutcome read = (%+v, %v), want the identical answer: the read "+
			"must have no side effect on the record", again, ok2)
	}

	// The replay path (the phone re-sending the SAME operation id) is still the one
	// re-driver, and it produces exactly one process.
	m, err := d2.Launch(spec)
	if err != nil {
		t.Fatalf("replay Launch after crash: %v", err)
	}
	t.Cleanup(func() { killTree(m.ShimPID) })
	if n := len(d2.List()); n != 1 {
		t.Errorf("after crash + replay, %d sessions exist; want exactly 1 (at most one process)", n)
	}
	if out, ok := d2.OperationOutcome(op); !ok || out.State != OpStateApplied || out.SessionID != m.ID {
		t.Errorf("after the replay resolved it, OperationOutcome = (%+v, %v); want applied/%s", out, ok, m.ID)
	}
}

// TestR5Fault_CrashAfterSpawnBeforeConfirm_ReplayAdoptsTheOneLiveProcess: the daemon
// dies after the shim spawned and its identity was persisted, before the launch
// confirmed. The restarted daemon reconciles the live shim back, and the phone's
// replay of the same operation id must ADOPT that session -- same session id, same
// shim PID, exactly one session -- never spawn a second process. OperationOutcome is
// then authoritative for it.
func TestR5Fault_CrashAfterSpawnBeforeConfirm_ReplayAdoptsTheOneLiveProcess(t *testing.T) {
	cfg := daemonConfig(t)
	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const op = "devA:01JSPAWNCRASH"
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := launchOpSpec(t, pidFile, op)

	var spawnedID string
	var shimPID int
	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseSpawned {
			spawnedID = m.ID
			shimPID = m.ShimPID
			return errInjectedCrash
		}
		return nil
	}
	if _, err := d1.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want injected crash", err)
	}
	if shimPID <= 0 || !processAlive(shimPID) {
		t.Fatalf("shim %d not alive after the modelled crash; the crash must leave it running", shimPID)
	}
	agentPID := readPIDFile(t, pidFile)
	t.Cleanup(func() { killTree(agentPID); killTree(shimPID) })

	d1.abandon()
	d2 := openDaemon(t, cfg)

	m, err := d2.Launch(spec) // the phone's replay, same operation id
	if err != nil {
		t.Fatalf("replay Launch: %v", err)
	}
	if m.ID != spawnedID {
		t.Errorf("replay returned session %q, want the reconciled original %q: a fresh session "+
			"here is a SECOND live process racing the first on one cwd", m.ID, spawnedID)
	}
	if n := len(d2.List()); n != 1 {
		t.Errorf("after crash + replay, %d sessions exist; want exactly 1", n)
	}
	if m.ShimPID != shimPID {
		t.Errorf("replay meta shim PID = %d, want the original live shim %d (adopted, not respawned)", m.ShimPID, shimPID)
	}

	out, ok := d2.OperationOutcome(op)
	if !ok || out.State != OpStateApplied || out.SessionID != spawnedID {
		t.Errorf("OperationOutcome after adoption = (%+v, %v), want applied/%s: the record is "+
			"decidable (the session lives), so the answer must be authoritative", out, ok, spawnedID)
	}
}

// TestR5Fault_DoubleDriver_ConcurrentSameOperationID_ExactlyOneProcess: two drivers
// race the SAME signed operation id into Launch concurrently (a network retry
// overtaking its original, or two gateway deliveries of one command). Exactly one
// session/process may result. This is the "double-driver" injection of the R5 scope,
// riding the mutex-guarded Prepare inside the existing launch() -- not a parallel
// remote-only gate.
//
// AMENDED, round-4 review MAJOR 2. This test previously required BOTH drivers to
// return a session ("converge on the single winner, not error out"), which -- proven by
// the reviewer's probe -- is satisfiable in a way that is a LIE: when the loser arrives
// inside the winner's phase-1 window, the only thing there to converge on is a
// reservation with NO PROCESS, whose launch can still roll back (newHookToken,
// spawnShim, procStartTimeFn, the second saveMeta) or fail at waitShimServing. Handing
// that back as a success replies OpSessionLaunch on the wire, which the phone renders as
// "the session was created on the machine and is in your session list" -- an
// authoritative claim about a session that may never exist. Round 3 had already ruled
// this shape a PHANTOM for the status read; MAJOR 2 is that the primary launch reply
// kept it.
//
// The contract asserted below is therefore STRENGTHENED, not relaxed:
//
//   - the at-most-one-process bar is unchanged and still asserted exactly as before
//     (one session row, one agent process, applied status);
//   - a driver that returns a session must return the SAME session as the other, AND
//     that session must have a RECORDED PROCESS (ShimPID != 0) -- the assertion the
//     defect could previously slip past;
//   - a driver that does not return a session may fail ONLY with the undecidable
//     sentinel ErrLaunchOutcomeUnknown, never any other error and never silently.
//
// Which of the two the loser gets is genuinely timing-dependent (it depends on whether
// it reached resolveReplay before or after the winner recorded its shim), so both are
// accepted -- but only these two, and neither may be a phantom.
func TestR5Fault_DoubleDriver_ConcurrentSameOperationID_ExactlyOneProcess(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const op = "devA:01JDOUBLE"
	pidA := filepath.Join(t.TempDir(), "a.pid")
	pidB := filepath.Join(t.TempDir(), "b.pid")

	var wg sync.WaitGroup
	results := make([]persist.Meta, 2)
	errs := make([]error, 2)
	for i, pf := range []string{pidA, pidB} {
		wg.Add(1)
		go func(i int, pf string) {
			defer wg.Done()
			results[i], errs[i] = d.Launch(launchOpSpec(t, pf, op))
		}(i, pf)
	}
	wg.Wait()

	t.Cleanup(func() { killTree(results[0].ShimPID); killTree(results[1].ShimPID) })

	var launched []persist.Meta
	for i, err := range errs {
		switch {
		case err == nil:
			if results[i].ShimPID == 0 {
				t.Fatalf("driver %d was handed session %q with NO recorded process as an "+
					"authoritative success; that is the winner's phase-1 reservation, which can "+
					"still roll back -- the wire replies it as applied and the phone tells the "+
					"user the session exists (round-4 MAJOR 2)", i, results[i].ID)
			}
			launched = append(launched, results[i])
		case errors.Is(err, ErrLaunchOutcomeUnknown):
			// The honest answer while the winner is still in flight.
		default:
			t.Fatalf("driver %d failed with %v; the only permitted non-success answer to a "+
				"concurrent double-driver is the undecidable sentinel", i, err)
		}
	}
	if len(launched) == 0 {
		t.Fatal("neither driver returned a session; one of the two must be the winner")
	}
	if len(launched) == 2 && launched[0].ID != launched[1].ID {
		t.Fatalf("double-driver produced TWO sessions %q and %q for one operation id; the bar "+
			"is at most one process", launched[0].ID, launched[1].ID)
	}
	if n := len(d.List()); n != 1 {
		t.Errorf("registry holds %d sessions after the double-driver, want exactly 1", n)
	}
	// At most one agent process: exactly one of the two pid files may exist. The agent
	// writes its pid file ASYNCHRONOUSLY after its shim confirms serving, so the count
	// polls until the winner's file lands (readPIDFile's own convention; an un-polled
	// stat here was a timing bug this test's -race run exposed -- the race-built agent
	// binary starts slowly enough that Launch returns before the write). The asserted
	// bound is unchanged: exactly one, never two.
	spawned := 0
	deadline := time.Now().Add(pollTimeout)
	for {
		spawned = 0
		for _, pf := range []string{pidA, pidB} {
			if _, err := os.Stat(pf); err == nil {
				spawned++
			}
		}
		if spawned >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(pollStep)
	}
	if spawned != 1 {
		t.Errorf("%d agent processes were spawned for one operation id, want exactly 1", spawned)
	}

	out, ok := d.OperationOutcome(op)
	if !ok || out.State != OpStateApplied || out.SessionID != launched[0].ID {
		t.Errorf("OperationOutcome after the race = (%+v, %v), want applied/%s: once the winner "+
			"has settled, the reconciliation read is authoritative for BOTH drivers -- including "+
			"the one that was answered undecidably at the time", out, ok, launched[0].ID)
	}
}
