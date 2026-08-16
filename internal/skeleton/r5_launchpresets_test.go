package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-2 review fix-pack (bead
// agents-tracker-hggx.6), MEDIUM 1: the assembled-daemon half of the preset flow
// (internal/skeleton/launchpresets.go) landed in round 1 with ZERO tests. This file
// is that missing fence: custody -> wire-view conversion, sentinel mapping, the
// D8 root re-resolution, the content-bound policy revision, the namespaced
// operation-status answer, the D10 activity log's 0600 custody (including over a
// PRE-EXISTING loose-mode file, the exact gap daemon.SaveLaunchPresets closes with
// its explicit Chmod), and the pinned-registry DeviceCapability seam behind the
// launch_presets reply's device_capability field.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// presetAPI is a coreAPI over a bare state dir: the preset custody methods read
// only a.stateDir, so no daemon core and no watch goroutine is needed.
func presetAPI(t *testing.T) (*coreAPI, string) {
	t.Helper()
	dir := t.TempDir()
	return &coreAPI{stateDir: dir, endpointID: "mach1"}, dir
}

// TestR5Skeleton_LaunchPresetListServesTheAuthoredCustody: the list is EXACTLY the
// machine-authored custody as wire views; absent custody answers an empty list, and
// the policy revision is content-bound -- any preset edit changes it, so an operator
// can correlate a phone's stale view with the machine's current policy.
func TestR5Skeleton_LaunchPresetListServesTheAuthoredCustody(t *testing.T) {
	a, dir := presetAPI(t)

	views, emptyRev := a.LaunchPresetList()
	if len(views) != 0 {
		t.Fatalf("empty custody answered %d presets, want 0 -- never an invented default", len(views))
	}

	root := t.TempDir()
	p := daemon.LaunchPreset{ID: "preset-api", DisplayName: "API repo", Agent: "claude",
		Root: root, Options: map[string]string{"model": "sonnet"}, Worktree: true}
	if err := daemon.SaveLaunchPresets(dir, []daemon.LaunchPreset{p}); err != nil {
		t.Fatalf("SaveLaunchPresets: %v", err)
	}

	views, rev := a.LaunchPresetList()
	if len(views) != 1 {
		t.Fatalf("authored custody answered %d presets, want 1", len(views))
	}
	v := views[0]
	if v.ID != "preset-api" || v.Agent != "claude" || v.Root != root || !v.Worktree ||
		v.Options["model"] != "sonnet" || v.Revision != daemon.PresetRevision(p) {
		t.Errorf("preset view = %+v, want the authored preset with its content revision", v)
	}
	if rev == "" || rev == emptyRev {
		t.Errorf("policy revision = %q (empty custody's was %q); authoring must change it", rev, emptyRev)
	}

	// Content-bound: an edit to any preset changes the POLICY revision too.
	p.DisplayName = "API repo (renamed)"
	if err := daemon.SaveLaunchPresets(dir, []daemon.LaunchPreset{p}); err != nil {
		t.Fatalf("SaveLaunchPresets: %v", err)
	}
	if _, rev2 := a.LaunchPresetList(); rev2 == rev {
		t.Errorf("policy revision unchanged (%q) across a preset edit; staleness is uncorrelatable", rev)
	}
}

// TestR5Skeleton_ResolveMapsSentinelsAndReResolvesTheRoot: unknown id ->
// protocol.ErrUnknownPreset, changed revision -> protocol.ErrStalePreset (the wire
// layer's stable-code sources), and the resolved view carries the SYMLINK-RESOLVED
// real root via daemon.LaunchSpecForPreset -- D8's no-gap rule: the path the policy
// checks is the path the shim gets.
func TestR5Skeleton_ResolveMapsSentinelsAndReResolvesTheRoot(t *testing.T) {
	a, dir := presetAPI(t)

	real := t.TempDir()
	realResolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	p := daemon.LaunchPreset{ID: "preset-ln", DisplayName: "Linked", Agent: "claude", Root: link}
	if err := daemon.SaveLaunchPresets(dir, []daemon.LaunchPreset{p}); err != nil {
		t.Fatalf("SaveLaunchPresets: %v", err)
	}

	if _, err := a.ResolveLaunchPreset("preset-never", "rev-x"); !errors.Is(err, protocol.ErrUnknownPreset) {
		t.Errorf("unknown id error = %v, want protocol.ErrUnknownPreset (the unknown_preset source)", err)
	}
	if _, err := a.ResolveLaunchPreset("preset-ln", "rev-stale"); !errors.Is(err, protocol.ErrStalePreset) {
		t.Errorf("stale revision error = %v, want protocol.ErrStalePreset (the stale_preset source)", err)
	}

	v, err := a.ResolveLaunchPreset("preset-ln", daemon.PresetRevision(p))
	if err != nil {
		t.Fatalf("ResolveLaunchPreset: %v", err)
	}
	if v.Root != realResolved {
		t.Errorf("resolved view root = %q, want the symlink-resolved %q (D8: no "+
			"check-on-resolved/use-on-original gap)", v.Root, realResolved)
	}
}

// TestR5Skeleton_ActivityLogIsOwnerOnlyEvenOverAPreExistingFile: the D10 activity
// log is 0600 custody. O_CREATE's mode applies only on create, so a pre-existing
// group/world-readable file must be HARDENED on append -- the same defect class
// daemon.SaveLaunchPresets closes with its explicit Chmod, for the same stated
// reason (a loose-mode remote-activity log leaks device ids and operation ids to
// every local user).
func TestR5Skeleton_ActivityLogIsOwnerOnlyEvenOverAPreExistingFile(t *testing.T) {
	a, dir := presetAPI(t)
	logPath := filepath.Join(dir, "remote-activity.log")

	a.RecordRemoteActivity(schema.ActivityRecord{
		Action: "session_launch", DeviceID: "devA", OperationID: "devA:01J1", Outcome: "applied", SessionID: "s1",
	})
	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("activity log not written: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("fresh activity log mode = %o, want 0600", got)
	}

	// The gap: a pre-existing loose-mode file must not stay loose.
	if err := os.Chmod(logPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	a.RecordRemoteActivity(schema.ActivityRecord{
		Action: "session_launch", DeviceID: "devA", OperationID: "devA:01J2", Outcome: "refused", Code: schema.CodeStalePreset,
	})
	fi, err = os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("pre-existing activity log mode = %o after append, want 0600: O_CREATE's mode "+
			"never applies to an existing file, so without an explicit Chmod the log stays "+
			"world-readable forever", got)
	}

	// And the records are real JSON lines carrying the D10 facts.
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("activity log has %d lines, want 2", len(lines))
	}
	var rec struct {
		schema.ActivityRecord
		At string `json:"at"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil {
		t.Fatalf("second record is not JSON: %v (%q)", err, lines[1])
	}
	if rec.Outcome != "refused" || rec.Code != schema.CodeStalePreset || rec.At == "" {
		t.Errorf("refusal record = %+v, want outcome/code/timestamp", rec)
	}
}

// TestR5Skeleton_DeviceCapabilityReadsThePinnedRegistry: the seam behind the
// launch_presets reply's device_capability field reads the PINNED registry --
// never the wire -- and fails honest: unknown device or no registry answers
// (_, false), never an invented tier.
func TestR5Skeleton_DeviceCapabilityReadsThePinnedRegistry(t *testing.T) {
	reg, err := device.Open(t.TempDir())
	if err != nil {
		t.Fatalf("device.Open: %v", err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	id := device.DeviceIDFor(pub)
	if err := reg.Add(device.Record{
		DeviceID: id, Name: "phone", CommandSignPub: pub,
		NoiseStaticPub: make([]byte, 32), RelayAuthPub: make([]byte, 32), RecipientPub: make([]byte, 32),
		Capability: device.CapReadOnly,
	}); err != nil {
		t.Fatalf("registry Add: %v", err)
	}

	a := &coreAPI{devices: reg}
	tier, ok := a.DeviceCapability(id)
	if !ok || tier != "read_only" {
		t.Errorf("DeviceCapability(%s) = (%q, %v), want (read_only, true) from the pinned registry", id, tier, ok)
	}
	if tier, ok := a.DeviceCapability("dev-nobody"); ok || tier != "" {
		t.Errorf("unknown device answered (%q, %v), want (\"\", false) -- never an invented tier", tier, ok)
	}
	bare := &coreAPI{}
	if tier, ok := bare.DeviceCapability(id); ok || tier != "" {
		t.Errorf("registry-less coreAPI answered (%q, %v), want (\"\", false)", tier, ok)
	}
}

// TestR5Skeleton_OperationOutcomeIsNamespacedToTheWireForm: the applied answer names
// the session in the namespaced form the phone's roster carries, so the phone can
// actually find it; an id the daemon has no record of answers (_, false).
func TestR5Skeleton_OperationOutcomeIsNamespacedToTheWireForm(t *testing.T) {
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "swskr5op")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	core, err := daemon.Open(daemon.Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "d.sock"),
		LockPath:    filepath.Join(dir, "d.lock"),
		LogPath:     filepath.Join(dir, "d.log"),
		ShimBinary:  swarmBin,
		MaxSessions: 8,
	})
	if err != nil {
		t.Fatalf("daemon.Open: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	const op = "devA:01JSKOP"
	m, err := core.Launch(daemon.LaunchSpec{
		AgentType:   "fake",
		Argv:        []string{fakeAgentBin, mustScript(t, "print booting\nidle 60s\n")},
		Cwd:         t.TempDir(),
		ClientEnv:   []string{"PATH=" + os.Getenv("PATH")},
		Cols:        80, Rows: 24,
		OperationID: op,
	})
	if err != nil {
		t.Fatalf("core Launch: %v", err)
	}
	t.Cleanup(func() { _ = core.Kill(m.ID) })

	a := &coreAPI{core: core, endpointID: "mach1"}
	out, ok := a.RemoteOperationOutcome(op)
	if !ok || out.State != schema.OutcomeApplied {
		t.Fatalf("RemoteOperationOutcome = (%+v, %v), want applied", out, ok)
	}
	if want := protocol.NamespacedID("mach1", m.ID); out.SessionID != want {
		t.Errorf("applied session id = %q, want the namespaced %q (the id the phone's roster carries)", out.SessionID, want)
	}
	if _, ok := a.RemoteOperationOutcome("devA:01JNEVER"); ok {
		t.Error("an id the daemon never saw answered ok=true; unknown_operation must come from absence, not invention")
	}
}
