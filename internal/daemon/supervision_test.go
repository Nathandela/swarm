package daemon

// FAILING-FIRST test for ADR-010 Amendment 3, daemon hop: the daemon stamps the
// supervision mode from the LaunchSpec into the reserved meta beside the spawn
// lineage, so it is on disk before any agent process exists and survives a crash
// (spawnlineage_test.go is the shape this follows).
//
// FROZEN API:
//
//	type LaunchSpec struct {
//	    ...
//	    Supervision string // "", passive, manual, none; stamped into meta verbatim
//	}
//
// RED today: LaunchSpec has no Supervision field, so this file fails to compile.

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestLaunch_StampsSupervisionIntoMeta(t *testing.T) {
	for _, mode := range []string{"passive", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			d := openDaemon(t, daemonConfig(t))

			spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
			spec.SpawnedFrom = "sess-parent-1"
			spec.SpawnIntent = "handoff"
			spec.Supervision = mode

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
			if reserved.Supervision != mode {
				t.Errorf("reserved meta supervision = %q; want spec.Supervision %q stamped verbatim", reserved.Supervision, mode)
			}
			d.abandon()
		})
	}
}
