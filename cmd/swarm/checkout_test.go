package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/skeleton"
	"github.com/Nathandela/swarm/internal/status"
)

func inventoryTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func inventoryGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if result, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, result)
	}
}

func inventoryRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := inventoryTempDir(t)
	inventoryGit(t, repo, "init", "-q")
	inventoryGit(t, repo, "-c", "user.email=fixture@example.com", "-c", "user.name=fixture", "commit", "-qm", "fixture", "--allow-empty")
	return repo
}

func inventoryState(t *testing.T) (*persist.Store, string) {
	t.Helper()
	root := inventoryTempDir(t)
	t.Setenv(daemon.EnvStateDir, root)
	t.Setenv(daemon.EnvSocket, filepath.Join(root, "daemon.sock"))
	t.Setenv("XDG_CONFIG_HOME", inventoryTempDir(t))
	t.Setenv("HOME", inventoryTempDir(t))
	store, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "idempotency"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "idempotency", "idempotency.log"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	return store, root
}

func readCheckoutInventory(t *testing.T, args ...string) (skeleton.CheckoutInventory, int) {
	t.Helper()
	var out, stderr bytes.Buffer
	code := dispatch(append([]string{"checkout", "inventory", "--json"}, args...), &out, &stderr)
	var result skeleton.CheckoutInventory
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("code=%d stderr=%s stdout=%s error=%v", code, stderr.String(), out.String(), err)
	}
	return result, code
}

func inventoryPath(t *testing.T, report skeleton.CheckoutInventory, target string) skeleton.CheckoutPath {
	t.Helper()
	for _, path := range report.Paths {
		if path.Path == target {
			return path
		}
	}
	t.Fatalf("path %q missing in inventory: %+v", target, report.Paths)
	return skeleton.CheckoutPath{}
}

