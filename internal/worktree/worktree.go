// Package worktree implements Epic 12's launch-time isolation (S-3/R-3): Create
// prepares an isolated git worktree + branch for a session and Remove tears it
// down. Session ids are validated against path traversal (ADR-004) before
// anything ever touches git or disk.
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/persist"
)

// maxSlugBytes keeps the generated path component and branch suffix comfortably
// below common filesystem and git-ref limits. The session id is always retained
// in full; only the cosmetic name prefix is truncated.
const maxSlugBytes = 128

// createMu makes the friendly unsuffixed path selection atomic for launches
// handled by the daemon. Swarm has one daemon per machine, so this covers two
// same-named launches arriving concurrently without introducing repo lock files.
var createMu sync.Mutex

// validID reports whether id is safe to use as a path component and as a git
// branch-name suffix (ADR-004); it delegates to persist.ValidID, the single
// source of truth for the path-safe session-id pattern.
func validID(id string) bool {
	return persist.ValidID(id)
}

// slugifyName turns a cosmetic session name into a safe, readable path/ref
// component. Letter and number runs are preserved (including Unicode), all other
// runs become one hyphen, and leading/trailing separators disappear.
func slugifyName(name string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if separator && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
			separator = false
			continue
		}
		separator = b.Len() > 0
	}
	return b.String()
}

// truncateUTF8 returns the longest rune-aligned prefix of s that occupies no
// more than max bytes.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// slugs returns the preferred readable directory component and the unique git
// branch component. The common named path is just the name slug; the branch
// retains the full session id. Empty or punctuation-only names keep the
// established id-only form.
func slugs(id, name string) (dirSlug, branchSlug string) {
	prefix := slugifyName(name)
	prefix = strings.TrimRight(truncateUTF8(prefix, maxSlugBytes), "-")
	if prefix == "" {
		return id, id
	}

	budget := maxSlugBytes - len(id) - 1
	if budget <= 0 {
		return prefix, id
	}
	branchPrefix := strings.TrimRight(truncateUTF8(prefix, budget), "-")
	if branchPrefix == "" {
		return prefix, id
	}
	return prefix, branchPrefix + "-" + id
}

// worktreeRoot is the only directory RemoveAt is allowed to target beneath.
func worktreeRoot(repoDir string) string {
	return filepath.Join(repoDir, ".swarm", "worktrees")
}

// worktreeDir returns the on-disk worktree path for a path-safe slug.
func worktreeDir(repoDir, slug string) string {
	return filepath.Join(worktreeRoot(repoDir), slug)
}

// Create makes an isolated git worktree for session id. When name has usable
// letters or numbers, its preferred directory is <name-slug> and its branch is
// swarm/<name-slug>-<id>. If that friendly directory is occupied by another
// same-named session, this session falls back to <name-slug>-<id>. An unnamed
// session retains the established id-only form. id is validated before anything
// touches git or disk, so a rejected id never creates so much as the .swarm
// directory.
func Create(repoDir, id, name string) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("worktree: invalid session id %q", id)
	}
	if out, err := exec.Command("git", "-C", repoDir, "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil {
		return "", fmt.Errorf("worktree: %s is not a git repository: %w\n%s", repoDir, err, out)
	}

	createMu.Lock()
	defer createMu.Unlock()

	dirComponent, branchComponent := slugs(id, name)
	dir := worktreeDir(repoDir, dirComponent)
	if _, err := os.Lstat(dir); err == nil {
		dirComponent = branchComponent
		dir = worktreeDir(repoDir, dirComponent)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("worktree: inspect candidate path %s: %w", dir, err)
	}
	branch := "swarm/" + branchComponent
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

	return RemoveAt(repoDir, worktreeDir(repoDir, id))
}

// RemoveAt tears down the exact worktree path persisted in Meta.AgentCwd. It
// accepts only a direct child of <repoDir>/.swarm/worktrees, so corrupt or
// malicious metadata cannot make teardown remove an arbitrary path. Remove is
// retained for sessions created by older versions, whose id was their path.
func RemoveAt(repoDir, dir string) error {
	rootAbs, err := filepath.Abs(worktreeRoot(repoDir))
	if err != nil {
		return fmt.Errorf("worktree: resolve worktree root: %w", err)
	}
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("worktree: resolve worktree path: %w", err)
	}
	if filepath.Dir(filepath.Clean(dirAbs)) != filepath.Clean(rootAbs) || filepath.Base(dirAbs) == "." {
		return fmt.Errorf("worktree: path %q is not a direct child of %s", dir, rootAbs)
	}
	if fi, err := os.Lstat(dirAbs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("worktree: path %q is a symlink", dir)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("worktree: inspect path %q: %w", dir, err)
	}
	if out, err := exec.Command("git", "-C", repoDir, "worktree", "remove", "--force", dirAbs).CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: git worktree remove %s: %w\n%s", dirAbs, err, out)
	}
	if out, err := exec.Command("git", "-C", repoDir, "worktree", "prune").CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: git worktree prune: %w\n%s", err, out)
	}
	return nil
}
