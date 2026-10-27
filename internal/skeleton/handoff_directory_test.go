package skeleton

// agents-tracker-xl6k: a first-class `swarm handoff` child used to inherit the
// source's ROSTER cwd -- for a worktree-isolated source, the repository root -- while
// the source's agent worked in its provider worktree, a separate checkout. The CLI
// sends the roster cwd because SessionView carries no AgentCwd; the daemon holds the
// authoritative value in the source's row. These tests drive coreAPI.Launch and
// assert where the shim runs the agent (the spawn binding) and the persisted launch
// cwd.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// handoffDirFixture persists the given roster metas into a fresh state dir and
// builds the daemon + coreAPI over it (the continuityAPI precedent). The dir is
// short-pathed under /tmp for the daemon socket's sun_path cap.
func handoffDirFixture(t *testing.T, metas ...persist.Meta) (*coreAPI, string) {
	t.Helper()
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "sw-handoffcwd-")
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
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	api := newCoreAPI(core, fakeAgentBin, endpointID(dir), nil)
	t.Cleanup(api.close)
	return api, dir
}

// handoffWorktreeSource is the roster row of a worktree-isolated session: the launch
// it was requested in (the repo root) as Cwd and the checkout the agent actually ran
// in as AgentCwd. The fixture worktree must exist on disk: a source that RAN there
// implies it did, and the correction stats it. withAgentCwd=false is the legacy or
// stale shape: an AgentCwd recorded on the row without a live directory.
func handoffWorktreeSource(t *testing.T, id string, withAgentCwd bool) (src persist.Meta, repo, worktree string) {
	t.Helper()
	repo = t.TempDir()
	worktree = filepath.Join(repo, ".swarm", "worktrees", id)
	if withAgentCwd {
		if err := os.MkdirAll(worktree, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	src = endedCodexSource(id, nil)
	src.Cwd = repo
	if withAgentCwd {
		src.AgentCwd = worktree
	}
	// Living shape: `swarm handoff` refuses non-running sources client-side, so a
	// first-class child always launches against a running source's row. Reconcile
	// may reclassify the fixture, which the correction must not care about.
	src.Status.Process = status.ProcessRunning
	return src, repo, worktree
}

// handoffChildSpec is the launch a first-class `swarm handoff` CLI produces, with
// argv carried explicitly (the launchFake fixture pattern) so no agent binary
// resolution is under test. cwd mirrors the caller's constraint: the CLI can send
// nothing but the source's roster Cwd (the launch cwd).
func handoffChildSpec(t *testing.T, source persist.Meta, cwd string) daemon.LaunchSpec {
	t.Helper()
	return daemon.LaunchSpec{
		AgentType:   "fake",
		Argv:        []string{fakeAgentBin, mustScript(t, "print handoff-child\nidle 60s\n")},
		Cwd:         cwd,
		ClientEnv:   []string{"PATH=" + os.Getenv("PATH")},
		Cols:        80,
		Rows:        24,
		SpawnedFrom: source.ID,
		SpawnIntent: protocol.SpawnIntentHandoff,
		Supervision: protocol.SupervisionPassive,
	}
}

// spawnBinding reads the child's spawn binding: the exact cwd the shim execs the
// agent in (the e2e/worktree_e2e_test.go contract), out of the 0600
// shim-launch.json the daemon wrote at spawn (launch.go's contract).
func spawnBinding(t *testing.T, dir, id string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id, "shim-launch.json"))
	if err != nil {
		t.Fatalf("read session %s's launch config: %v", id, err)
	}
	var lc struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(data, &lc); err != nil {
		t.Fatalf("decode session %s's launch config: %v", id, err)
	}
	return lc.Cwd
}

func killSpawnedShim(t *testing.T, m persist.Meta) {
	t.Helper()
	if m.ShimPID > 0 {
		_ = syscall.Kill(m.ShimPID, syscall.SIGTERM)
	}
}

// TestHandoffChildLaunchesInTheSourceProviderWorktree is the worktree-vs-repository
// regression: the successor must start in the checkout the source agent worked in,
// not in the repository root the roster row records as the launch cwd.
func TestHandoffChildLaunchesInTheSourceProviderWorktree(t *testing.T) {
	src, repo, worktree := handoffWorktreeSource(t, "handoff-src", true)
	api, dir := handoffDirFixture(t, src)

	child, err := api.Launch(handoffChildSpec(t, src, repo))
	if err != nil {
		t.Fatalf("first-class handoff child launch refused: %v", err)
	}
	t.Cleanup(func() { killSpawnedShim(t, child) })

	if got := spawnBinding(t, dir, child.ID); got != worktree {
		t.Fatalf("handoff child's agent runs in %q, want the source's provider worktree %q; the repo root %q is a different checkout", got, worktree, repo)
	}
	if child.Cwd != worktree {
		t.Fatalf("the child's persisted launch cwd = %q, want %q: the roster row must name the checkout the agent runs in", child.Cwd, worktree)
	}
}

