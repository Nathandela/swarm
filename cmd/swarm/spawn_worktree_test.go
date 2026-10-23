package main

// FAILING-FIRST for ADR-010 Amendment 10: `swarm spawn --handoff` and `--delegate` hand
// work to another agent, and that agent gets a git worktree of its own by default when
// the target directory is a git checkout -- the improvised alternative was a checkout
// under /tmp that a reboot erased. An explicit --worktree=false keeps the old shape, and
// --prompt (and a non-git directory) are unchanged.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func newSpawnGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not found on PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "seed"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestRunSpawn_HandoffAndDelegateDefaultToAWorktreeInAGitCheckout(t *testing.T) {
	useTempHandoffRoot(t)
	inSession(t, "parent1")
	repo := newSpawnGitRepo(t)
	doc := filepath.Join(t.TempDir(), "context.md")
	if err := os.WriteFile(doc, []byte("# Goal\nship it\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"handoff", []string{"--handoff", doc}, true},
		{"delegate", []string{"--delegate", doc}, true},
		{"handoff opted out", []string{"--handoff", doc, "--worktree=false"}, false},
		{"delegate asked explicitly", []string{"--delegate", doc, "--worktree"}, true},
		{"inline prompt", []string{"--prompt", "go"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeSpawnClient()
			var stdout, stderr bytes.Buffer
			args := append([]string{"--cli", "claude", "--dir", repo}, tc.args...)
			if exit := runSpawn(args, client, &stdout, &stderr); exit != 0 {
				t.Fatalf("runSpawn %v exit %d, stderr=%q", args, exit, stderr.String())
			}
			if got := onlyLaunch(t, client).Worktree; got != tc.want {
				t.Fatalf("runSpawn %v sent Worktree=%v, want %v", args, got, tc.want)
			}
		})
	}
}

func TestRunSpawn_HandoffOutsideGitStaysInPlace(t *testing.T) {
	useTempHandoffRoot(t)
	inSession(t, "parent1")
	plain := t.TempDir()
	doc := filepath.Join(t.TempDir(), "context.md")
	if err := os.WriteFile(doc, []byte("# Goal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := newFakeSpawnClient()
	var stdout, stderr bytes.Buffer
	if exit := runSpawn([]string{"--cli", "claude", "--dir", plain, "--handoff", doc}, client, &stdout, &stderr); exit != 0 {
		t.Fatalf("runSpawn exit %d, stderr=%q", exit, stderr.String())
	}
	if req := onlyLaunch(t, client); req.Worktree || req.Cwd != plain {
		t.Fatalf("a handoff into a non-git directory sent Worktree=%v Cwd=%q, want no worktree in %q", req.Worktree, req.Cwd, plain)
	}
}
