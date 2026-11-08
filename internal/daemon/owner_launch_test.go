package daemon

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestOwnerLaunchExactRetryAndConflict(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	killAllSessions(t, d)
	spec := announceSpec(t, t.TempDir()+"/pid")
	spec.OperationID, spec.OwnerLaunchHash = "launch:exact", "exact-request"
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Reconciliation precedes capacity and mutable runtime/source validation.
	spec.Cwd = "/no-longer-present"
	again, err := d.Launch(spec)
	if err != nil || again.ID != m.ID {
		t.Fatalf("exact retry = %s, %v; want %s", again.ID, err, m.ID)
	}
	spec.OwnerLaunchHash = "different-request"
	if _, err := d.Launch(spec); err == nil {
		t.Fatal("changed request inherited original receipt")
	}
	if len(d.List()) != 1 {
		t.Fatal("retry created another session")
	}
	if err := d.Kill(m.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	spec.OwnerLaunchHash = "exact-request"
	if _, err := d.Launch(spec); !errors.Is(err, ErrLaunchOpConsumed) {
		t.Fatalf("deleted worker replay = %v", err)
	}
	out, known := d.OwnerLaunchOutcome(spec.OperationID)
	if !known || out.State != OpStateApplied || out.SessionID != m.ID || len(d.List()) != 0 {
		t.Fatalf("deleted launch lost receipt or spawned: %+v, %v", out, known)
	}
}

func TestOwnerLaunchCrashReservationNeverRedrives(t *testing.T) {
	for _, phase := range []idempotency.Phase{idempotency.PhasePrepared, idempotency.PhaseExecuting} {
		t.Run(string(phase), func(t *testing.T) {
			cfg := daemonConfig(t)
			st, err := idempotency.Open(cfg.StateDir + "/idempotency")
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = st.PrepareBound("launch:crash", "owner_launch", "original", "", "exact")
			if err != nil {
				t.Fatal(err)
			}
			if phase == idempotency.PhaseExecuting {
				if err := st.Begin("launch:crash"); err != nil {
					t.Fatal(err)
				}
			}
			d := openDaemon(t, cfg)
			killAllSessions(t, d)
			spec := announceSpec(t, t.TempDir()+"/pid")
			spec.OperationID, spec.OwnerLaunchHash = "launch:crash", "exact"
			if _, err := d.Launch(spec); !errors.Is(err, ErrLaunchOutcomeUnknown) {
				t.Fatalf("crash retry = %v", err)
			}
			if len(d.List()) != 0 {
				t.Fatal("ambiguous launch spawned a replacement")
			}
			out, known := d.OwnerLaunchOutcome("launch:crash")
			if !known || out.State != OpStateOutcomeUnknown {
				t.Fatalf("status = %+v, %v", out, known)
			}
		})
	}
}

func TestOwnerLaunchLostReplyReconcilesOriginalAfterRestart(t *testing.T) {
	cfg := daemonConfig(t)
	d1 := openDaemon(t, cfg)
	spec := announceSpec(t, t.TempDir()+"/pid")
	spec.OperationID, spec.OwnerLaunchHash = "launch:lost-reply", "exact"
	m, err := d1.launch(spec, func(phase launchPhase, _ persist.Meta) error {
		if phase == phaseConfirmed {
			return errInjectedCrash
		}
		return nil
	})
	if !errors.Is(err, errInjectedCrash) {
		t.Fatal(err)
	}
	if err := d1.Close(); err != nil {
		t.Fatal(err)
	}
	d2 := openDaemon(t, cfg)
	killAllSessions(t, d2)
	again, err := d2.Launch(spec)
	if err != nil || again.ID != m.ID {
		t.Fatalf("lost reply retry = %s, %v, want %s", again.ID, err, m.ID)
	}
	out, known := d2.OwnerLaunchOutcome(spec.OperationID)
	if !known || out.State != OpStateApplied || out.SessionID != m.ID {
		t.Fatalf("status = %+v, %v", out, known)
	}
}
