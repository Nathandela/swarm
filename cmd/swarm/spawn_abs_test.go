package main

// Review follow-ups on the Phase 2 spawn verb (bead agents-tracker-zqsk): the daemon
// is long-lived and resolves nothing against the CALLER's cwd, so every path the verb
// puts on the wire or into a prompt must already be absolute when it leaves the
// process; and a Launch refusal must not strand the handoff copy it just made.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunSpawn_RelativeDirResolvesAbsolute pins that a relative --dir is resolved
// against the caller's cwd before it reaches the LaunchReq: the daemon would
// otherwise stat it against ITS OWN cwd, launching the child in the wrong place or
// refusing a perfectly real directory.
func TestRunSpawn_RelativeDirResolvesAbsolute(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)

	c := newFakeSpawnClient()
	if code := runSpawn([]string{"--cli", "claude", "--dir", "child", "--prompt", "go"}, c, io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	req := onlyLaunch(t, c)
	if !filepath.IsAbs(req.Cwd) {
		t.Fatalf("Cwd = %q, want absolute", req.Cwd)
	}
	want := filepath.Join(base, "child")
	if resolved, _ := filepath.EvalSymlinks(req.Cwd); resolved != want {
		if req.Cwd != want {
			t.Errorf("Cwd = %q, want %q", req.Cwd, want)
		}
	}
}

// TestRunSpawn_HandoffPointerIsAbsolute pins that the pointer prompt names an
// absolute destination even when the state-dir resolver yields a relative path
// (SWARM_DAEMON_STATE is a supported knob): the child starts in its own cwd and
// would otherwise be pointed at a file it cannot resolve.
func TestRunSpawn_HandoffPointerIsAbsolute(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	prev := spawnStateDir
	spawnStateDir = func() (string, error) { return "state", nil }
	t.Cleanup(func() { spawnStateDir = prev })

	src := filepath.Join(base, "plan.md")
	if err := os.WriteFile(src, []byte("do the thing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := newFakeSpawnClient()
	if code := runSpawn([]string{"--cli", "claude", "--handoff", src, "--prompt", ""}, c, io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	req := onlyLaunch(t, c)
	const prefix = "Read and follow the instructions in "
	path := strings.TrimSuffix(strings.TrimPrefix(req.InitialPrompt, prefix), ".")
	if !strings.HasPrefix(req.InitialPrompt, prefix) || !filepath.IsAbs(path) {
		t.Fatalf("InitialPrompt = %q, want an absolute pointer path", req.InitialPrompt)
	}
}

// TestRunSpawn_LaunchErrorRemovesHandoffCopy pins that a refused launch does not
// strand its handoff copy: retries against a full daemon must not accumulate
// orphaned documents no session will ever reference.
func TestRunSpawn_LaunchErrorRemovesHandoffCopy(t *testing.T) {
	stateDir := useTempStateDir(t)

	src := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(src, []byte("do the thing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := newFakeSpawnClient()
	c.err = errFakeDaemon
	if code := runSpawn([]string{"--cli", "claude", "--handoff", src}, c, io.Discard, io.Discard); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	left, err := os.ReadDir(filepath.Join(stateDir, "handoffs"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("handoffs dir holds %d orphaned copies after a refused launch, want 0", len(left))
	}
}
