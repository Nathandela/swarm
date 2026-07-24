// Epic 14 T12 — the WORKTREE launch->run->teardown COMPOSITE (carry-forward from
// Epic 12, S-3/R-3). The worktree machinery is unit-covered in isolation
// (worktree/worktree_test.go Create/Remove) and its daemon seams are covered
// (daemon/hookpoints_test.go, daemon/worktree_rollback_test.go), but nothing chains
// the whole thing through the ASSEMBLED daemon: a real launch with the worktree
// toggle, a real agent actually RUNNING inside the isolated worktree, and a real
// delete tearing it down.
//
// This test composes that end-to-end: inside a throwaway git repo, launch a fake
// session with LaunchReq.Worktree — proving the reserved OptionWorktree reaches the
// skeleton's PreLaunch hook — then assert (a) the worktree directory
// <repo>/.swarm/worktrees/<id> and branch swarm/<id> exist per git, and (b) the
// agent process's ACTUAL working directory is that worktree dir (the kernel's view,
// not a printed claim). Then delete the session and assert the worktree is torn down
// (git worktree removed + the directory gone). git is required; the test skips with a
// clear message when git is absent, per the repo convention. COST: fake agent only.
//
// Shared harness (buildBinaries, newDaemonEnv, startDaemon, dial, waitOneView,
// localOf, readMeta, alive, agentPIDOf) lives in skeleton_e2e_test.go — same package.
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

// TestE2E_Worktree_LaunchRunTeardown drives the full worktree lifecycle through the
// assembled daemon: launch-in-isolation -> agent runs inside the worktree -> delete
// tears it down.
func TestE2E_Worktree_LaunchRunTeardown(t *testing.T) {
	requireGitE2E(t)
	buildBinaries(t)
	env := newDaemonEnv(t)
	startDaemon(t, env)
	c := dial(t, env.sock)

	repo := newGitRepoE2E(t)

	// Launch WITH the worktree toggle. Cwd is the repo; the daemon's PreLaunch hook
	// must create an isolated worktree and run the agent there instead. The agent
	// prints once then idles, so it stays alive for the cwd + teardown assertions.
	id := launchWorktreeSession(t, c, repo, "print IN-WORKTREE\nidle 300s\n")
	view := waitOneView(t, c)
	if view.ID != id {
		t.Fatalf("listed id %q != launched id %q", view.ID, id)
	}
	local := localOf(t, id)

	// (a) git-visible isolation: the worktree dir and branch exist, and git registers
	// the worktree — the launch really created the Epic 12 isolation, not just a cwd.
	wantDir := filepath.Join(repo, ".swarm", "worktrees", local)
	branch := "swarm/" + local
	if fi, err := os.Stat(wantDir); err != nil || !fi.IsDir() {
		t.Fatalf("worktree dir %q not created (err=%v)", wantDir, err)
	}
	if !branchExistsE2E(t, repo, branch) {
		t.Fatalf("branch %q was not created for the worktree launch", branch)
	}
	if !worktreeListedE2E(t, repo, wantDir) {
		t.Fatalf("git worktree list does not register %q", wantDir)
	}

	// (b) the agent RAN inside the worktree: assert the actual working directory of the
	// live agent process (the kernel's view via /proc or lsof), not a self-reported
	// path. This proves the PreLaunch cwd override reached the shim's exec, closing the
	// launch->run half end-to-end.
	meta := readMeta(t, env.stateDir, local)
	if !alive(meta.ShimPID) {
		t.Fatalf("shim %d not alive; cannot check the agent's cwd", meta.ShimPID)
	}
	agentPID := agentPIDOf(t, meta.ShimPID)
	gotCwd := agentCwdE2E(t, agentPID)
	if resolve(t, gotCwd) != resolve(t, wantDir) {
		t.Fatalf("agent %d ran with cwd %q; want the worktree %q (PreLaunch cwd override "+
			"did not reach the agent's exec)", agentPID, resolve(t, gotCwd), resolve(t, wantDir))
	}

	// Teardown: deleting the session must tear the worktree down (R-3). Delete kills the
	// running agent first, then runs the PreDelete worktree teardown.
	if err := c.Delete(id); err != nil {
		t.Fatalf("Delete worktree session: %v", err)
	}
	if _, err := os.Stat(wantDir); !os.IsNotExist(err) {
		t.Fatalf("worktree dir %q still present after Delete (err=%v) — teardown did not run", wantDir, err)
	}
	if worktreeListedE2E(t, repo, wantDir) {
		t.Fatalf("git worktree list still registers %q after Delete — worktree not pruned", wantDir)
	}
}

