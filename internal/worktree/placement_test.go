package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateUsesPrimaryPlacementAndLegacySourceCommit(t *testing.T) {
	repo := newGitRepo(t)
	parent, err := Create(repo, "parent-id", "parent")
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(parent, ".scratch", "cutover")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	child := worktreeDir(scratch, "child")
	runGit(t, scratch, "worktree", "add", "-qb", "swarm/child-child-id", child, "HEAD")
	deep := worktreeDir(child, "grandchild")
	runGit(t, child, "worktree", "add", "-qb", "swarm/grandchild-deep-id", deep, "HEAD")
	runGit(t, deep, "commit", "--allow-empty", "-qm", "deep source")
	if err := os.WriteFile(filepath.Join(deep, "untracked"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "source")
	if err := os.Symlink(deep, alias); err != nil {
		t.Fatal(err)
	}
	destination, err := Create(alias, "sibling-id", "sibling")
	if err != nil || destination != worktreeDir(repo, "sibling") {
		t.Fatalf("flattened destination = %q, %v", destination, err)
	}
	if got, want := runGit(t, destination, "rev-parse", "HEAD"), runGit(t, deep, "rev-parse", "HEAD"); got != want {
		t.Fatalf("child HEAD %q, want source HEAD %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(destination, "untracked")); !os.IsNotExist(err) {
		t.Fatalf("source dirt appeared in new checkout: %v", err)
	}
	for _, test := range []struct{ from, id, name, want string }{
		{alias, "sibling-id", "sibling", destination},
		{scratch, "child-id", "child", child},
		{child, "deep-id", "grandchild", deep},
	} {
		if found, err := ReservedCheckout(test.from, test.id, test.name); err != nil || found != test.want {
			t.Fatalf("recovery for %s = %q, %v", test.id, found, err)
		}
	}
	if err := RemoveAt(repo, parent, "parent-id"); err == nil || !strings.Contains(err.Error(), "registered Git worktree") {
		t.Fatalf("legacy parent removal = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(deep, "untracked")); err != nil || string(got) != "keep" {
		t.Fatalf("legacy dirty data = %q, %v", got, err)
	}
}

func TestRemoveOwnedSiblingAfterSourceDisappearsAndRejectReplacement(t *testing.T) {
	repo := newGitRepo(t)
	parent, err := Create(repo, "parent-id", "parent")
	if err != nil {
		t.Fatal(err)
	}
	child, err := Create(parent, "child-id", "child")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, child, "checkout", "--detach", "HEAD")
	if err := RemoveAt(repo, parent, "parent-id"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAt(parent, child, "child-id"); err != nil {
		t.Fatal(err)
	}
	replacement, err := Create(repo, "other-id", "child")
	if err != nil || replacement != child {
		t.Fatalf("replacement = %q, %v", replacement, err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "untracked"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAt(repo, child, "child-id"); err == nil {
		t.Fatal("old ownership erased replacement")
	}
	if got, err := os.ReadFile(filepath.Join(replacement, "untracked")); err != nil || string(got) != "keep" {
		t.Fatalf("replacement data = %q, %v", got, err)
	}
}

func TestCreateChecksPhysicalDestinationAndRefusesOccupiedFallback(t *testing.T) {
	repo := newGitRepo(t)
	src := filepath.Join(repo, "subdir")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("outside allowed roots")
	want := worktreeDir(repo, "same")
	if _, err := CreateChecked(src, "id", "same", func(path string) error {
		if path != want {
			t.Fatalf("policy received %q, want %q", path, want)
		}
		return denied
	}); !errors.Is(err, denied) {
		t.Fatalf("creation after policy refusal = %v", err)
	}
	if _, err := os.Stat(worktreeRoot(repo)); !os.IsNotExist(err) {
		t.Fatalf("refused launch created worktree directory: %v", err)
	}
	for _, slug := range []string{"same", "same-id"} {
		if err := os.MkdirAll(worktreeDir(repo, slug), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Create(src, "id", "same"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("occupied fallback = %v", err)
	}
	if branchExists(t, repo, "swarm/same-id") {
		t.Fatal("occupied fallback was registered")
	}
}

func TestCreateRefusesDestinationRedirectedIntoLinkedCheckout(t *testing.T) {
	repo := newGitRepo(t)
	external := filepath.Join(t.TempDir(), "linked")
	runGit(t, repo, "worktree", "add", "-qb", "external", external, "HEAD")
	if err := os.Symlink(external, filepath.Join(repo, ".swarm")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(repo, "id", "name"); err == nil {
		t.Fatal("created a checkout recursively through a destination alias")
	}
	if _, err := os.Stat(filepath.Join(external, "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("refused launch changed redirected destination: %v", err)
	}
}

func TestCreateRefusesRedirectInstalledDuringPolicyCheck(t *testing.T) {
	repo := newGitRepo(t)
	external := filepath.Join(t.TempDir(), "linked")
	runGit(t, repo, "worktree", "add", "-qb", "external", external, "HEAD")
	if _, err := CreateChecked(repo, "id", "name", func(string) error {
		return os.Symlink(external, filepath.Join(repo, ".swarm"))
	}); err == nil {
		t.Fatal("created a checkout after its policy destination was redirected")
	}
	if _, err := os.Stat(filepath.Join(external, "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("refused launch changed redirected destination: %v", err)
	}
}

func TestRemoveRetainsIndependentRepositoryWithinWorktree(t *testing.T) {
	repo := newGitRepo(t)
	parent, err := Create(repo, "parent-id", "parent")
	if err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(parent, ".scratch", "inner")
	runGit(t, repo, "init", "-q", inner)
	if err := os.WriteFile(filepath.Join(inner, "untracked"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAt(repo, parent, "parent-id"); err == nil {
		t.Fatal("removed a worktree containing an independent repository")
	}
	if got, err := os.ReadFile(filepath.Join(inner, "untracked")); err != nil || string(got) != "keep" {
		t.Fatalf("nested repository data = %q, %v", got, err)
	}
}
