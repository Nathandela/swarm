package worktree

// FAILING-FIRST for ADR-010 Amendment 10: an agent handoff is isolated in a worktree
// exactly when its checkout is a git work tree, so the decision needs a predicate that
// answers for a repository root, a directory inside one, and anything else.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsRepoAnswersForRootsSubdirectoriesAndPlainDirectories(t *testing.T) {
	repo := newGitRepo(t)
	sub := filepath.Join(repo, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	plain := t.TempDir()

	for _, tc := range []struct {
		name string
		dir  string
		want bool
	}{
		{"repository root", repo, true},
		{"directory inside the repository", sub, true},
		{"plain directory", plain, false},
		{"missing directory", filepath.Join(plain, "gone"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRepo(tc.dir); got != tc.want {
				t.Fatalf("IsRepo(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}
}
