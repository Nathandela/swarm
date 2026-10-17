package skeleton

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/worktree"
)

func TestDeleteDiscussionRetainsUnrelatedHistory(t *testing.T) {
	src := endedCodexSource("old", nil)
	src.RosterHidden = true
	current := src
	current.ID, current.ResumedFrom, current.RosterHidden = "current", src.ID, false
	current.CreatedAt = time.Now()
	independent := src
	independent.ID, independent.RosterHidden = "independent", false
	handoff := independent
	handoff.ID, handoff.SpawnedFrom, handoff.SpawnIntent = "handoff", src.ID, "handoff"
	api := continuityAPI(t, src, current, independent, handoff)
	if err := api.Delete(current.ID); err != nil {
		t.Fatal(err)
	}
	for _, m := range []persist.Meta{src, current, independent, handoff} {
		_, err := os.Stat(filepath.Join(api.stateDir, m.ID))
		if keep := m.ID == independent.ID || m.ID == handoff.ID; keep != (err == nil) {
			t.Fatalf("unexpected retention of %s: %v", m.ID, err)
		}
	}
}

func TestDeleteDiscussionRefusesOtherLiveAttemptBeforeRemovingFiles(t *testing.T) {
	src := endedCodexSource("old", nil)
	buildBinaries(t)
	api := continuityAPIWithConfig(t, func(cfg *daemon.Config) { cfg.ShimBinary = swarmBin }, src)
	current := launchDeleteTestAttempt(t, api, src, t.TempDir())
	live := launchDeleteTestAttempt(t, api, src, t.TempDir())
	if err := api.Delete(current.ID); err == nil || !strings.Contains(err.Error(), "running attempt") {
		t.Fatal("deleted files underneath another running attempt")
	}
	for _, m := range []persist.Meta{src, current, live} {
		if _, err := os.Stat(filepath.Join(api.stateDir, m.ID, "meta.json")); err != nil {
			t.Fatalf("refused deletion changed %s: %v", m.ID, err)
		}
	}
}

func TestDeleteDiscussionStopsBeforeRemovingRunningAttemptsWorkspace(t *testing.T) {
	root := endedCodexSource("root", map[string]string{protocol.OptionWorktree: "true"})
	root.AgentCwd = t.TempDir()
	cleaned := false
	buildBinaries(t)
	api := continuityAPIWithConfig(t, func(cfg *daemon.Config) {
		cfg.ShimBinary = swarmBin
		cfg.PreDelete = func(persist.Meta) error { cleaned = true; return nil }
	}, root)
	current := launchDeleteTestAttempt(t, api, root, root.AgentCwd)
	if err := api.Delete(current.ID); err == nil {
		t.Fatal("deletion accepted while the resumed attempt still runs")
	}
	if cleaned || len(api.List()) != 2 {
		t.Fatal("removed an ancestor workspace before its resumed attempt stopped")
	}
}

func launchDeleteTestAttempt(t *testing.T, api *coreAPI, source persist.Meta, cwd string) persist.Meta {
	t.Helper()
	script := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(script, []byte("idle 60s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := api.core.Launch(daemon.LaunchSpec{
		AgentType: source.AgentType, Argv: []string{fakeAgentBin, script}, Cwd: cwd,
		ConversationID: source.ConversationID, ResumedFrom: source.ID, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := api.core.Delete(m.ID); err != nil {
			t.Errorf("test process cleanup: %v", err)
		}
	})
	if m.Status.Process != status.ProcessRunning {
		t.Fatal("fixture did not launch a running attempt")
	}
	return m
}

