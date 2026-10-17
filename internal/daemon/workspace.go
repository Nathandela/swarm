package daemon

import (
	"fmt"
	"os"
	"path/filepath"
)

// State contains supervision records, never an agent's project or build tree.
func (d *Daemon) validateWorkspace(cwd string) error {
	// Resolve links before cleaning so link/.. matches the kernel's directory.
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return fmt.Errorf("launch: working directory: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("launch: working directory: %w", err)
	}
	state, err := os.Stat(d.cfg.StateDir)
	if err != nil {
		return fmt.Errorf("launch: state directory: %w", err)
	}
	for path := resolved; ; path = filepath.Dir(path) {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("launch: working directory: %w", err)
		}
		if os.SameFile(state, info) {
			return fmt.Errorf("launch: working directory is inside Swarm state; choose a project directory outside Swarm state")
		}
		if filepath.Dir(path) == path {
			return nil
		}
	}
}
