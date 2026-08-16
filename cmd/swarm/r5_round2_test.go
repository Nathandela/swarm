package main

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-2 review fix-pack (bead
// agents-tracker-hggx.6), MAJOR 3: the setup UX had only add/list, so an authored
// preset was launchable by any full-tier phone FOREVER -- no operator path could
// produce the unknown_preset refusal (nothing withdrew an id) or the stale_preset
// refusal (nothing re-authored a preset), and the phone's remedy copy ("pick it
// again from the refreshed list") prescribed remedies the machine could not perform.
// Separately, --agent was not validated against the adapter registry, contradicting
// presets.go's own stated principle ("a preset nobody can launch is a setup error
// surfaced at setup time, not at the phone's confirm").
//
// The round-2 contract:
//
//   swarm remote presets remove <id>   withdraws the preset: the id stops being
//       authored (subsequent session_launch -> unknown_preset), an unknown id
//       refuses naming it, and list shows the explicit empty state again.
//   swarm remote presets edit <id> [--name|--agent|--root|--worktree=...]
//       re-authors IN PLACE: same id, changed fields, and therefore a CHANGED
//       revision -- the operator path behind stale_preset.
//   swarm remote presets add --agent <not-a-provider>   refuses at authoring time,
//       naming the unknown provider and the registered ones, and authors nothing.

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
)

// TestR5Round2_PresetsRemoveWithdrawsTheAuthoring: remove deletes the preset from
// custody durably (a fresh invocation lists the empty state), and an id the machine
// never authored refuses naming it.
func TestR5Round2_PresetsRemoveWithdrawsTheAuthoring(t *testing.T) {
	stateDir := t.TempDir()
	root := t.TempDir()

	exit, out, errOut := runPresets(t, stateDir, "add", "--name", "API repo", "--agent", "claude", "--root", root)
	if exit != 0 {
		t.Fatalf("presets add exit = %d; stderr=%q", exit, errOut)
	}
	id := mintedPresetID(t, out)

	exit, out, errOut = runPresets(t, stateDir, "remove", id)
	if exit != 0 {
		t.Fatalf("presets remove exit = %d, want 0; stderr=%q", exit, errOut)
	}
	if !strings.Contains(out, id) {
		t.Errorf("remove stdout %q does not name the withdrawn id; a silent withdrawal is the D3 "+
			"defect class on the terminal side", out)
	}

	presets, err := daemon.LoadLaunchPresets(stateDir)
	if err != nil {
		t.Fatalf("LoadLaunchPresets: %v", err)
	}
	if len(presets) != 0 {
		t.Errorf("custody still holds %d presets after remove, want 0: an unremovable preset is "+
			"launchable by any full-tier phone forever", len(presets))
	}

	exit, _, errOut = runPresets(t, stateDir, "remove", "preset-never-authored")
	if exit == 0 {
		t.Error("removing a never-authored id exited 0; want a refusal")
	}
	if !strings.Contains(errOut, "preset-never-authored") {
		t.Errorf("unknown-id refusal %q does not name the id", errOut)
	}
}

