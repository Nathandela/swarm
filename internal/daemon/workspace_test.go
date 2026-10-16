package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestLaunchRefusesWorkspaceInsideStateBeforeReservation(t *testing.T) {
	cfg := daemonConfig(t)
	preLaunch := false
	cfg.PreLaunch = func(string, LaunchSpec) (string, error) { preLaunch = true; return "", nil }
	d := openDaemon(t, cfg)
	workspace := filepath.Join(cfg.StateDir, "legacy-workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{cfg.StateDir, workspace, alias} {
		if _, err := d.Launch(LaunchSpec{Cwd: cwd}); err == nil || !strings.Contains(err.Error(), "inside Swarm state") {
			t.Fatalf("state workspace %q was not refused: %v", cwd, err)
		}
	}
	if preLaunch || len(d.List()) != 0 {
		t.Fatal("refused workspace mutated launch state")
	}
	if err := d.validateWorkspace(t.TempDir()); err != nil {
		t.Fatalf("outside project refused: %v", err)
	}
}

func TestLaunchRefusesResolvedStateWorkspaceAndRollsBack(t *testing.T) {
	for _, finalizer := range []bool{false, true} {
		t.Run(map[bool]string{false: "prelaunch", true: "finalizer"}[finalizer], func(t *testing.T) {
			cfg := daemonConfig(t)
			rolledBack := false
			cfg.PreLaunch = func(string, LaunchSpec) (string, error) {
				if !finalizer {
					return cfg.StateDir, nil
				}
				return "", nil
			}
			if finalizer {
				cfg.FinalizeLaunch = func(_ string, spec LaunchSpec) (LaunchSpec, error) { spec.Cwd = cfg.StateDir; return spec, nil }
			}
			cfg.PreDelete = func(persist.Meta) error { rolledBack = true; return nil }
			d := openDaemon(t, cfg)
			if _, err := d.Launch(LaunchSpec{Cwd: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "inside Swarm state") {
				t.Fatalf("resolved state workspace was not refused: %v", err)
			}
			if !rolledBack || len(d.List()) != 0 {
				t.Fatal("refused resolved workspace left a launch reservation or hook side effect")
			}
		})
	}
}
