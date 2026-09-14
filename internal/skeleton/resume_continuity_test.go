package skeleton

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestResumeContinuity(t *testing.T) {
	source := endedCodexSource("source", map[string]string{protocol.OptionWorktree: "true"})
	source.Cwd = "/repo"
	source.AgentCwd = t.TempDir()
	source.Name, source.Tag = "Research", "WIP"
	source.SpawnedFrom, source.SpawnIntent, source.Supervision = "parent", "handoff", "passive"
	req := daemon.LaunchSpec{AgentType: "codex", Cwd: "/wrong-checkout", Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, source.ID)}}
	got, err := composeLaunchSpec(req, testEndpoint, "", srcGetter(source.ID, source), stubLookPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cwd != source.AgentCwd || got.Tag != "WIP" || got.Name != "Research" || got.SpawnedFrom != "parent" || got.Supervision != "passive" {
		t.Fatalf("lost source context: %+v", got)
	}
	if got.Options[protocol.OptionWorktree] != "" {
		t.Fatal("resume allocates a new checkout")
	}
	source.AgentCwd = filepath.Join(source.AgentCwd, "missing")
	if _, err := composeLaunchSpec(req, testEndpoint, "", srcGetter(source.ID, source), stubLookPath); err == nil {
		t.Fatal("missing checkout accepted")
	}
	source.AgentCwd = ""
	if _, err := composeLaunchSpec(req, testEndpoint, "", srcGetter(source.ID, source), stubLookPath); err == nil {
		t.Fatal("uncaptured worktree accepted")
	}
}

func TestResumeDeduplicatesConversationAcrossParents(t *testing.T) {
	src := endedCodexSource("old-parent", nil)
	live := persist.Meta{ID: "grandchild", AgentType: "codex", ConversationID: src.ConversationID, ResumedFrom: "other-parent", Status: status.Status{Process: status.ProcessRunning}}
	unrelated := live
	unrelated.ID = "unrelated"
	unrelated.ConversationID = "different"
	unrelated.CreatedAt = time.Now()
	otherProvider := live
	otherProvider.ID = "claude"
	otherProvider.AgentType = "claude"
	ended := live
	ended.ID = "failed"
	ended.Status.Process = status.ProcessExited
	parent := src
	parent.ID, parent.ResumedFrom = "other-parent", src.ID
	got, ok := runningConversation([]persist.Meta{src, parent, unrelated, otherProvider, ended, live}, src)
	if !ok || got.ID != live.ID {
		t.Fatalf("existing live thread missed: %+v %v", got, ok)
	}
	src.ConversationID = ""
	if _, ok := runningConversation([]persist.Meta{live}, src); ok {
		t.Fatal("unknown identities merged")
	}
}

func TestResumeUsesSavedEnvironmentBeforeResolvingBinary(t *testing.T) {
	source := endedCodexSource("original", nil)
	source.Cwd = t.TempDir()
	source.Env = []string{"PATH=/saved/provider/path", "HOME=/saved/home"}
	api := continuityAPI(t, source)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	req := daemon.LaunchSpec{AgentType: "codex", Cwd: source.Cwd, ClientEnv: []string{"PATH=" + bin}, Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, source.ID)}}
	if _, err := api.Launch(req); err == nil {
		t.Fatal("client environment overrode saved provider environment")
	}
	if len(api.List()) != 1 {
		t.Fatal("resume launched with another terminal's environment")
	}
}

