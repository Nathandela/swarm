package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/worktree"
)

// requireGit skips when the git CLI is unavailable (mirrors internal/worktree's
// own seam; Create/Remove shell out to git).
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not found on PATH")
	}
}

// newGitRepo git-inits a fresh short-path temp dir with one commit, so a
// worktree has a HEAD to branch from.
func newGitRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir, err := os.MkdirTemp("", "dwt")
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

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestPreLaunchPreDeleteRegisterWorktreeHooks is the E12.3 design assertion:
// wiring internal/worktree's Create/Remove into Config.PreLaunch/PreDelete is
// the sanctioned way to attach launch-time isolation — daemon core carries no
// inline worktree branching. Driving this through a full Daemon.Launch/Delete
// would additionally require a real shim binary, so this pins the hook-point
// contract directly: it calls the registered hooks the same way the E12
// implementation's launch()/Delete() call sites will, using the real
// worktree.Create/Remove behind them.
func TestPreLaunchPreDeleteRegisterWorktreeHooks(t *testing.T) {
	repo := newGitRepo(t)

	cfg := Config{
		PreLaunch: func(id string, spec LaunchSpec) (string, error) {
			return worktree.Create(spec.Cwd, id)
		},
		PreDelete: func(m persist.Meta) error {
			return worktree.Remove(m.Cwd, m.ID)
		},
	}

	cwd, err := cfg.PreLaunch("sess1", LaunchSpec{Cwd: repo})
	if err != nil {
		t.Fatalf("PreLaunch: %v", err)
	}
	want := filepath.Join(repo, ".swarm", "worktrees", "sess1")
	if cwd != want {
		t.Fatalf("PreLaunch cwdOverride = %q, want %q", cwd, want)
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		t.Fatalf("PreLaunch did not produce a worktree dir at %q: %v", cwd, err)
	}

	if err := cfg.PreDelete(persist.Meta{ID: "sess1", Cwd: repo}); err != nil {
		t.Fatalf("PreDelete: %v", err)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatalf("worktree dir %q still exists after PreDelete", cwd)
	}
}

// TestHookFieldsAreOptional confirms the zero-value Config (no hooks
// registered) leaves PreLaunch/PreDelete nil. Every daemon test written before
// Epic 12 constructs Config{} without these fields, so none of them may start
// invoking a worktree hook once Epic 12 lands — the hook points are additive
// and opt-in, never on by default.
func TestHookFieldsAreOptional(t *testing.T) {
	var cfg Config
	if cfg.PreLaunch != nil {
		t.Fatalf("zero-value Config.PreLaunch = non-nil, want nil")
	}
	if cfg.PreDelete != nil {
		t.Fatalf("zero-value Config.PreDelete = non-nil, want nil")
	}
}