// launchWorktreeSession launches the reserved fake agent with the worktree toggle set
// and Cwd pinned to repo, returning the namespaced session id. It mirrors
// launchFakeSession but opts into isolation (LaunchReq.Worktree) and does not use a
// throwaway Cwd (the repo is load-bearing for worktree creation).
func launchWorktreeSession(t *testing.T, c *protocol.Client, repo, script string) string {
	t.Helper()
	spath := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(spath, []byte(script), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	_ = os.Chmod(spath, 0o644) // the daemon subprocess reads it when spawning

	id, _, err := c.Launch(protocol.LaunchReq{
		Agent:    "fake",
		Cwd:      repo,
		Worktree: true,
		Options:  map[string]string{"script": spath},
		Env:      []string{"PATH=" + os.Getenv("PATH")},
		Cols:     80,
		Rows:     24,
	})
	if err != nil {
		t.Fatalf("client Launch(fake, worktree): %v", err)
	}
	return id
}

// requireGitE2E skips the whole test when git is unavailable — the worktree feature
// shells out to plain `git`, so its e2e is only meaningful with git present.
func requireGitE2E(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not found on PATH — worktree launch/teardown e2e requires git")
	}
}

// newGitRepoE2E git-inits a short-path temp repo with one commit (a worktree needs a
// HEAD to branch from) and registers cleanup. Short "/tmp/wt" prefix matches the rest
// of the suite's temp-dir convention.
func newGitRepoE2E(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wt")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	runGitE2E(t, dir, "init", "-q")
	runGitE2E(t, dir, "config", "user.email", "test@example.com")
	runGitE2E(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runGitE2E(t, dir, "add", "seed.txt")
	runGitE2E(t, dir, "commit", "-q", "-m", "seed")
	return dir
}

// runGitE2E runs git in dir and fails on a non-zero exit.
func runGitE2E(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// branchExistsE2E reports whether repo has a local branch named name.
func branchExistsE2E(t *testing.T, repo, name string) bool {
	t.Helper()
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	cmd.Dir = repo
	return cmd.Run() == nil
}

// worktreeListedE2E reports whether repo's `git worktree list` registers dir. git
// reports worktree paths resolved through symlinks (macOS /tmp -> /private/tmp), so
// both sides are resolved before comparing.
func worktreeListedE2E(t *testing.T, repo, dir string) bool {
	t.Helper()
	out := runGitE2E(t, repo, "worktree", "list", "--porcelain")
	want := resolve(t, dir)
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

// agentCwdE2E returns the ACTUAL working directory of process pid as the kernel sees
// it: the /proc/<pid>/cwd symlink on Linux, else lsof's cwd field on macOS/BSD. It
// skips (not fails) when neither mechanism is available — the same graceful
// degradation the suite applies to missing tools.
func agentCwdE2E(t *testing.T, pid int) string {
	t.Helper()
	if target, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil {
		return target
	}
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skipf("cannot read process cwd: no /proc and no lsof on PATH")
	}
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err != nil {
		t.Fatalf("lsof cwd for pid %d: %v", pid, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if path, ok := strings.CutPrefix(line, "n"); ok {
			return path
		}
	}
	t.Fatalf("lsof produced no cwd (n) field for pid %d:\n%s", pid, out)
	return ""
}

// resolve returns path with symlinks resolved, falling back to the raw path when it
// no longer exists (e.g. a torn-down worktree), so equality comparisons are robust to
// macOS's /tmp -> /private/tmp indirection.
func resolve(t *testing.T, path string) string {
	t.Helper()
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}
