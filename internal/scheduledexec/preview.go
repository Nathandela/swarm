package scheduledexec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/Nathandela/swarm/internal/worktree"
)

// PreviewWorkspace checks the current launch constraints without creating a
// worktree, reserving an occurrence or starting an agent. Dispatch checks again.
func PreviewWorkspace(stateDir string, spec schedule.JobSpec, active []schedule.Occurrence, liveConflict func(string) (string, error)) (string, error) {
	if !filepath.IsAbs(spec.Directory) {
		return "", errors.New("choose an absolute working directory")
	}
	run := schedule.Occurrence{ID: schedule.NewID(time.Now()), Spec: spec}
	source, ws, err := describeWorkspace(stateDir, run, false)
	if err != nil {
		return "", err
	}
	if err = requireDirectory(source); err != nil {
		return "", err
	}
	for _, existing := range active {
		if existing.BlocksSelectedDirectory(spec) {
			return "", fmt.Errorf("workspace overlaps the scheduled directory %s; choose a dedicated checkout or allow independent Git worktrees", existing.Spec.Directory)
		}
	}
	conflictAt := func(path string) (string, error) {
		conflict, err := liveConflict(path)
		if err != nil || conflict != "" {
			return conflict, err
		}
		for _, r := range active {
			if ownsWorkspace(r) && worktree.PathsOverlap(path, r.Workspace) {
				return r.Workspace, nil
			}
		}
		return "", nil
	}
	var conflict string
	if spec.Workspace == schedule.WorkspaceDirectory {
		if worktree.PathsOverlap(ws, stateDir) {
			return "", fmt.Errorf("working directory %s overlaps Swarm state at %s; choose a separate directory", ws, stateDir)
		}
		conflict, err = conflictAt(ws)
		if err != nil {
			return "", err
		}
		if !spec.AllowOverlap {
			if conflict != "" {
				return "", fmt.Errorf("workspace overlaps %s; choose a dedicated directory or an isolated Git worktree", conflict)
			}
			return "", nil
		}
	}
	ws, err = previewIsolatedWorkspace(stateDir, run)
	if err != nil {
		if spec.Workspace == schedule.WorkspaceDirectory && conflict == "" {
			return fmt.Sprintf("This directory can run while free. Independent overlapping runs need an available Git worktree: %v", err), nil
		}
		if conflict != "" {
			return "", fmt.Errorf("workspace overlaps %s; choose a dedicated Git checkout for isolation: %w", conflict, err)
		}
		return "", err
	}
	blocked, err := conflictAt(ws)
	if err != nil {
		return "", err
	}
	if blocked != "" {
		if spec.Workspace == schedule.WorkspaceDirectory && conflict == "" {
			return fmt.Sprintf("This directory can run while free. Its isolated worktree would overlap %s.", blocked), nil
		}
		return "", fmt.Errorf("isolated worktree overlaps %s; choose a separate location for the Swarm state directory", blocked)
	}
	if conflict != "" {
		return fmt.Sprintf("Workspace overlaps %s. Swarm will use an isolated Git worktree while it is busy.", conflict), nil
	}
	return "", nil
}

func previewIsolatedWorkspace(stateDir string, run schedule.Occurrence) (string, error) {
	source, ws, err := describeWorkspace(stateDir, run, true)
	if err != nil {
		return "", err
	}
	if _, err = gitLine(source, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return "", errors.New("isolated worktrees need a Git checkout with a commit; select a committed checkout")
	}
	prefix, err := gitLine(source, "rev-parse", "--show-prefix")
	if err != nil {
		return "", err
	}
	if prefix != "" {
		kind, err := gitLine(source, "cat-file", "-t", "HEAD:"+strings.TrimSuffix(prefix, "/"))
		if err != nil || kind != "tree" {
			return "", fmt.Errorf("working directory %s is absent from the committed checkout; select a tracked directory for isolation", source)
		}
	}
	root := scheduledWorktreeRoot(stateDir)
	if fi, err := os.Lstat(root); err == nil {
		if !fi.IsDir() || fi.Mode().Perm()&0077 != 0 {
			return "", fmt.Errorf("worktree directory %s must be a private directory", root)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return ws, nil
}