// TestR5Round2_PresetsEditReauthorsInPlaceAndChangesTheRevision: edit keeps the id,
// changes the named field, and therefore changes the content revision -- the
// operator path that makes a phone's already-confirmed selection answer
// stale_preset instead of silently launching different policy.
func TestR5Round2_PresetsEditReauthorsInPlaceAndChangesTheRevision(t *testing.T) {
	stateDir := t.TempDir()
	root := t.TempDir()

	exit, out, errOut := runPresets(t, stateDir, "add", "--name", "API repo", "--agent", "claude", "--root", root)
	if exit != 0 {
		t.Fatalf("presets add exit = %d; stderr=%q", exit, errOut)
	}
	id := mintedPresetID(t, out)
	before, err := daemon.LoadLaunchPresets(stateDir)
	if err != nil || len(before) != 1 {
		t.Fatalf("LoadLaunchPresets after add = (%v, %v), want one preset", before, err)
	}
	revBefore := daemon.PresetRevision(before[0])

	exit, out, errOut = runPresets(t, stateDir, "edit", id, "--name", "API repo (worktrees)")
	if exit != 0 {
		t.Fatalf("presets edit exit = %d, want 0; stderr=%q", exit, errOut)
	}
	after, err := daemon.LoadLaunchPresets(stateDir)
	if err != nil || len(after) != 1 {
		t.Fatalf("LoadLaunchPresets after edit = (%v, %v), want one preset", after, err)
	}
	if after[0].ID != id {
		t.Errorf("edit changed the id (%q -> %q); the phone selects by this opaque id and edit "+
			"must re-author IN PLACE", id, after[0].ID)
	}
	if after[0].DisplayName != "API repo (worktrees)" {
		t.Errorf("edited display name = %q, want the new one", after[0].DisplayName)
	}
	revAfter := daemon.PresetRevision(after[0])
	if revAfter == revBefore {
		t.Error("edit left the revision unchanged; without a revision change a phone that " +
			"confirmed the OLD policy launches the NEW one silently -- the exact defect " +
			"stale_preset exists to refuse")
	}
	if !strings.Contains(out, revAfter) {
		t.Errorf("edit stdout %q does not print the new revision %s; the operator correlates "+
			"stale_preset refusals against it", out, revAfter)
	}

	exit, _, errOut = runPresets(t, stateDir, "edit", "preset-never-authored", "--name", "x")
	if exit == 0 {
		t.Error("editing a never-authored id exited 0; want a refusal")
	}
	if !strings.Contains(errOut, "preset-never-authored") {
		t.Errorf("unknown-id refusal %q does not name the id", errOut)
	}
}

// TestR5Round2_PresetsAddRefusesAnUnregisteredAgent: a typo'd provider refuses at
// authoring time -- naming the provider and the registered choices -- and authors
// nothing, instead of authoring cleanly and refusing only at the phone's confirm as
// a generic policy code.
func TestR5Round2_PresetsAddRefusesAnUnregisteredAgent(t *testing.T) {
	stateDir := t.TempDir()
	root := t.TempDir()

	exit, _, errOut := runPresets(t, stateDir, "add", "--name", "Typo", "--agent", "clade", "--root", root)
	if exit == 0 {
		t.Fatal("presets add with an unregistered agent exited 0; want a refusal at setup time " +
			"(presets.go's own principle: a preset nobody can launch is a setup error surfaced " +
			"at setup time, not at the phone's confirm)")
	}
	if !strings.Contains(errOut, "clade") {
		t.Errorf("refusal %q does not name the offending provider", errOut)
	}
	if !strings.Contains(errOut, "claude") {
		t.Errorf("refusal %q does not name the registered providers the operator can pick from", errOut)
	}
	presets, err := daemon.LoadLaunchPresets(stateDir)
	if err != nil {
		t.Fatalf("LoadLaunchPresets: %v", err)
	}
	if len(presets) != 0 {
		t.Errorf("custody holds %d presets after a refused add, want 0", len(presets))
	}
}

// TestR5Round2_PresetsUsageNamesTheLifecycleVerbs: remove and edit are discoverable
// -- a withdrawal verb nobody can find is a setup UX that still cannot produce
// unknown_preset.
func TestR5Round2_PresetsUsageNamesTheLifecycleVerbs(t *testing.T) {
	_, out, errOut := runPresets(t, t.TempDir())
	usage := out + errOut
	for _, verb := range []string{"remove", "edit"} {
		if !strings.Contains(usage, verb) {
			t.Errorf("presets usage does not name %q:\n%s", verb, usage)
		}
	}
}

// mintedPresetID extracts the minted preset- id from add's stdout.
func mintedPresetID(t *testing.T, out string) string {
	t.Helper()
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "preset-") {
			return f
		}
	}
	t.Fatalf("no minted preset- id in add output %q", out)
	return ""
}
