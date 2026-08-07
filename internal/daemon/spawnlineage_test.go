package daemon

// FAILING-FIRST test for ADR-010 Phase 2 PIECE 1, daemon hop: the daemon stamps the
// spawn lineage from the LaunchSpec into the session meta at reservation time, so
// the link is on disk before any agent process exists and survives a crash. This is
// the same seam (and the same crash-injection probe) the P2 naming stamp uses —
// naming_test.go:TestLaunch_StampsSpecNameIntoMeta is the shape this follows.
//
// FROZEN API:
//
//	type LaunchSpec struct {
//	    ...
//	    // SpawnedFrom, when non-empty, is the LOCAL id of the session that spawned
//	    // this one (ADR-010 D4), and SpawnIntent is "handoff" or "delegate". The
//	    // daemon only stamps them into meta — exactly as it does ResumedFrom.
//	    SpawnedFrom string
//	    SpawnIntent string
//	}
//
// RED today: LaunchSpec has neither field, so this file fails to compile on them.

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestLaunch_StampsSpawnLineageIntoMeta(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.SpawnedFrom = "sess-parent-1"
	spec.SpawnIntent = "delegate"

	var reserved persist.Meta
	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseReserved {
			reserved = m
			return errInjectedCrash // abort before spawning a real agent
		}
		return nil
	}
	if _, err := d.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want injected crash", err)
	}
	if reserved.SpawnedFrom != "sess-parent-1" {
		t.Errorf("daemon must stamp spec.SpawnedFrom into the reserved meta; got %q", reserved.SpawnedFrom)
	}
	if reserved.SpawnIntent != "delegate" {
		t.Errorf("daemon must stamp spec.SpawnIntent into the reserved meta; got %q", reserved.SpawnIntent)
	}
	d.abandon()
}

// TestLaunch_UnlineagedLaunchStampsNothing is the contrast case: an ordinary launch
// (the overwhelming majority) leaves both fields empty, so no session is ever shown
// a lineage badge it did not earn.
func TestLaunch_UnlineagedLaunchStampsNothing(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))

	var reserved persist.Meta
	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseReserved {
			reserved = m
			return errInjectedCrash
		}
		return nil
	}
	if _, err := d.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want injected crash", err)
	}
	if reserved.SpawnedFrom != "" || reserved.SpawnIntent != "" {
		t.Errorf("an un-lineaged launch stamped (%q, %q); want both empty", reserved.SpawnedFrom, reserved.SpawnIntent)
	}
	d.abandon()
}