func TestCheckoutInventoryDiscoversLiveDescendantAndHiddenResume(t *testing.T) {
	repo := inventoryRepo(t)
	store, root := inventoryState(t)
	worktree := filepath.Join(inventoryTempDir(t), "linked")
	inventoryGit(t, repo, "worktree", "add", "-qb", "independent", worktree)
	ext := inventoryTempDir(t)
	// This test process stands in for the live shim whose cwd the inventory inspects.
	t.Chdir(ext)
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []persist.Meta{
		{ID: "old", AgentType: "codex", Cwd: repo, AgentCwd: worktree, LaunchOptions: map[string]string{protocol.OptionWorktree: "true"}, RosterHidden: true, Status: status.Status{Process: status.ProcessExited}},
		{ID: "resumed", AgentType: "codex", Cwd: worktree, ResumedFrom: "old", Status: status.Status{Process: status.ProcessExited}, ConversationID: "5990e77e-5f9c-4884-ad1e-7744d4660a6f"},
		{ID: "child", AgentType: "codex", Cwd: ext, SpawnedFrom: "resumed", ShimPID: os.Getpid(), ShimStartTime: start, Status: status.Status{Process: status.ProcessRunning}},
	} {
		if err := store.Save(m); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(idempotency.Record{OperationID: "pending-receipt", Action: "semantic_send", SessionID: "child", SessionInstance: "incarnation-1", Phase: idempotency.PhasePrepared})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "idempotency", "idempotency.log"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(ext, ".git"))
	before, err := os.Stat(filepath.Join(repo, ".git", "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	report, code := readCheckoutInventory(t)
	if code != 0 || !report.Complete {
		t.Fatalf("inventory incomplete: %+v", report)
	}
	linked := inventoryPath(t, report, worktree)
	if linked.GitKind != "linked" || linked.Repo != repo || linked.Worktree != worktree || linked.Branch != "refs/heads/independent" || len(linked.References) != 3 {
		t.Fatalf("wrong checkout or lineage: %+v", linked)
	}
	ep := skeleton.EndpointIDForStateDir(root)
	byID := make(map[string]skeleton.CheckoutSession)
	for _, r := range linked.References {
		byID[r.ID] = r
	}
	old, child, resumed := byID[ep+"/old"], byID[ep+"/child"], byID[ep+"/resumed"]
	if !old.Hidden || !slices.Contains(old.Links, "swarm_worktree_record") || child.State != "live" ||
		!slices.Contains(child.Links, "descendant") || len(child.Receipts) != 1 ||
		child.Receipts[0].SessionInstance != "incarnation-1" || resumed.Resume != "candidate" {
		t.Fatalf("missing ownership, liveness, receipt or history: %+v", byID)
	}
	after, err := os.Stat(filepath.Join(repo, ".git", "worktrees"))
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("Git worktree registration changed: %v", err)
	}
	for _, file := range []string{"daemon.sock", "daemon.log", "daemon.lock"} {
		if _, err := os.Stat(filepath.Join(root, file)); !os.IsNotExist(err) {
			t.Fatalf("inventory created daemon state: %s %v", file, err)
		}
	}
}

func TestCheckoutInventoryRetainsRedirectedAndMissingGitRegistrations(t *testing.T) {
	repo := inventoryRepo(t)
	inventoryState(t)
	base := inventoryTempDir(t)
	alpha, beta := filepath.Join(base, "long-alpha"), filepath.Join(base, "b")
	inventoryGit(t, repo, "worktree", "add", "-qb", "alpha", alpha)
	inventoryGit(t, repo, "worktree", "add", "-qb", "beta", beta)
	missing := filepath.Join(base, "missing")
	inventoryGit(t, repo, "worktree", "add", "-qb", "missing", missing)
	if err := os.Rename(missing, filepath.Join(base, "moved-missing")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(alpha, filepath.Join(base, "moved-alpha")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(beta, alpha); err != nil {
		t.Fatal(err)
	}
	report, code := readCheckoutInventory(t, repo, beta)
	a, b, m := inventoryPath(t, report, alpha), inventoryPath(t, report, beta), inventoryPath(t, report, missing)
	if code == 0 || report.Complete || len(a.Unknowns) == 0 || m.PathState != "missing" || len(m.Unknowns) == 0 ||
		b.Worktree != beta || b.Branch != "refs/heads/beta" || len(b.Unknowns) != 0 {
		t.Fatalf("unsafe worktree association: %+v", report)
	}
}

func TestCheckoutInventoryReportsUnknownOfflineEvidenceWithoutCreatingState(t *testing.T) {
	root := filepath.Join(inventoryTempDir(t), "absent-state")
	t.Setenv(daemon.EnvStateDir, root)
	t.Setenv(daemon.EnvSocket, filepath.Join(root, "daemon.sock"))
	t.Setenv("XDG_CONFIG_HOME", inventoryTempDir(t))
	t.Setenv("HOME", inventoryTempDir(t))
	external := inventoryTempDir(t)
	missing := filepath.Join(external, "not-created")
	report, code := readCheckoutInventory(t, external, missing)
	if code == 0 || report.Complete || len(report.Unknowns) == 0 {
		t.Fatalf("missing local state produced confident report: %+v", report)
	}
	if path := inventoryPath(t, report, external); path.GitKind != "outside_git" || len(path.References) != 0 {
		t.Fatalf("external path inventory incorrect: %+v", path)
	}
	if p := inventoryPath(t, report, missing); p.PathState != "missing" || len(p.Unknowns) == 0 {
		t.Fatalf("missing candidate was dropped: %+v", p)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("inventory created local state: %v", err)
	}
}

func TestCheckoutInventoryReportsVerifiedParkedSource(t *testing.T) {
	store, root := inventoryState(t)
	dir := inventoryTempDir(t)
	now := time.Now().UTC()
	m := persist.Meta{ID: "parked", AgentType: "codex", Cwd: dir, Role: "manager", Status: status.Status{Process: status.ProcessExited}, ShimPID: 1, ShimStartTime: 9, ConversationID: "f2cf9a23-98a8-42eb-8a2c-18df99324361"}
	m.ManagerRetirement = persist.ManagerRetirement{OperationID: "retire", Instance: "incarnation", NativeSessionID: m.ConversationID, NativeGeneration: "native-gen", ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime, ClaimedAt: now}
	if err := store.Save(m); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, m.ID, "exit.json")
	body, err := json.Marshal(map[string]time.Time{"finished_at": now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	report, code := readCheckoutInventory(t, dir)
	got := inventoryPath(t, report, dir).References
	if code != 0 || len(got) != 1 || got[0].State != "parked_resume_source" || got[0].Resume != "parked_candidate" {
		t.Fatalf("claimed and exited manager not recognized: %+v", report)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	report, code = readCheckoutInventory(t, dir)
	got = inventoryPath(t, report, dir).References
	if code == 0 || len(got) != 1 || got[0].Resume != "unresolved" || len(got[0].Receipts) != 1 {
		t.Fatalf("missing exit treated as parked evidence: %+v", report)
	}
}
