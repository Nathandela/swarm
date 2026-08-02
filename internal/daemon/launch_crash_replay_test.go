package daemon

// FAILING-FIRST crash-window tests for two-phase launch idempotency (fix-pack 4a;
// findings DCR-1/DCR-2, docs/verification/remote-phase1-daemon-review.md "A3 launch
// crash table"). The launch path persists the operation_id via idem.Prepare (an
// fsync SEPARATE from and BEFORE the reserve saveMeta) and its replay branch never
// inspects the record Phase, so a daemon crash inside the launch reserve window
// leaves the operation_id in one of two broken states after restart:
//
//   - W1 (crash after Prepare fsync, before the reserve saveMeta): on disk an idem
//     `prepared`/`executing` launch record whose reserved session has NO persisted
//     meta. The current replay does Get(reservedID) -> not found -> a HARD error
//     ("cached session gone"); the operation_id is POISONED FOREVER (the phone can
//     never retry it).
//   - W3 (crash after the reserve saveMeta, before spawn): on disk the same idem
//     record AND a reserved meta {Running, ShimPID=0}. On Open, reconcile marks that
//     meta LOST; the current replay does Get(reservedID) -> the LOST session and
//     returns it as a SUCCESSFUL launch — a SILENT CORPSE.
//
// These tests pin the DESIRED BEHAVIOR (mechanism-agnostic): W1 must re-drive to a
// fresh live Running session (never poison); W3 must never return a LOST/dead
// session as success (re-drive, or a clearly retryable transient error). They MUST
// FAIL against the current code (which poisons W1 and returns the W3 corpse). A
// separate implementer makes them pass — this file is TESTS ONLY.
//
// STATE-PLANTING APPROACH: the pre-crash on-disk state is synthesized directly with
// the REAL idempotency.Store + persist.Store APIs into the daemon's StateDir layout
// (idem under <StateDir>/idempotency, meta under <StateDir>/<id>/meta.json), THEN a
// daemon is opened over that dir (Open == crash-restart) and Launch(spec-with-that-
// OperationID) is replayed. This reaches W1/W3 with no new crash probe. W1 is only
// reachable by planting: the launch two-phase probe seam fires no earlier than
// phaseReserved (after the reserve saveMeta), so no existing probe sits in the
// Prepare->saveMeta gap; both windows are planted here for uniformity.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// plantLaunchIdemRecord writes a durable launch idempotency record for opID at the
// given phase (prepared, or prepared->executing) pointing at sessionID, using the
// REAL store so the daemon's Open replays it exactly as it would a pre-crash record.
func plantLaunchIdemRecord(t *testing.T, cfg Config, opID, sessionID string, phase idempotency.Phase) {
	t.Helper()
	st, err := idempotency.Open(filepath.Join(cfg.StateDir, "idempotency"))
	if err != nil {
		t.Fatalf("plant idem: open store: %v", err)
	}
	if _, _, err := st.Prepare(opID, "launch", sessionID); err != nil {
		t.Fatalf("plant idem: prepare: %v", err)
	}
	if phase == idempotency.PhaseExecuting {
		if err := st.Begin(opID); err != nil {
			t.Fatalf("plant idem: begin: %v", err)
		}
	}
}

// plantReservedMeta writes a reserved session meta {Running, ShimPID=0} — the
// on-disk shape a crash leaves after the reserve saveMeta but before any live shim
// (windows W3/W4). On the next Open, reconcile marks it LOST (ShimPID=0 fails the
// identity check, reconcile.go:58-72).
func plantReservedMeta(t *testing.T, cfg Config, sessionID string) {
	t.Helper()
	store, err := persist.NewStore(cfg.StateDir)
	if err != nil {
		t.Fatalf("plant meta: new store: %v", err)
	}
	now := time.Now()
	m := persist.Meta{
		ID:           sessionID,
		AgentType:    "fake",
		Cwd:          t.TempDir(),
		CreatedAt:    now,
		LastActivity: now,
		Status:       status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
		ShimPID:      0, // a reservation that never got a live shim
	}
	if err := store.Save(m); err != nil {
		t.Fatalf("plant meta: save: %v", err)
	}
}

// killAllSessions registers a cleanup that tears down every session in the registry
// so a real shim spawned by a re-drive never leaks past the test (shims are detached
// and survive the daemon's Close by design).
func killAllSessions(t *testing.T, d *Daemon) {
	t.Helper()
	t.Cleanup(func() {
		for _, m := range d.List() {
			_ = d.Kill(m.ID)
		}
	})
}

