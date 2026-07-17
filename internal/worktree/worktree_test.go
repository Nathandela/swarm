// Package worktree provides Epic 12's launch-time isolation hooks (S-3/R-3):
// Create prepares an isolated git worktree for a session and Remove tears it
// down. These tests pin the Epic 12 RED state: Create, Remove, and validID do
// not exist yet, so this file fails to compile until they are implemented.
package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit skips the test when the git CLI is unavailable (E12 assumption:
// Create/Remove shell out to plain `git`).
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not found on PATH")
	}
}

// runGit runs git in dir and fails the test on a non-zero exit.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newGitRepo git-inits a fresh short-path temp dir with one commit (a worktree
// needs a HEAD to branch from) and registers its cleanup. The short "wt" prefix
// keeps the path well under macOS's AF_UNIX sun_path cap, matching the rest of
// this project's temp-dir convention even though these tests open no sockets.
func newGitRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir, err := os.MkdirTemp("", "wt")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runGit(t, dir, "add", "seed.txt")
	runGit(t, dir, "commit", "-q", "-m", "seed")
	return dir
}

// branchExists reports whether repoDir has a local branch named name.
func branchExists(t *testing.T, repoDir, name string) bool {
	t.Helper()
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

// worktreeListed reports whether repoDir's `git worktree list` registers dir.
// Git reports worktree paths resolved through symlinks (e.g. macOS's
// /var -> /private/var), so both sides are resolved before comparing.
func worktreeListed(t *testing.T, repoDir, dir string) bool {
	t.Helper()
	out := runGit(t, repoDir, "worktree", "list", "--porcelain")
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		want = dir // dir may already be gone; fall back to a raw match
	}
	for _, line := range strings.Split(out, "\n") {
		p, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if got, err := filepath.EvalSymlinks(p); err == nil && got == want {
			return true
		}
		if p == dir {
			return true
		}
	}
	return false
}

// --- E12.1: Create -----------------------------------------------------------

func TestCreateMakesWorktreeAndBranch(t *testing.T) {
	repo := newGitRepo(t)

	dir, err := Create(repo, "sess1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	wantDir := filepath.Join(repo, ".swarm", "worktrees", "sess1")
	if dir != wantDir {
		t.Fatalf("worktreeDir = %q, want %q", dir, wantDir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("Create did not produce a directory at %q: %v", dir, err)
	}
	if !branchExists(t, repo, "swarm/sess1") {
		t.Fatalf("branch swarm/sess1 was not created")
	}
	if !worktreeListed(t, repo, dir) {
		t.Fatalf("git worktree list does not show %q", dir)
	}
}

func TestCreateRejectsPathTraversalID(t *testing.T) {
	repo := newGitRepo(t)

	for _, id := range []string{"..", "../evil", "a/../../b", "/etc/passwd", ""} {
		dir, err := Create(repo, id)
		if err == nil {
			t.Errorf("Create(%q): want error, got dir %q", id, dir)
		}
		if dir != "" {
			t.Errorf("Create(%q): want empty dir on error, got %q", id, dir)
		}
	}
	// A rejected id must never reach git: no .swarm dir should exist at all.
	if _, err := os.Stat(filepath.Join(repo, ".swarm")); !os.IsNotExist(err) {
		t.Fatalf(".swarm was created despite every id being rejected")
	}
}

func TestCreateInNonRepoDirErrors(t *testing.T) {
	requireGit(t)
	dir, err := os.MkdirTemp("", "notrepo")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	got, err := Create(dir, "sess1")
	if err == nil {
		t.Fatalf("Create in a non-repo dir: want error, got dir %q", got)
	}
	if got != "" {
		t.Fatalf("Create in a non-repo dir: want empty dir on error, got %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".swarm")); !os.IsNotExist(statErr) {
		t.Fatalf(".swarm was created in a non-repo directory")
	}
}

func TestCreateCollisionOnSecondCall(t *testing.T) {
	repo := newGitRepo(t)

	if _, err := Create(repo, "sess1"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	// A second Create for the SAME id, without an intervening Remove, collides on
	// both the existing worktree directory and the existing branch. It must fail
	// clearly rather than silently reusing or corrupting the first worktree.
	dir, err := Create(repo, "sess1")
	if err == nil {
		t.Fatalf("second Create(sess1): want error, got dir %q", dir)
	}
	if !branchExists(t, repo, "swarm/sess1") {
		t.Fatalf("original branch swarm/sess1 was lost after a colliding Create")
	}
	if !worktreeListed(t, repo, filepath.Join(repo, ".swarm", "worktrees", "sess1")) {
		t.Fatalf("original worktree was lost after a colliding Create")
	}
}

// --- E12.2: Remove -------------------------------------------------------------

func TestRemoveTearsDownWorktree(t *testing.T) {
	repo := newGitRepo(t)
	dir, err := Create(repo, "sess1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := Remove(repo, "sess1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("worktree dir %q still exists after Remove", dir)
	}
	if worktreeListed(t, repo, dir) {
		t.Fatalf("git worktree list still shows %q after Remove", dir)
	}
}

func TestRemoveUnknownSessionErrors(t *testing.T) {
	repo := newGitRepo(t)
	if err := Remove(repo, "never-created"); err == nil {
		t.Fatalf("Remove of a session with no worktree: want error, got nil")
	}
}

// --- validID ---------------------------------------------------------------

func TestValidID(t *testing.T) {
	valid := []string{"a", "sess1", "abc-def_123", "A.B.C", strings.Repeat("a", 128)}
	invalid := []string{"", ".", "..", "-abc", "a/b", "../evil", "/etc/passwd", strings.Repeat("a", 129), "a b"}

	for _, id := range valid {
		if !validID(id) {
			t.Errorf("validID(%q) = false, want true", id)
		}
	}
	for _, id := range invalid {
		if validID(id) {
			t.Errorf("validID(%q) = true, want false", id)
		}
	}
}