func continuityAPI(t *testing.T, metas ...persist.Meta) *coreAPI {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sw-cont-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	store, err := persist.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range metas {
		if err = store.Save(m); err != nil {
			t.Fatal(err)
		}
	}
	core, err := daemon.Open(daemon.Config{StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"), LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	return &coreAPI{core: core, endpointID: testEndpoint}
}

func TestDeleteDiscussionArchivesPriorAttempts(t *testing.T) {
	src := endedCodexSource("original", nil)
	src.CreatedAt = time.Unix(1, 0)
	child := src
	child.ID = "replacement"
	child.ResumedFrom = src.ID
	child.CreatedAt = time.Unix(2, 0)
	api := continuityAPI(t, src, child)
	if err := api.Delete(child.ID); err != nil {
		t.Fatal(err)
	}
	history, ok := api.core.Get(src.ID)
	if !ok || !history.RosterHidden {
		t.Fatal("history lost or not archived")
	}
	if _, ok := api.core.Get(child.ID); ok {
		t.Fatal("explicit deletion did not delete selected attempt")
	}
	visible, _ := persist.ProjectDiscussions(api.List())
	if len(visible) != 0 {
		t.Fatal("deleted discussion reappeared")
	}
}

func TestDeleteWorktreeOwnerRefusesSharedCheckout(t *testing.T) {
	src := endedCodexSource("original", map[string]string{protocol.OptionWorktree: "true"})
	src.Cwd = "/repo"
	src.AgentCwd = t.TempDir()
	child := endedCodexSource("replacement", nil)
	child.Cwd = src.AgentCwd
	child.ResumedFrom = src.ID
	api := continuityAPI(t, src, child)
	if err := api.Delete(src.ID); err == nil {
		t.Fatal("deleting shared checkout owner was allowed")
	}
	if _, ok := api.core.Get(src.ID); !ok {
		t.Fatal("refused deletion erased source")
	}
	if _, err := os.Stat(src.AgentCwd); err != nil {
		t.Fatal("refused deletion removed checkout")
	}
}

func TestAuthRecoveryRequiresCurrentSubscribedFeed(t *testing.T) {
	d := &Daemon{}
	d.capStore.instances = map[string]string{"candidate": "instance"}
	m := persist.Meta{ID: "candidate", ConversationID: "thread", Status: status.Status{Process: status.ProcessRunning}}
	b := &sessionBackend{sessionInstance: "instance", threadID: "thread", feed: &backendFeed{}}
	d.backend.live = map[string]*sessionBackend{m.ID: b}
	if d.authRecoveryReady(m) {
		t.Fatal("unsubscribed backend counted as resumed")
	}
	b.subscribed = true
	if !d.authRecoveryReady(m) {
		t.Fatal("current subscribed feed not recognized")
	}
	b.threadID = "other"
	if d.authRecoveryReady(m) {
		t.Fatal("foreign thread accepted")
	}
	b.threadID = "thread"
	b.sessionInstance = "old"
	if d.authRecoveryReady(m) {
		t.Fatal("stale process accepted")
	}
	b.sessionInstance = "instance"
	b.feed.retired.Store(true)
	if d.authRecoveryReady(m) {
		t.Fatal("retired feed accepted")
	}
}

func TestCodexResumeCannotSilentlyDiscardPermissionChange(t *testing.T) {
	src := endedCodexSource("original", map[string]string{"sandbox": "workspace-write"})
	req := daemon.LaunchSpec{AgentType: "codex", Cwd: "/work", Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, src.ID), "sandbox": "read-only"}}
	if _, err := composeLaunchSpec(req, testEndpoint, "", srcGetter(src.ID, src), stubLookPath); err == nil {
		t.Fatal("explicit permission narrowing would be silently dropped")
	}
	req.Options = map[string]string{protocol.OptionResumeConversationID: src.ConversationID, "sandbox": "read-only"}
	if _, err := composeLaunchSpec(req, testEndpoint, "", srcGetter(src.ID, src), stubLookPath); err == nil {
		t.Fatal("external permission override without saved policy accepted")
	}
}

func TestArchivedSourceCannotBeAutomaticallyResurrectedAtLaunch(t *testing.T) {
	source := endedCodexSource("archived", nil)
	source.Cwd = t.TempDir()
	source.RosterHidden = true
	api := continuityAPI(t, source)
	req := daemon.LaunchSpec{AgentType: "codex", Cwd: source.Cwd, Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, source.ID)}}
	if _, err := api.Launch(req); err == nil {
		t.Fatal("archived source accepted at launch boundary")
	}
	if len(api.List()) != 1 {
		t.Fatal("archived discussion was resurrected")
	}
}