// sessionInList reports whether id is present in d.List().
func sessionInList(d *Daemon, id string) bool {
	for _, m := range d.List() {
		if m.ID == id {
			return true
		}
	}
	return false
}

// TestLaunchCrashReplay_W1_PoisonedOperationID_Redrive pins fix-pack 4a item 1
// (DCR-1/DCR-2, window W1). After a restart over a state dir holding a
// `prepared`/`executing` launch idem record whose reserved session has NO persisted
// meta, a Launch replay with that operation_id MUST re-drive to a fresh, live,
// Running session — never the "cached session gone" hard error that permanently
// poisons the key. A second replay must then return the SAME session (the key is a
// normal completed idempotency key, not re-driving forever — item 3, behavioral).
//
// RED against current code: the replay returns the hard poison error (launch.go:154),
// so the first t.Fatalf fires.
func TestLaunchCrashReplay_W1_PoisonedOperationID_Redrive(t *testing.T) {
	for _, phase := range []idempotency.Phase{idempotency.PhasePrepared, idempotency.PhaseExecuting} {
		t.Run(string(phase), func(t *testing.T) {
			cfg := daemonConfig(t)
			const opID = "devA:01JCRASHW1REDRIVE000000000"
			const reservedID = "w1planted0reserved0session00"
			plantLaunchIdemRecord(t, cfg, opID, reservedID, phase)
			// Deliberately NO meta planted: the reserve saveMeta never ran (W1).

			d := openDaemon(t, cfg)
			killAllSessions(t, d)

			pidFile := filepath.Join(t.TempDir(), "agent.pid")
			spec := announceSpec(t, pidFile)
			spec.OperationID = opID

			m, err := d.Launch(spec) // replay of the poisoned key
			if err != nil {
				t.Fatalf("W1 replay returned an error (operation_id poisoned): %v; want a re-driven live session", err)
			}
			if m.Status.Process != status.ProcessRunning {
				t.Fatalf("W1 replay returned session %q in state %v; want Running (a live re-drive)", m.ID, m.Status.Process)
			}
			if got, ok := d.Get(m.ID); !ok || got.Status.Process != status.ProcessRunning {
				t.Fatalf("W1 re-driven session %q not present-and-running in registry (ok=%v status=%v)", m.ID, ok, got.Status.Process)
			}
			if !sessionInList(d, m.ID) {
				t.Fatalf("W1 re-driven session %q absent from d.List()", m.ID)
			}

			// item 3 (behavioral): the key is now stably resolved — a second replay
			// returns the SAME live session and spawns nothing new.
			m2, err := d.Launch(spec)
			if err != nil {
				t.Fatalf("W1 second replay errored: %v; the key must be stably resolved after re-drive", err)
			}
			if m2.ID != m.ID {
				t.Fatalf("W1 second replay produced a DIFFERENT session %q (first was %q); the re-driven key must be idempotent (no re-drive-forever)", m2.ID, m.ID)
			}
		})
	}
}

