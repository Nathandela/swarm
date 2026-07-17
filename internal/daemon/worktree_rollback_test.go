package daemon

// Epic 12 review, finding F2 (distinct from this package's own pre-existing
// F2/N2 identity-read-failure scenario in daemon_fixes_test.go — the label
// collides by coincidence): PreLaunch may succeed and create a real git
// worktree before a LATER launch step fails. Every such rollback goes through
// dropReserved, which erases the meta entirely — so unless something
// compensates for PreLaunch's side effect during that same rollback, no future
// Delete() can ever look this session id up again, and the worktree (and its
// branch) leak permanently. This test drives a real Launch through the daemon
// with real worktree.Create/Remove wired as PreLaunch/PreDelete, injects a
// failure on the identity-read step (which only runs after the shim, and so
// PreLaunch, already succeeded), and asserts the worktree directory does not
// survive the failed launch.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/worktree"
)

func TestLaunchRollback_CompensatesPreLaunchWorktree(t *testing.T) {
	repo := newGitRepo(t) // hookpoints_test.go helper: git-inits a fresh repo with one commit

	cfg := daemonConfig(t)
	cfg.PreLaunch = func(id string, spec LaunchSpec) (string, error) {
		return worktree.Create(spec.Cwd, id)
	}
	cfg.PreDelete = func(m persist.Meta) error {
		return worktree.Remove(m.Cwd, m.ID)
	}
	d := openDaemon(t, cfg)

	// Inject a failure on the post-spawn identity read, the earliest rollback
	// point that runs strictly after spawnShim (and so PreLaunch) has already
	// succeeded.
	orig := procStartTimeFn
	procStartTimeFn = func(pid int) (int64, error) {
		return 0, errors.New("injected identity-read failure")
	}
	defer func() { procStartTimeFn = orig }()

	spec := LaunchSpec{
		AgentType: "fake",
		Argv:      []string{"/bin/sh", "-c", "while :; do sleep 1; done"},
		Cwd:       repo,
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      80,
		Rows:      24,
	}
	if _, err := d.Launch(spec); err == nil {
		t.Fatalf("Launch: want error from injected identity-read failure, got nil")
	}

	// dropReserved must have erased the reservation entirely: nothing left in
	// the registry to ever Delete() later.
	if list := d.List(); len(list) != 0 {
		t.Fatalf("registry after failed launch = %v, want empty", list)
	}

	// The worktree PreLaunch created must not survive the rollback. Without
	// compensation this is a permanent leak: the meta is already gone, so no
	// later Delete() could ever find this id to clean it up.
	entries, err := os.ReadDir(filepath.Join(repo, ".swarm", "worktrees"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read .swarm/worktrees: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf(".swarm/worktrees still has entries after a failed launch (orphaned worktree): %v", entries)
	}
}
