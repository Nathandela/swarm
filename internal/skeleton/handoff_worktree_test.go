package skeleton

// FAILING-FIRST for ADR-010 Amendment 10: an agent handoff's child runs in a git
// worktree of its own, never in a temporary directory. A first-class `swarm handoff`
// whose source checkout is a git work tree launches with worktree isolation, based on
// that checkout's HEAD; a source outside git continues where it was, as before; and any
// agent handoff or delegation whose worktree would land under the temporary directory is
// refused by name, because a reboot erases it with the successor's work.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// handoffWorktreeFixture is handoffDirFixture with the production worktree hooks wired
// in, so a launch that asks for isolation really creates (and a rollback really removes)
// the checkout.
func handoffWorktreeFixture(t *testing.T, metas ...persist.Meta) (*coreAPI, string) {
	t.Helper()
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "sw-handoffwt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	store, err := persist.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range metas {
		if err := store.Save(m); err != nil {
			t.Fatal(err)
		}
	}
	core, err := daemon.Open(daemon.Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "d.sock"),
		LockPath:    filepath.Join(dir, "d.lock"),
		LogPath:     filepath.Join(dir, "d.log"),
		ShimBinary:  swarmBin,
		MaxSessions: 16,
		PreLaunch:   preLaunchWorktree,
		PreDelete:   preDeleteWorktree,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	api := newCoreAPI(core, fakeAgentBin, endpointID(dir))
	t.Cleanup(api.close)
	return api, dir
}

// gitOutput runs git in dir and returns its trimmed output.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// withTemporaryRoots replaces the directories handoffs treat as temporary for one test.
// The fixtures' repositories live under the OS temp dir like every other test fixture;
// pointing the roots elsewhere is what lets a test stand for a real, durable checkout.
func withTemporaryRoots(t *testing.T, roots ...string) {
	t.Helper()
	old := handoffTemporaryRoots
	handoffTemporaryRoots = func() []string { return roots }
	t.Cleanup(func() { handoffTemporaryRoots = old })
}

// runningGitSource is a live source session working in a git checkout.
func runningGitSource(t *testing.T, id string) (persist.Meta, string) {
	t.Helper()
	repo := newAgentCwdGitRepo(t)
	src := endedCodexSource(id, nil)
	src.Cwd = repo
	src.Status.Process = status.ProcessRunning
	return src, repo
}

func TestHandoffChildFromAGitCheckoutRunsInItsOwnWorktree(t *testing.T) {
	withTemporaryRoots(t, t.TempDir())
	src, repo := runningGitSource(t, "git-src")
	api, dir := handoffWorktreeFixture(t, src)

	child, err := api.Launch(handoffChildSpec(t, src, repo))
	if err != nil {
		t.Fatalf("handoff child launch from a git checkout refused: %v", err)
	}
	t.Cleanup(func() { killSpawnedShim(t, child) })

	if child.LaunchOptions[protocol.OptionWorktree] != "true" {
		t.Fatalf("handoff child launch options = %v, want worktree isolation", child.LaunchOptions)
	}
	got := spawnBinding(t, dir, child.ID)
	if filepath.Dir(got) != filepath.Join(repo, ".swarm", "worktrees") {
		t.Fatalf("handoff child's agent runs in %q, want a worktree under %q", got, filepath.Join(repo, ".swarm", "worktrees"))
	}
	if child.AgentCwd != got {
		t.Fatalf("child AgentCwd = %q, want the worktree %q it runs in", child.AgentCwd, got)
	}
	if child.Cwd != repo {
		t.Fatalf("child Cwd = %q, want the source checkout %q that anchors the worktree", child.Cwd, repo)
	}
	if head, base := gitOutput(t, got, "rev-parse", "HEAD"), gitOutput(t, repo, "rev-parse", "HEAD"); head != base {
		t.Fatalf("the worktree starts at %s, want the source checkout's HEAD %s", head, base)
	}
}