func TestDeleteDiscussionRetryKeepsBranchedFamilyReachable(t *testing.T) {
	root := endedCodexSource("root", nil)
	bridge, branch, current := root, root, root
	bridge.ID, bridge.ResumedFrom = "bridge", root.ID
	branch.ID, branch.ResumedFrom = "branch", root.ID
	current.ID, current.ResumedFrom, current.CreatedAt = "current", bridge.ID, time.Now()
	failed := true
	api := continuityAPIWithConfig(t, func(cfg *daemon.Config) {
		cfg.PreDelete = func(m persist.Meta) error {
			if failed && m.ID == root.ID {
				return errors.New("cleanup unavailable")
			}
			return nil
		}
	}, root, bridge, branch, current)
	if err := api.Delete(current.ID); err == nil {
		t.Fatal("cleanup failure reported success")
	}
	if _, err := os.Stat(filepath.Join(api.stateDir, branch.ID)); !os.IsNotExist(err) {
		t.Fatalf("test did not exercise partial deletion: %v", err)
	}
	store, err := persist.NewStore(api.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := store.Scan()
	if err != nil || len(persist.DiscussionAttempts(remaining, current.ID)) != 3 {
		t.Fatalf("retry lost the remaining family on disk: %+v, %v", remaining, err)
	}
	failed = false
	if err := api.Delete(current.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err = store.Scan()
	if err != nil || len(remaining) != 0 {
		t.Fatalf("retry retained attempts: %+v, %v", remaining, err)
	}
}

func TestDeleteDiscussionRefusesSharedWorkspaceAliases(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(map[bool]string{false: "subdirectory", true: "symlink"}[alias], func(t *testing.T) {
			root := endedCodexSource("root", map[string]string{protocol.OptionWorktree: "true"})
			root.AgentCwd = t.TempDir()
			sub := filepath.Join(root.AgentCwd, "src")
			if err := os.Mkdir(sub, 0700); err != nil {
				t.Fatal(err)
			}
			if alias {
				link := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(sub, link); err != nil {
					t.Fatal(err)
				}
				sub = link
			}
			current := endedCodexSource("current", nil)
			current.ResumedFrom, current.CreatedAt = root.ID, time.Now()
			outsider := endedCodexSource("outsider", nil)
			outsider.Cwd = sub
			api := continuityAPI(t, root, current, outsider)
			if err := api.Delete(current.ID); err == nil {
				t.Fatal("deleted a checkout another discussion still uses")
			}
			if len(api.List()) != 3 {
				t.Fatal("shared-workspace refusal partially deleted history")
			}
		})
	}
}

func TestDeleteDiscussionRemovesOwnedWorktree(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "seed"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	root := endedCodexSource("root", map[string]string{protocol.OptionWorktree: "true"})
	root.Cwd = repo
	var err error
	root.AgentCwd, err = worktree.Create(repo, root.ID, "cleanup")
	if err != nil {
		t.Fatal(err)
	}
	current := endedCodexSource("current", nil)
	current.Cwd, current.ResumedFrom, current.CreatedAt = root.AgentCwd, root.ID, time.Now()
	api := continuityAPIWithConfig(t, func(cfg *daemon.Config) { cfg.PreDelete = preDeleteWorktree }, root, current)
	if err := api.Delete(current.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root.AgentCwd); !os.IsNotExist(err) {
		t.Fatalf("owned checkout retained: %v", err)
	}
	if err := preDeleteWorktree(root); err != nil {
		t.Fatalf("completed checkout cleanup was not retryable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("original project removed: %v", err)
	}
}

func TestDeleteDiscussionExcludesFreshLaunchDuringCleanup(t *testing.T) {
	source := endedCodexSource("source", nil)
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	api := continuityAPIWithConfig(t, func(cfg *daemon.Config) {
		cfg.PreDelete = func(persist.Meta) error { close(entered); <-release; return nil }
	}, source)
	deleted := make(chan error, 1)
	go func() { deleted <- api.Delete(source.ID) }()
	<-entered
	started, launched := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := api.launchResolved(daemon.LaunchSpec{})
		launched <- err
	}()
	<-started
	select {
	case <-launched:
		t.Fatal("a fresh launch entered while workspace cleanup was in progress")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	released = true
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	select {
	case <-launched:
	case <-time.After(time.Second):
		t.Fatal("fresh launch stayed blocked after cleanup")
	}
}

func TestCleanupContainsCaseAliases(t *testing.T) {
	root := filepath.Join(t.TempDir(), "WorkspaceCase")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(root), "workspacecase")
	if _, err := os.Stat(alias); err != nil {
		t.Skip("case-sensitive filesystem")
	}
	if !cleanupContains(root, filepath.Join(alias, "missing-subdirectory")) {
		t.Fatal("shared-workspace guard missed the filesystem case alias")
	}
}

func TestDeleteDiscussionRefusesUnregisteredStatePayload(t *testing.T) {
	for _, id := range []string{"accounts", "journal", "remote", "idempotency", "upgrade", "orphan-attempt"} {
		t.Run(id, func(t *testing.T) {
			api := continuityAPI(t)
			dir := filepath.Join(api.stateDir, id)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(dir, "retained")
			if err := os.WriteFile(payload, []byte("shared custody"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := api.core.JournalReadFrom(0)
			if err != nil {
				t.Fatal(err)
			}
			if err := api.Delete(id); err == nil {
				t.Fatal("delete accepted an existing unregistered state directory")
			}
			if raw, err := os.ReadFile(payload); err != nil || string(raw) != "shared custody" {
				t.Fatalf("refusal changed unregistered payload: %q, %v", raw, err)
			}
			after, err := api.core.JournalReadFrom(0)
			if err != nil || after.Cursor != before.Cursor {
				t.Fatalf("refusal appended a deletion event: %d -> %d, %v", before.Cursor, after.Cursor, err)
			}
		})
	}
}

func TestDeleteDiscussionCompletedRetryDoesNotTouchReusedPath(t *testing.T) {
	source := endedCodexSource("source", nil)
	api := continuityAPI(t, source)
	if err := api.Delete(source.ID); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(source.ID); err != nil {
		t.Fatalf("completed deletion was not idempotent: %v", err)
	}
	if err := api.core.Close(); err != nil {
		t.Fatal(err)
	}
	core, err := daemon.Open(daemon.Config{StateDir: api.stateDir, SocketPath: filepath.Join(api.stateDir, "d.sock"), LockPath: filepath.Join(api.stateDir, "d.lock")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	api.core = core
	if err := api.Delete(source.ID); err != nil {
		t.Fatalf("completed deletion was not idempotent after restart: %v", err)
	}
	if err := os.Mkdir(filepath.Join(api.stateDir, source.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(api.stateDir, source.ID, "new-owner"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(source.ID); err == nil {
		t.Fatal("old delete retry accepted a newly unregistered payload")
	}
}

func TestDeleteDiscussionRefusesPhysicalSymlinkParentWorkspace(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "missing-descendant"}[missing], func(t *testing.T) {
			root := endedCodexSource("root", map[string]string{protocol.OptionWorktree: "true"})
			root.AgentCwd = t.TempDir()
			nested := filepath.Join(root.AgentCwd, "nested")
			if err := os.Mkdir(nested, 0700); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(nested, alias); err != nil {
				t.Fatal(err)
			}
			outsider := endedCodexSource("outsider", nil)
			outsider.Cwd = alias + "/.."
			if missing {
				outsider.Cwd += "/missing/nested"
			}
			cleaned := false
			api := continuityAPIWithConfig(t, func(cfg *daemon.Config) {
				cfg.PreDelete = func(persist.Meta) error { cleaned = true; return nil }
			}, root, outsider)
			if err := api.Delete(root.ID); err == nil {
				t.Fatal("deleted a checkout reached physically through symlink/..")
			}
			if cleaned || len(api.List()) != 2 {
				t.Fatal("physical workspace refusal entered cleanup")
			}
		})
	}
}

func TestCanonicalCleanupPathKeepsPhysicalParentAndMissingSuffix(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(nested, alias); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(wd, alias)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{alias + "/..", relative + "/.."} {
		for _, suffix := range []string{"", "/missing/nested"} {
			got, err := canonicalCleanupPath(path + suffix)
			if err != nil || got != want+suffix {
				t.Fatalf("physical cleanup path = %q, %v; want %q", got, err, want+suffix)
			}
		}
	}
}

func TestDeleteDiscussionRemovesHiddenOnlyFamilyAndRetries(t *testing.T) {
	root := endedCodexSource("root", nil)
	root.RosterHidden = true
	bridge, branch, selected := root, root, root
	bridge.ID, bridge.ResumedFrom = "bridge", root.ID
	branch.ID, branch.ResumedFrom = "branch", root.ID
	selected.ID, selected.ResumedFrom = "selected", bridge.ID
	fail := true
	api := continuityAPIWithConfig(t, func(cfg *daemon.Config) {
		cfg.PreDelete = func(m persist.Meta) error {
			if fail && m.ID == root.ID {
				return errors.New("cleanup unavailable")
			}
			return nil
		}
	}, root, bridge, branch, selected)
	if err := api.Delete(selected.ID); err == nil {
		t.Fatal("fixture failed to exercise cleanup failure")
	}
	if _, ok := api.core.Get(branch.ID); ok {
		t.Fatal("hidden-only family was not admitted to normal cleanup")
	}
	if got := persist.DiscussionAttempts(api.List(), selected.ID); len(got) != 3 {
		t.Fatalf("partial cleanup lost hidden-only family connectivity: %+v", got)
	}
	fail = false
	if err := api.Delete(selected.ID); err != nil {
		t.Fatal(err)
	}
	for _, m := range []persist.Meta{root, bridge, branch, selected} {
		if _, err := os.Stat(filepath.Join(api.stateDir, m.ID)); !os.IsNotExist(err) {
			t.Fatalf("hidden-only attempt retained after deletion: %v", err)
		}
	}
	if err := api.Delete(selected.ID); err != nil {
		t.Fatalf("hidden-only completed retry failed: %v", err)
	}
}

func TestDeleteDiscussionHiddenPredecessorStillRequiresCurrent(t *testing.T) {
	root := endedCodexSource("root", nil)
	root.RosterHidden = true
	current := endedCodexSource("current", nil)
	current.ResumedFrom = root.ID
	api := continuityAPI(t, root, current)
	if err := api.Delete(root.ID); err == nil || len(api.List()) != 2 {
		t.Fatal("hidden predecessor bypassed the visible current discussion")
	}
}