// TestHandoffChildKeepsTheClientCwdForALegacySource pins the fallback: a row
// recorded before AgentCwd existed offers no captured checkout, so the launch uses
// the source's recorded launch cwd (the same cwd the CLI sends; the daemon trusts
// the row, not the caller's copy).
func TestHandoffChildKeepsTheClientCwdForALegacySource(t *testing.T) {
	src, repo, _ := handoffWorktreeSource(t, "legacy-src", false) // AgentCwd stays empty
	api, dir := handoffDirFixture(t, src)

	child, err := api.Launch(handoffChildSpec(t, src, repo))
	if err != nil {
		t.Fatalf("legacy source's handoff child launch refused: %v", err)
	}
	t.Cleanup(func() { killSpawnedShim(t, child) })

	if got := spawnBinding(t, dir, child.ID); got != repo {
		t.Fatalf("legacy source's handoff child's agent runs in %q, want the unchanged launch cwd %q", got, repo)
	}
	if child.Cwd != repo {
		t.Fatalf("legacy source's child persisted launch cwd = %q, want %q", child.Cwd, repo)
	}
}

// TestHandoffChildRefusesAStaleSourceWorktree is the adversarial case the existing
// contracts agree on: the source ran in a worktree that has since been torn down.
// A silent launch into the repo root would repeat the wrong-checkout bug one level
// of indirection later; hands-off STEP 5b and resumeDirectory both refuse by name
// for exactly this shape, so the first-class path refuses the same way.
func TestHandoffChildRefusesAStaleSourceWorktree(t *testing.T) {
	src, repo, gone := handoffWorktreeSource(t, "stale-src", false)
	src.AgentCwd = gone // recorded but torn down: the directory must NOT exist
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("fixture must name a missing worktree; stat err=%v", err)
	}
	api, dir := handoffDirFixture(t, src)

	child, err := api.Launch(handoffChildSpec(t, src, repo))
	if err == nil {
		t.Fatalf("a handoff child was launched despite the source's worktree %q being gone; binding cwd=%q meta=%+v", gone, spawnBinding(t, dir, child.ID), child)
	}
	if !strings.Contains(err.Error(), "handoff:") || !strings.Contains(err.Error(), gone) || !strings.Contains(err.Error(), "no longer a directory") {
		t.Fatalf("stale worktree refusal = %q, want a handoff refusal naming %q", err, gone)
	}
	// The refusal must be total: no reservation, no spawned process, no halfway child.
	if got := len(api.List()); got != 1 {
		t.Fatalf("after the refusal the roster holds %d rows (the source only), want no new session", got)
	}
}

// TestUnsupervisedSpawnChildKeepsItsClientCwd pins the shape gate's boundary:
// `swarm spawn --handoff` inside a session records the handoff intent WITHOUT a
// supervision mode, and its cwd is the caller's own (os.Getwd, the agent's cwd) or
// an explicit --dir. The correction owns the first-class shape only.
func TestUnsupervisedSpawnChildKeepsItsClientCwd(t *testing.T) {
	src, repo, _ := handoffWorktreeSource(t, "spawn-src", true)
	callerCwd := t.TempDir()
	api, dir := handoffDirFixture(t, src)

	spec := handoffChildSpec(t, src, callerCwd)
	spec.Supervision = "" // the agent-verb spawn carries no supervision mode
	child, err := api.Launch(spec)
	if err != nil {
		t.Fatalf("spawn-shaped launch refused: %v", err)
	}
	t.Cleanup(func() { killSpawnedShim(t, child) })

	if got := spawnBinding(t, dir, child.ID); got != callerCwd {
		t.Fatalf("spawn child's agent runs in %q, want the caller's cwd %q (not the source's repo root %q)", got, callerCwd, repo)
	}
	if child.Cwd != callerCwd {
		t.Fatalf("spawn child persisted launch cwd = %q, want the caller's %q", child.Cwd, callerCwd)
	}
	if child.AgentCwd != "" {
		t.Fatalf("spawn child recorded AgentCwd %q without worktree isolation", child.AgentCwd)
	}
}

// TestHandoffChildRefusesAnUncapturedWorktreeSource mirrors resumeDirectory's
// unknowable-checkout refusal: a source whose roster row says its launch was
// worktree-isolated but recorded no captured AgentCwd offers no directory the
// successor can trust -- ProviderCwd degrades to the roster cwd, which is exactly
// the wrong checkout, now presented as if it were correct. Refuse by name like the
// resume path does.
func TestHandoffChildRefusesAnUncapturedWorktreeSource(t *testing.T) {
	src, _, _ := handoffWorktreeSource(t, "uncaptured-src", false)
	src.LaunchOptions = map[string]string{protocol.OptionWorktree: "true"}
	api, _ := handoffDirFixture(t, src)

	child, err := api.Launch(handoffChildSpec(t, src, t.TempDir()))
	t.Cleanup(func() { killSpawnedShim(t, child) })
	if err == nil {
		t.Fatalf("a handoff child was launched although the source's worktree path was not captured, placing it in the roster checkout")
	}
	if !strings.Contains(err.Error(), "handoff:") || !strings.Contains(err.Error(), "not captured") {
		t.Fatalf("uncaptured-worktree refusal = %q, want the handoff refusal matching resumeDirectory's", err)
	}
	if got := len(api.List()); got != 1 {
		t.Fatalf("after the refusal the roster holds %d rows (the source only), want no new session", got)
	}
}

