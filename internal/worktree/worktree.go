// Package worktree implements Epic 12's launch-time isolation (S-3/R-3): Create
// prepares an isolated git worktree + branch for a session and Remove tears it
// down. Session ids are validated against path traversal (ADR-004) before
// anything ever touches git or disk.
package worktree

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/persist"
)

// validID reports whether id is safe to use as a path component and as a git
// branch-name suffix (ADR-004); it delegates to persist.ValidID, the single
// source of truth for the path-safe session-id pattern.
func validID(id string) bool {
	return persist.ValidID(id)
}

// worktreeDir returns the on-disk path for session id's worktree under repoDir.
func worktreeDir(repoDir, id string) string {
	return filepath.Join(repoDir, ".swarm", "worktrees", id)
}

// Create makes an isolated git worktree for session id at
// <repoDir>/.swarm/worktrees/<id>, on a new branch swarm/<id> branched from
// HEAD (S-3). id is validated before anything touches git or disk, so a
// rejected id never creates so much as the .swarm directory. A second Create
// for the same id fails — git refuses the already-existing branch and
// directory — leaving the first worktree intact.
func Create(repoDir, id string) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("worktree: invalid session id %q", id)
	}
	if out, err := exec.Command("git", "-C", repoDir, "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil {
		return "", fmt.Errorf("worktree: %s is not a git repository: %w\n%s", repoDir, err, out)
	}

	dir := worktreeDir(repoDir, id)
	branch := "swarm/" + id
	out, err := exec.Command("git", "-C", repoDir, "worktree", "add", "-b", branch, dir, "HEAD").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("worktree: git worktree add %s: %w\n%s", dir, err, out)
	}
	return dir, nil
}

// Remove tears down session id's worktree under repoDir: `git worktree remove
// --force` followed by `git worktree prune` (R-3). Removing an unknown session
// errors.
func Remove(repoDir, id string) error {
	if !validID(id) {
		return fmt.Errorf("worktree: invalid session id %q", id)
	}

	dir := worktreeDir(repoDir, id)
	if out, err := exec.Command("git", "-C", repoDir, "worktree", "remove", "--force", dir).CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: git worktree remove %s: %w\n%s", dir, err, out)
	}
	if out, err := exec.Command("git", "-C", repoDir, "worktree", "prune").CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: git worktree prune: %w\n%s", err, out)
	}
	return nil
}