// TestLaunchCrashReplay_W3_SilentCorpse_NotReturnedAsSuccess pins fix-pack 4a item 2
// (DCR-1, window W3). After a restart over a state dir holding a `prepared`/
// `executing` launch idem record AND a reserved meta {Running, ShimPID=0} (which
// reconcile marks LOST on Open), a Launch replay with that operation_id MUST NOT
// return the LOST session as a successful launch. It must re-drive to a live Running
// session OR return a clearly retryable transient error — never a dead session as
// success.
//
// RED against current code: the replay returns the LOST reserved session with a nil
// error (launch.go:152), so the silent-corpse t.Fatalf fires.
func TestLaunchCrashReplay_W3_SilentCorpse_NotReturnedAsSuccess(t *testing.T) {
	for _, phase := range []idempotency.Phase{idempotency.PhasePrepared, idempotency.PhaseExecuting} {
		t.Run(string(phase), func(t *testing.T) {
			cfg := daemonConfig(t)
			const opID = "devA:01JCRASHW3CORPSE0000000000"
			const reservedID = "w3planted0reserved0session00"
			plantReservedMeta(t, cfg, reservedID) // meta {Running, ShimPID=0}
			plantLaunchIdemRecord(t, cfg, opID, reservedID, phase)

			d := openDaemon(t, cfg)
			killAllSessions(t, d)

			// Ground the premise: reconcile has marked the reserved corpse LOST.
			if got, ok := d.Get(reservedID); !ok || got.Status.Process != status.ProcessLost {
				t.Fatalf("precondition: reserved meta should be LOST after reconcile; got ok=%v status=%v", ok, got.Status.Process)
			}

			pidFile := filepath.Join(t.TempDir(), "agent.pid")
			spec := announceSpec(t, pidFile)
			spec.OperationID = opID

			m, err := d.Launch(spec) // replay
			if err != nil {
				return // acceptable: a clearly retryable transient error, no corpse returned
			}
			// Success path: must be a LIVE session, never the LOST corpse.
			if m.ID == reservedID && m.Status.Process == status.ProcessLost {
				t.Fatalf("W3 replay returned the LOST reserved session %q as a SUCCESSFUL launch (silent corpse)", reservedID)
			}
			if m.Status.Process != status.ProcessRunning {
				t.Fatalf("W3 replay succeeded but returned a non-Running session %q (state %v); a success must be a live re-drive", m.ID, m.Status.Process)
			}
			if got, ok := d.Get(m.ID); !ok || got.Status.Process != status.ProcessRunning {
				t.Fatalf("W3 replay success but session %q not present-and-running (ok=%v status=%v)", m.ID, ok, got.Status.Process)
			}
		})
	}
}

// TestLaunchCrashReplay_OpenTimeResolution_NoStalePreparedPointingAtDeadSession is
// the white-box SECOND guard for fix-pack 4a item 3. Behavioral coverage lives in
// the W1/W3 tests above; this pins the store-phase invariant directly: after Open +
// replay over a W1 state, the idem record for the operation_id must not remain
// in-flight (`prepared`/`executing`) while pointing at a session that is not a live
// Running session — that exact combination IS the poison/corpse the fix resolves.
// Mechanism-tolerant: if the fix moves the key out of the idem store entirely, the
// record is absent and there is nothing stale to leak.
//
// RED against current code: the poisoned replay leaves the record `prepared` still
// pointing at the reserved id, which has no live session -> t.Fatalf fires.
func TestLaunchCrashReplay_OpenTimeResolution_NoStalePreparedPointingAtDeadSession(t *testing.T) {
	cfg := daemonConfig(t)
	const opID = "devA:01JCRASHRESOLVE00000000000"
	const reservedID = "resolve0planted0reserved0000"
	plantLaunchIdemRecord(t, cfg, opID, reservedID, idempotency.PhasePrepared)

	d := openDaemon(t, cfg)
	killAllSessions(t, d)

	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := announceSpec(t, pidFile)
	spec.OperationID = opID
	_, _ = d.Launch(spec) // drive the resolver; the outcome is asserted behaviorally in W1

	rec, ok := d.idem.Get(opID)
	if !ok {
		return // key no longer lives in the idem store: nothing stale to leak
	}
	if rec.Phase == idempotency.PhasePrepared || rec.Phase == idempotency.PhaseExecuting {
		m, live := d.Get(rec.SessionID)
		if !live || m.Status.Process != status.ProcessRunning {
			t.Fatalf("idem record for %q is still in-flight (%s) but points at a dead/missing session %q (live=%v status=%v): the stale record was not resolved (poison/corpse)",
				opID, rec.Phase, rec.SessionID, live, m.Status.Process)
		}
	}
}

// TestLaunchCrashReplay_W4_LiveOrphanAgent_TODO documents window W4 — a crash after
// cmd.Start but before the identity saveMeta (launch.go ~L208-234) leaves a REAL
// shim+agent running with meta {Running, ShimPID=0}, on-disk INDISTINGUISHABLE from
// W3. Faithfully reproducing it needs a spawn-then-kill-9 integration harness that
// keeps a real orphan shim alive across the daemon restart (so a blind re-drive
// would double-spawn a live code-editing agent while the orphan still runs). That is
// out of scope for this RED slice; W1+W3 cover the poison + silent-corpse core.
// Tracked here so it is not silently dropped.
func TestLaunchCrashReplay_W4_LiveOrphanAgent_TODO(t *testing.T) {
	t.Skip("W4 (live orphan agent) needs a spawn-then-kill-9 integration harness keeping a real shim alive across restart; W1+W3 cover the poison + silent-corpse core. See fix-pack 4a / DCR-1.")
}