// TestResumeBeatsTheHandoffShapeForALineageChildReLaunch guards the authwatch
// recycle interaction: recycling a SUPERVISED handoff child re-launches it as a
// RESUME (Options resume_from) while carrying the ORIGINAL lineage fields verbatim.
// The resume layer already picks the directory (resumeDirectory) from the child's
// own record, so the handoff correction must not run over it -- neither overriding
// it with the grandparent's row (a different checkout) nor refusing on a
// grandparent worktree the resume never needed.
func TestResumeBeatsTheHandoffShapeForALineageChildReLaunch(t *testing.T) {
	grandparent, _, staleWorktree := handoffWorktreeSource(t, "recycle-ancestor", false)
	grandparent.AgentCwd = staleWorktree // recorded, but the directory is long gone
	endedChild := endedCodexSource("handoff-child", nil)
	endedChild.Cwd = t.TempDir() // the child resumed into its OWN checkout
	endedChild.AgentCwd = t.TempDir()
	endedChild.SpawnedFrom, endedChild.SpawnIntent, endedChild.Supervision = grandparent.ID, protocol.SpawnIntentHandoff, protocol.SupervisionPassive
	api, _ := handoffDirFixture(t, grandparent, endedChild)

	spec := daemon.LaunchSpec{
		AgentType: "codex",
		Cwd:       endedChild.Cwd,
		// A codex binary never resolves under this PATH: the launch must reach argv
		// resolution, not the handoff correction, and this test's failure output
		// distinguishes the two.
		ClientEnv:   []string{"PATH=" + filepath.Join(t.TempDir())},
		Cols:        80,
		Rows:        24,
		SpawnedFrom: grandparent.ID,
		SpawnIntent: protocol.SpawnIntentHandoff,
		Supervision: protocol.SupervisionPassive,
		// The endpoint prefix is the FIXTURE'S OWN so the resume resolves inside
		// this daemon (a prefix naming another endpoint fails at the roster
		// lookup, before any cwd layer, and the test would then prove nothing).
		Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(api.endpointID, endedChild.ID)},
	}
	_, err := api.Launch(spec)
	if err == nil {
		t.Fatal("the recycle would need a codex binary; launching anyway is out of this test's scope")
	}
	// The resume's own layers must own the outcome: it has to REACH argv
	// resolution (the only failure this PATH can produce) -- anything earlier
	// means the resume resolution never happened, and any worktree refusal means
	// the handoff correction leaked into the resume.
	if !strings.Contains(err.Error(), "not found on the agent PATH") {
		t.Fatalf("lineage resume failure = %q, want the intended codex binary-resolution failure after resumeDirectory ran", err)
	}
	if strings.Contains(err.Error(), "no longer a directory") || strings.Contains(err.Error(), staleWorktree) {
		t.Fatalf("the handoff cwd correction leaked into a resume launch: %q", err)
	}
}

// TestHandoffChildRefusesAMissingSource pins the race the CLI cannot close: the
// CLI resolves the source on its own roster and sends that source's identity, but
// the row can be deleted between its List and the daemon's launch. Launching with
// the caller's cwd then repeats the wrong-checkout bug the correction exists to
// close, so the already-narrow first-class shape refuses an absent lineage row by
// name instead, with no reservation and no spawn.
func TestHandoffChildRefusesAMissingSource(t *testing.T) {
	src, _, worktree := handoffWorktreeSource(t, "ghost-parent", true)
	callerCwd := t.TempDir()
	api, dir := handoffDirFixture(t) // the roster deliberately holds no row

	child, err := api.Launch(handoffChildSpec(t, src, callerCwd))
	t.Cleanup(func() { killSpawnedShim(t, child) })
	if err == nil {
		t.Fatalf("a handoff child was launched despite missing lineage %q; binding cwd=%q, and no source row confirms the worktree %q at all", src.ID, spawnBinding(t, dir, child.ID), worktree)
	}
	if !strings.Contains(err.Error(), "handoff:") || !strings.Contains(err.Error(), src.ID) {
		t.Fatalf("missing-source refusal = %q, want a handoff refusal naming %q", err, src.ID)
	}
	if got := len(api.List()); got != 0 {
		t.Fatalf("after the refusal the roster holds %d rows, want none: the refusal must not reserve a session", got)
	}
}
