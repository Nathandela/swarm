package daemon

// v0.4 P2 — session naming (bd agents-tracker-4e2). The daemon stamps
// LaunchSpec.Name into the session meta at reservation time, so a user-provided
// label survives to disk and to every SessionView. Verified through the
// crash-injection seam at the reserved phase, before any agent process spawns.

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestLaunch_StampsSpecNameIntoMeta(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.Name = "backend-refactor"

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
	if reserved.Name != "backend-refactor" {
		t.Fatalf("daemon must stamp spec.Name into the reserved meta; got %q", reserved.Name)
	}
	d.abandon()
}
