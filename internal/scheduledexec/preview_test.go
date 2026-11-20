package scheduledexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/schedule"
)

func TestPreviewWorkspaceMatchesLaunchConstraintsWithoutSideEffects(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(base, "swarm-state")
	repo := filepath.Join(base, "work")
	for _, path := range []string{state, repo} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s, %v", args, out, err)
		}
	}
	free := func(string) (string, error) { return "", nil }
	busy := func(path string) (string, error) {
		if path == repo {
			return filepath.Join(repo, "active"), nil
		}
		return "", nil
	}
	spec := schedule.JobSpec{Directory: repo, Workspace: schedule.WorkspaceDirectory}
	check := func(live func(string) (string, error), runs []schedule.Occurrence, wantWarning, wantError string) {
		t.Helper()
		warn, err := PreviewWorkspace(state, spec, runs, live)
		if (wantWarning == "" && warn != "") || !strings.Contains(warn, wantWarning) || (wantError == "" && err != nil) || (wantError != "" && (err == nil || !strings.Contains(err.Error(), wantError))) {
			t.Fatalf("preview (%+v): warning=%q, err=%v; wanted %q, %q", spec, warn, err, wantWarning, wantError)
		}
	}
	spec.Directory = base
	check(free, nil, "", "overlaps Swarm state")
	spec.Directory = repo
	check(busy, nil, "", filepath.Join(repo, "active"))
	check(free, []schedule.Occurrence{{Workspace: repo, State: schedule.OccurrenceReady}}, "", repo)
	check(free, []schedule.Occurrence{{Workspace: repo, State: schedule.OccurrenceReady, ReleasedAt: time.Now()}}, "", "")
	check(free, []schedule.Occurrence{{State: schedule.OccurrenceQueued, Spec: spec}}, "", "scheduled directory")
	spec.AllowOverlap = true
	git("init", "-q")
	check(free, nil, "with a commit", "")
	check(busy, nil, "", "with a commit")
	git("commit", "-q", "--allow-empty", "-m", "initial")
	check(busy, nil, "will use an isolated Git worktree", "")
	spec.Workspace = schedule.WorkspaceWorktree
	untracked := filepath.Join(repo, "untracked")
	if err := os.Mkdir(untracked, 0700); err != nil {
		t.Fatal(err)
	}
	spec.Directory = untracked
	check(free, nil, "", "absent from the committed checkout")
	spec.Directory = repo
	check(free, nil, "", "")
	if _, err := os.Stat(scheduledWorktreeRoot(state)); !os.IsNotExist(err) {
		t.Fatalf("preview created a worktree root: %v", err)
	}
}
