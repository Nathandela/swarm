package daemon

// FAILING-FIRST (TDD RED, GG-5) for Wave R5 deliverable 1's daemon half (bead
// agents-tracker-hggx.6, playbook "Wave R5 -- phone remote launch"): MACHINE-AUTHORED
// launch presets and their staleness contract, as a launch-policy surface of THIS
// package -- the same package whose two-phase launch() the resolved preset must feed,
// so the preset path cannot fork into a parallel launch pipeline (deliverable 3).
//
// The contract these tests freeze (ADR-007 B144(b), playbook:215-222, :447-448):
//
//   - type LaunchPreset { ID, DisplayName, Agent, Root string; Options map[string]string;
//     Worktree bool } -- a stable opaque id, display name, provider, canonical allowed
//     workspace/worktree root, allowlisted options, and the worktree-isolation default.
//     The preset is MACHINE-AUTHORED: nothing in it ever comes from a phone.
//   - SaveLaunchPresets(stateDir, []LaunchPreset) / LoadLaunchPresets(stateDir):
//     durable custody under the daemon state dir, owner-only (0600), missing file ==
//     zero presets (fail-closed: nothing is launchable until the terminal authors it).
//   - PresetRevision(LaunchPreset) string: a deterministic content binding. The phone
//     echoes this revision inside the signed session_launch, so "the preset I confirmed"
//     and "the preset the machine executes" are the same bytes or the launch refuses.
//   - ErrUnknownPreset / ErrStalePreset sentinels, and
//     ResolveLaunchPreset(presets, id, revision) (LaunchPreset, error): an id this
//     machine never authored is ErrUnknownPreset; a right id at a changed revision is
//     ErrStalePreset ("a changed revision receives stale_preset instead of silently
//     launching different policy", playbook:447-448). Both BEFORE any argv exists.
//   - LaunchSpecForPreset(p, operationID, initialPrompt) (LaunchSpec, error): composes
//     the daemon LaunchSpec from the preset ALONE -- resolved (symlink-hardened) Root as
//     Cwd, the preset's own Options (copied, not aliased), NO ClientEnv ever (D8: "no
//     phone-supplied env"; the preset carries policy, not a phone's environment), the
//     signed operation_id so the EXISTING two-phase reservation engages, and the
//     phone's initial prompt as the one free-text field the phone may contribute.
//
// Every symbol above is intentionally undefined today: this file must fail to compile
// (vet: undefined: LaunchPreset ...) until the GREEN slice adds the surface.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// r5Preset returns a well-formed machine-authored preset rooted at a real directory.
func r5Preset(t *testing.T, id string) LaunchPreset {
	t.Helper()
	return LaunchPreset{
		ID:          id,
		DisplayName: "API repo",
		Agent:       "claude",
		Root:        t.TempDir(),
		Options:     map[string]string{"model": "sonnet"},
		Worktree:    true,
	}
}

