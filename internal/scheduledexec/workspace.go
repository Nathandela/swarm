package scheduledexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/Nathandela/swarm/internal/worktree"
)

func describeWorkspace(stateDir string, run schedule.Occurrence, isolate bool) (source, workspace string, err error) {
	source, err = filepath.EvalSymlinks(run.Spec.Directory)
	if err != nil {
		return "", "", err
	}
	if !isolate {
		return source, source, nil
	}
	root, err := gitLine(source, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("isolated workspace requires a Git working tree: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(root, source)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("job directory is outside its Git working tree")
	}
	target := filepath.Join(scheduledWorktreeRoot(stateDir), run.ID)
	if worktree.PathsOverlap(root, target) {
		return "", "", errors.New("the scheduled worktree location is inside the source checkout; use a daemon state directory outside the repository")
	}
	return source, filepath.Join(target, relative), nil
}

// ensureWorkspace adopts only the exact named worktree owned by this
// occurrence. A partial Git operation gets surfaced and preserved for repair.
func ensureWorkspace(stateDir string, run schedule.Occurrence) error {
	if run.Workspace == run.SourceDirectory {
		return requireCanonicalDirectory(run.Workspace)
	}
	if !filepath.IsLocal(run.ID) || filepath.Base(run.ID) != run.ID {
		return errors.New("invalid scheduled occurrence ID")
	}
	root := scheduledWorktreeRoot(stateDir)
	if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	fi, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0077 != 0 {
		return errors.New("scheduled worktree directory must be private and cannot be a symlink")
	}
	target := filepath.Join(root, run.ID)
	branch := "swarm/automation/" + run.ID
	if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		if _, err := git(run.SourceDirectory, "worktree", "add", "-b", branch, target, "HEAD"); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	if resolved != target {
		return errors.New("expected scheduled worktree is a symlink")
	}
	origin, err := gitLine(run.SourceDirectory, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	actual, err := gitLine(target, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	actualBranch, err := gitLine(target, "symbolic-ref", "HEAD")
	if err != nil {
		return err
	}
	actualRoot, err := gitLine(target, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if actualRoot != target || origin != actual || actualBranch != "refs/heads/"+branch {
		return errors.New("existing scheduled workspace belongs to a different worktree or branch")
	}
	workspace, err := filepath.EvalSymlinks(run.Workspace)
	if err != nil {
		return err
	}
	if workspace != run.Workspace {
		return errors.New("scheduled working directory changed via a symlink")
	}
	return requireDirectory(workspace)
}

// Worktrees hold project files and must stay outside the supervisor's
// private session state. The stable sibling name isolates independent daemons.
func scheduledWorktreeRoot(stateDir string) string {
	sum := sha256.Sum256([]byte(stateDir))
	return filepath.Join(filepath.Dir(stateDir), fmt.Sprintf("swarm-scheduled-worktrees-%x", sum[:6]))
}

func requireCanonicalDirectory(path string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if canonical != path {
		return errors.New("scheduled directory changed via a symlink")
	}
	return requireDirectory(path)
}

func requireDirectory(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("scheduled working directory is unavailable: %s", path)
	}
	return nil
}

func gitLine(dir string, args ...string) (string, error) {
	output, err := git(dir, args...)
	return strings.TrimSuffix(string(output), "\n"), err
}

func git(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w (%s)", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