func TestHandoffChildOutsideGitContinuesInTheSourceDirectory(t *testing.T) {
	withTemporaryRoots(t, t.TempDir())
	plain := t.TempDir()
	src := endedCodexSource("plain-src", nil)
	src.Cwd = plain
	src.Status.Process = status.ProcessRunning
	api, dir := handoffWorktreeFixture(t, src)

	child, err := api.Launch(handoffChildSpec(t, src, plain))
	if err != nil {
		t.Fatalf("handoff child launch from a non-git directory refused: %v", err)
	}
	t.Cleanup(func() { killSpawnedShim(t, child) })

	if got := spawnBinding(t, dir, child.ID); got != plain {
		t.Fatalf("handoff child's agent runs in %q, want the source directory %q (nothing to make a worktree of)", got, plain)
	}
	if child.LaunchOptions[protocol.OptionWorktree] != "" {
		t.Fatalf("a non-git handoff child was flagged for worktree isolation: %v", child.LaunchOptions)
	}
}

func TestHandoffChildRefusesAWorktreeUnderTheTemporaryDirectory(t *testing.T) {
	src, repo := runningGitSource(t, "tmp-src")
	withTemporaryRoots(t, filepath.Dir(repo))
	api, _ := handoffWorktreeFixture(t, src)

	child, err := api.Launch(handoffChildSpec(t, src, repo))
	if err == nil {
		t.Cleanup(func() { killSpawnedShim(t, child) })
		t.Fatalf("a handoff child was launched with its worktree under the temporary directory: %+v", child)
	}
	if !strings.Contains(err.Error(), "handoff:") || !strings.Contains(err.Error(), repo) || !strings.Contains(err.Error(), "temporary") {
		t.Fatalf("refusal = %q, want a handoff refusal naming %q and the temporary directory", err, repo)
	}
	if got := len(api.List()); got != 1 {
		t.Fatalf("after the refusal the roster holds %d rows, want the source only", got)
	}
	if _, err := os.Stat(filepath.Join(repo, ".swarm")); !os.IsNotExist(err) {
		t.Fatalf("the refused handoff still created %s (err=%v)", filepath.Join(repo, ".swarm"), err)
	}
}

// A `swarm spawn --delegate` child asks for its worktree on the wire; the same rule
// keeps that worktree out of the temporary directory.
func TestDelegatedWorktreeUnderTheTemporaryDirectoryIsRefused(t *testing.T) {
	src, repo := runningGitSource(t, "deleg-src")
	withTemporaryRoots(t, filepath.Dir(repo))
	api, _ := handoffWorktreeFixture(t, src)

	spec := handoffChildSpec(t, src, repo)
	spec.SpawnIntent, spec.Supervision = protocol.SpawnIntentDelegate, ""
	spec.Options = map[string]string{protocol.OptionWorktree: "true"}
	child, err := api.Launch(spec)
	if err == nil {
		t.Cleanup(func() { killSpawnedShim(t, child) })
		t.Fatalf("a delegated child was launched with its worktree under the temporary directory: %+v", child)
	}
	if !strings.Contains(err.Error(), repo) || !strings.Contains(err.Error(), "temporary") {
		t.Fatalf("refusal = %q, want one naming %q and the temporary directory", err, repo)
	}
	if _, err := os.Stat(filepath.Join(repo, ".swarm")); !os.IsNotExist(err) {
		t.Fatalf("the refused delegation still created %s (err=%v)", filepath.Join(repo, ".swarm"), err)
	}
}

// Isolation is for agent handoffs. An ordinary launch outside any lineage -- a human's
// `n` in the TUI -- keeps running exactly where it was asked to.
func TestOrdinaryLaunchInAGitCheckoutIsNotIsolated(t *testing.T) {
	withTemporaryRoots(t, t.TempDir())
	repo := newAgentCwdGitRepo(t)
	api, dir := handoffWorktreeFixture(t)

	m, err := api.Launch(daemon.LaunchSpec{
		AgentType: "fake",
		Argv:      []string{fakeAgentBin, mustScript(t, "print plain\nidle 60s\n")},
		Cwd:       repo,
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      80,
		Rows:      24,
	})
	if err != nil {
		t.Fatalf("ordinary launch refused: %v", err)
	}
	t.Cleanup(func() { killSpawnedShim(t, m) })
	if got := spawnBinding(t, dir, m.ID); got != repo {
		t.Fatalf("an ordinary launch runs in %q, want %q", got, repo)
	}
}
