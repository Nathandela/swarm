package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the two places the ASSEMBLY has to tell the launch
// cwd and the agent cwd apart, now that persist.Meta carries both.
//
//  1. Provider history lives under an encoding of the directory the agent RAN IN. For a
//     worktree session that is <repo>/.swarm/worktrees/<slug>, never the repo root, so the
//     resolver must ask the meta for its ProviderCwd. Until it does, resume-id recovery
//     cannot work for a worktree session at all -- it looks in a directory the provider
//     never wrote to.
//
//  2. Worktree teardown must keep anchoring on Meta.Cwd, which is the REPO. git worktree
//     remove is run with -C <repo>; run from inside the worktree it tears down nothing.
//     The new field is strictly additive and this is the test that says so.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

// newAgentCwdGitRepo git-inits a temp dir with one commit, so a worktree has a HEAD to
// branch from. It skips when git is unavailable, mirroring internal/worktree's own seam.
func newAgentCwdGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not found on PATH")
	}
	dir, err := os.MkdirTemp("", "swt")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "-q", "--allow-empty", "-m", "seed"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// TestAgentCwdWorktreeTeardownStillAnchorsOnTheRepo is the invariant guard for the whole
// change: preDeleteWorktree uses Meta.Cwd as the git repository and Meta.AgentCwd as
// the exact named worktree path. Renaming the session after launch must not change
// which checkout is removed.
func TestAgentCwdWorktreeTeardownStillAnchorsOnTheRepo(t *testing.T) {
	repo := newAgentCwdGitRepo(t)
	const id = "sess1"

	spec := daemon.LaunchSpec{Name: "Fix Login Flow", Cwd: repo, Options: map[string]string{protocol.OptionWorktree: "true"}}
	agentCwd, err := preLaunchWorktree(id, spec)
	if err != nil {
		t.Fatalf("preLaunchWorktree: %v", err)
	}
	want := filepath.Join(repo, ".swarm", "worktrees", "fix-login-flow")
	if agentCwd != want {
		t.Fatalf("preLaunchWorktree = %q, want %q", agentCwd, want)
	}

	m := persist.Meta{
		ID:            id,
		Name:          "Renamed After Launch",
		Cwd:           repo,
		AgentCwd:      agentCwd,
		LaunchOptions: map[string]string{protocol.OptionWorktree: "true"},
	}
	if err := preDeleteWorktree(m); err != nil {
		t.Fatalf("preDeleteWorktree on a meta carrying both directories: %v -- teardown must keep "+
			"using Cwd (the repo), not the new AgentCwd", err)
	}
	if _, err := os.Stat(agentCwd); !os.IsNotExist(err) {
		t.Fatalf("worktree %q still exists after preDeleteWorktree (err=%v)", agentCwd, err)
	}
}

// TestAgentCwdClaudeHistoryResolvesForAWorktreeSession: the transcript sits under the
// WORKTREE's encoded project directory, because that is where the agent ran. With the
// resolved cwd on the meta the id is recovered; without it the resolver searches the
// repo root's directory, which the provider never created.
func TestAgentCwdClaudeHistoryResolvesForAWorktreeSession(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "project")
	agentCwd := filepath.Join(repo, ".swarm", "worktrees", "sess1")
	writeClaudeHistory(t, home, legacyClaudeID, agentCwd, legacyCreatedAt.Add(time.Second), "", "")

	m := legacySource("claude", repo, legacyCreatedAt)
	m.AgentCwd = agentCwd
	got := resolveHistory(t, home, generousResumeHistoryLimits(), m)
	requireHistoryResult(t, got, resumeHistoryFound, legacyClaudeID)

	// Non-vacuity: the repo root alone cannot find it. This is the shipped bug the field
	// closes, and it is what makes the assertion above about ProviderCwd and not about
	// some path that happens to work either way.
	bare := legacySource("claude", repo, legacyCreatedAt)
	if blind := resolveHistory(t, home, generousResumeHistoryLimits(), bare); blind.Outcome == resumeHistoryFound {
		t.Fatalf("a meta carrying only the repo root resolved %q; the transcript is filed under the "+
			"worktree, so this test is not proving the resolver reads ProviderCwd", blind.ConversationID)
	}
}

// TestAgentCwdCodexHistoryResolvesForAWorktreeSession: the same for codex, whose dated
// rollout files record the agent's own cwd in their session_meta line.
func TestAgentCwdCodexHistoryResolvesForAWorktreeSession(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "project")
	agentCwd := filepath.Join(repo, ".swarm", "worktrees", "sess1")
	writeCodexHistory(t, home, legacyCodexRootID, agentCwd, legacyCreatedAt.Add(time.Second), "", "cli", "")

	m := legacySource("codex", repo, legacyCreatedAt)
	m.AgentCwd = agentCwd
	got := resolveHistory(t, home, generousResumeHistoryLimits(), m)
	requireHistoryResult(t, got, resumeHistoryFound, legacyCodexRootID)

	bare := legacySource("codex", repo, legacyCreatedAt)
	if blind := resolveHistory(t, home, generousResumeHistoryLimits(), bare); blind.Outcome == resumeHistoryFound {
		t.Fatalf("a meta carrying only the repo root resolved %q; the rollout file records the "+
			"worktree cwd, so this test is not proving the resolver reads ProviderCwd", blind.ConversationID)
	}
}
