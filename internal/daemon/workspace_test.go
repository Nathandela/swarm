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
	current, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeAlias, err := filepath.Rel(current, alias)
	if err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{cfg.StateDir, workspace, alias, alias + "/..", relativeAlias + "/.."} {
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
		for _, throughAlias := range []bool{false, true} {
			name := map[bool]string{false: "prelaunch", true: "finalizer"}[finalizer]
			if throughAlias {
				name += "/symlink-parent"
			}
			t.Run(name, func(t *testing.T) {
				cfg := daemonConfig(t)
				resolved := cfg.StateDir
				if throughAlias {
					nested := filepath.Join(cfg.StateDir, "nested")
					if err := os.Mkdir(nested, 0700); err != nil {
						t.Fatal(err)
					}
					alias := filepath.Join(t.TempDir(), "state-link")
					if err := os.Symlink(nested, alias); err != nil {
						t.Fatal(err)
					}
					resolved = alias + "/.."
				}
				rolledBack := false
				cfg.PreLaunch = func(string, LaunchSpec) (string, error) {
					if !finalizer {
						return resolved, nil
					}
					return "", nil
				}
				if finalizer {
					cfg.FinalizeLaunch = func(_ string, spec LaunchSpec) (LaunchSpec, error) { spec.Cwd = resolved; return spec, nil }
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
}

func TestWorkspaceSymlinkParentResolvesBeforeCleaning(t *testing.T) {
	cfg := daemonConfig(t)
	outside := t.TempDir()
	nested := filepath.Join(outside, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(cfg.StateDir, "outside-link")
	if err := os.Symlink(nested, alias); err != nil {
		t.Fatal(err)
	}
	cwd := alias + "/.."
	dir, err := os.Open(cwd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	actual, err := dir.Stat()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(outside)
	if err != nil || !os.SameFile(actual, want) {
		t.Fatalf("fixture does not resolve to the outside directory: %v", err)
	}
	d := &Daemon{cfg: cfg}
	if err := d.validateWorkspace(cwd); err != nil {
		t.Fatalf("outside kernel working directory was refused: %v", err)
	}
}