// TestR5Presets_SaveLoadRoundTrip_OwnerOnlyDurability: authoring writes the presets
// durably under the daemon state dir with owner-only permissions, and a fresh load
// returns exactly what was authored. A state dir with no presets file loads ZERO
// presets and no error -- the fail-closed first-run state, in which session_launch
// can only answer unknown_preset.
func TestR5Presets_SaveLoadRoundTrip_OwnerOnlyDurability(t *testing.T) {
	dir := shortStateDir(t)

	// First-run: no file, no presets, no error.
	got, err := LoadLaunchPresets(dir)
	if err != nil {
		t.Fatalf("LoadLaunchPresets on empty state dir: %v (missing must mean zero presets, not an error)", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty state dir loaded %d presets, want 0: presets exist only when the terminal authored them", len(got))
	}

	p := r5Preset(t, "preset-api")
	if err := SaveLaunchPresets(dir, []LaunchPreset{p}); err != nil {
		t.Fatalf("SaveLaunchPresets: %v", err)
	}
	got, err = LoadLaunchPresets(dir)
	if err != nil {
		t.Fatalf("LoadLaunchPresets after save: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d presets, want 1", len(got))
	}
	if got[0].ID != p.ID || got[0].Agent != p.Agent || got[0].Root != p.Root ||
		got[0].DisplayName != p.DisplayName || got[0].Options["model"] != "sonnet" || !got[0].Worktree {
		t.Errorf("round-trip mutated the preset: got %+v, want %+v", got[0], p)
	}

	// Owner-only: the presets file is remote-launch POLICY; a group/world-readable
	// policy file is the same defect class ADR-004 pins for the session dir.
	fi, err := os.Stat(launchPresetsPath(dir))
	if err != nil {
		t.Fatalf("stat presets file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("presets file perms = %o, want 0600", perm)
	}
}

// TestR5Presets_RevisionIsContentBound: the revision is a deterministic function of the
// preset's content -- identical content yields the identical revision (including across
// option-map iteration orders), and changing ANY policy-bearing field changes it. This
// is what makes the phone's confirmed revision a binding: if any of these mutations
// left the revision unchanged, a machine-side edit could ride under a stale confirm.
func TestR5Presets_RevisionIsContentBound(t *testing.T) {
	base := LaunchPreset{
		ID: "p1", DisplayName: "d", Agent: "claude", Root: "/tmp/ws",
		Options: map[string]string{"a": "1", "b": "2"}, Worktree: true,
	}
	same := LaunchPreset{
		ID: "p1", DisplayName: "d", Agent: "claude", Root: "/tmp/ws",
		Options: map[string]string{"b": "2", "a": "1"}, Worktree: true,
	}
	if PresetRevision(base) == "" {
		t.Fatal("PresetRevision returned empty; a revision the phone must echo cannot be the zero string")
	}
	if PresetRevision(base) != PresetRevision(same) {
		t.Errorf("identical content (option order aside) produced different revisions %q vs %q",
			PresetRevision(base), PresetRevision(same))
	}

	mutations := map[string]func(p LaunchPreset) LaunchPreset{
		"agent":    func(p LaunchPreset) LaunchPreset { p.Agent = "codex"; return p },
		"root":     func(p LaunchPreset) LaunchPreset { p.Root = "/tmp/other"; return p },
		"worktree": func(p LaunchPreset) LaunchPreset { p.Worktree = false; return p },
		"option-value": func(p LaunchPreset) LaunchPreset {
			p.Options = map[string]string{"a": "1", "b": "CHANGED"}
			return p
		},
		"option-added": func(p LaunchPreset) LaunchPreset {
			p.Options = map[string]string{"a": "1", "b": "2", "c": "3"}
			return p
		},
	}
	for name, mutate := range mutations {
		if PresetRevision(mutate(base)) == PresetRevision(base) {
			t.Errorf("mutating %s did not change the revision: a phone that confirmed the old "+
				"policy would silently launch the new one", name)
		}
	}
}

// TestR5Presets_ResolveUnknownIDIsErrUnknownPreset: an id this machine never authored
// resolves to the ErrUnknownPreset sentinel -- the machine-side decision behind the
// stable unknown_preset refusal, made before any argv composition.
func TestR5Presets_ResolveUnknownIDIsErrUnknownPreset(t *testing.T) {
	ps := []LaunchPreset{r5Preset(t, "preset-api")}
	_, err := ResolveLaunchPreset(ps, "preset-never-authored", "whatever")
	if !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("ResolveLaunchPreset(unknown id) = %v, want ErrUnknownPreset: a preset the "+
			"machine did not author must refuse by its own stable sentinel, never launch and "+
			"never fall through to a generic error", err)
	}
}

// TestR5Presets_ResolveChangedRevisionIsErrStalePreset: the right id at a revision that
// no longer matches the preset's current content is ErrStalePreset -- the machine
// re-authored the preset between the phone's list and its confirm, so the launch the
// user confirmed is not the launch this machine would run (playbook:447-448).
func TestR5Presets_ResolveChangedRevisionIsErrStalePreset(t *testing.T) {
	p := r5Preset(t, "preset-api")
	confirmed := PresetRevision(p) // what the phone saw and signed

	p.Options = map[string]string{"model": "opus"} // terminal edits the preset afterwards
	_, err := ResolveLaunchPreset([]LaunchPreset{p}, "preset-api", confirmed)
	if !errors.Is(err, ErrStalePreset) {
		t.Fatalf("ResolveLaunchPreset(stale revision) = %v, want ErrStalePreset: a changed "+
			"revision must refuse, never silently launch different policy", err)
	}

	// And the happy path still resolves: the CURRENT revision is accepted.
	got, err := ResolveLaunchPreset([]LaunchPreset{p}, "preset-api", PresetRevision(p))
	if err != nil {
		t.Fatalf("ResolveLaunchPreset(current revision): %v", err)
	}
	if got.ID != "preset-api" {
		t.Errorf("resolved preset id = %q, want preset-api", got.ID)
	}
}

// TestR5Presets_SpecComposition_ResolvedRootNoEnvCopiedOptions: LaunchSpecForPreset
// composes the daemon spec from the preset alone.
//
//   - Cwd is the SYMLINK-RESOLVED real path of the preset root (D8: "checked and handed
//     to the shim as the same fully-resolved real path" -- no check-on-resolved/
//     use-on-original gap survives into the spec).
//   - ClientEnv is nil, unconditionally: env comes from daemon policy, never a phone.
//   - Options are the preset's own, COPIED: mutating the returned spec's map must not
//     write back into the stored preset (a policy object shared by reference is a
//     policy an executed launch can edit).
//   - OperationID and InitialPrompt are carried, so the existing two-phase idempotent
//     reservation engages on this exact spec and the prompt reaches the adapter.
func TestR5Presets_SpecComposition_ResolvedRootNoEnvCopiedOptions(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "ws-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
	resolvedReal, err := filepath.EvalSymlinks(real) // macOS /tmp is itself a symlink
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", real, err)
	}

	p := r5Preset(t, "preset-sym")
	p.Root = link

	spec, err := LaunchSpecForPreset(p, "devA:01JOP", "fix the flaky test")
	if err != nil {
		t.Fatalf("LaunchSpecForPreset: %v", err)
	}
	if spec.Cwd != resolvedReal {
		t.Errorf("spec.Cwd = %q, want the resolved real path %q: the path policy checked must "+
			"be the path the shim gets", spec.Cwd, resolvedReal)
	}
	if spec.ClientEnv != nil {
		t.Errorf("spec.ClientEnv = %v, want nil: a preset launch carries no phone env, ever", spec.ClientEnv)
	}
	if spec.AgentType != p.Agent {
		t.Errorf("spec.AgentType = %q, want %q", spec.AgentType, p.Agent)
	}
	if spec.OperationID != "devA:01JOP" {
		t.Errorf("spec.OperationID = %q, want the signed operation id devA:01JOP -- without it "+
			"the two-phase reservation never engages and a replay double-spawns", spec.OperationID)
	}
	if spec.InitialPrompt != "fix the flaky test" {
		t.Errorf("spec.InitialPrompt = %q, want the phone's prompt carried verbatim", spec.InitialPrompt)
	}
	if spec.Options["model"] != "sonnet" {
		t.Fatalf("spec.Options = %v, want the preset's own allowlisted options", spec.Options)
	}
	spec.Options["model"] = "TAMPERED"
	if p.Options["model"] != "sonnet" {
		t.Error("mutating the spec's option map mutated the stored preset: Options must be copied, not aliased")
	}
}

// TestR5Presets_SpecCompositionRefusesUnresolvableRoot: a preset whose root no longer
// resolves (deleted, or a dangling symlink) refuses at composition -- there is no spec,
// so nothing downstream can stat, spawn, or half-launch into a path that is not the
// one the policy admitted.
func TestR5Presets_SpecCompositionRefusesUnresolvableRoot(t *testing.T) {
	p := r5Preset(t, "preset-gone")
	p.Root = filepath.Join(t.TempDir(), "deleted-after-authoring")

	if _, err := LaunchSpecForPreset(p, "devA:01JGONE", ""); err == nil {
		t.Fatal("LaunchSpecForPreset resolved a spec for a nonexistent root; want a refusal " +
			"before any spec exists")
	}
}
