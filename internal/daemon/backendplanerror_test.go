package daemon

// Lifecycle plan R1: the degraded-backend reason becomes durable. The 2026-08-28
// field incident was a session whose PTY ran while its backend was never planned
// ("codex does not resolve on PATH") -- and the only witness was one daemon.log
// line, which is how it cost a debugging session to root-cause. launch() now
// persists the planner's refusal onto the meta, where ls, the TUI and doctor can
// read it.
//
// The differential: a planner that refuses stamps the reason; a planner that
// plans (or no planner, the ordinary case) leaves the field empty AND absent
// from the durable JSON (omitempty), so a healthy meta.json serializes exactly
// as it did before the field existed.

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLaunchPersistsTheBackendPlanRefusal(t *testing.T) {
	cfg := daemonConfig(t)
	cfg.BackendPlanner = func(agentType, sessionDir, socketPath string, agentEnv []string) (*BackendSpec, error) {
		return nil, errors.New(`backend Program "codex" does not resolve on PATH`)
	}
	d := openDaemon(t, cfg)

	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := announceSpec(t, pidFile)
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: a backend refusal must degrade, never fail the launch: %v", err)
	}
	t.Cleanup(func() { killTree(readPIDFile(t, pidFile)); killTree(m.ShimPID) })

	want := `backend Program "codex" does not resolve on PATH`
	if m.BackendPlanError != want {
		t.Errorf("returned meta BackendPlanError = %q, want %q", m.BackendPlanError, want)
	}
	persisted, raw := loadPersistedMeta(t, cfg, m.ID)
	if persisted.BackendPlanError != want {
		t.Errorf("persisted backend_plan_error = %q, want %q -- the reason exists only inside "+
			"spawnShim unless it is written down", persisted.BackendPlanError, want)
	}
	if _, ok := raw["backend_plan_error"]; !ok {
		t.Error("backend_plan_error missing from the durable JSON of a degraded session")
	}
}

func TestLaunchWithAPlannedBackendLeavesNoPlanError(t *testing.T) {
	cfg := daemonConfig(t)
	// No BackendPlanner at all: the ordinary case for every CLI that needs none.
	d := openDaemon(t, cfg)

	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := announceSpec(t, pidFile)
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { killTree(readPIDFile(t, pidFile)); killTree(m.ShimPID) })

	if m.BackendPlanError != "" {
		t.Errorf("BackendPlanError = %q on a healthy launch, want empty", m.BackendPlanError)
	}
	_, raw := loadPersistedMeta(t, cfg, m.ID)
	if _, ok := raw["backend_plan_error"]; ok {
		t.Error("backend_plan_error present in a healthy meta.json -- omitempty must keep the " +
			"durable key set exactly as it was before the field existed")
	}
}
