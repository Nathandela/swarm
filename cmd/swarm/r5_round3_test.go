package main

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-3 review fix-pack (bead
// agents-tracker-hggx.6), setup-UX half (review MEDIUM): `LaunchPreset.Options` is a
// policy-bearing, revision-bound, wire-visible field (schema.LaunchPresetView
// documents it as a machine-authored fact and the R-POL.4 denylist walks it on the
// preset path), but the shipped `swarm remote presets` exposed no flag that could
// author it -- Options was ALWAYS nil in production, the denylist loop unreachable,
// and the field a documented fiction. The round-3 contract:
//
//   swarm remote presets add ... --option key=value [--option key=value ...]
//       authors the allowlisted option map (repeatable; each entry key=value)
//   swarm remote presets edit <id> --option key=value [...]
//       REPLACES the authored option set (a merge cannot ever remove an entry);
//       edit without any --option leaves the authored set unchanged
//   a malformed --option (no '=', or an empty key) refuses at authoring time
//       naming the entry; `worktree` refuses pointing at the dedicated flag
//       (one authoring spelling per policy bit, or the two silently diverge)

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
)

// TestR5Round3_PresetsAddAuthorsOptions: --option entries land in custody (and
// therefore in the content revision the phone echoes), and the usage names the flag.
func TestR5Round3_PresetsAddAuthorsOptions(t *testing.T) {
	stateDir := t.TempDir()
	root := t.TempDir()

	exit, out, errOut := runPresets(t, stateDir, "add",
		"--name", "API repo", "--agent", "claude", "--root", root,
		"--option", "model=opus", "--option", "permission-mode=plan")
	if exit != 0 {
		t.Fatalf("presets add --option exit = %d, want 0; stderr=%q", exit, errOut)
	}
	id := mintedPresetID(t, out)

	presets, err := daemon.LoadLaunchPresets(stateDir)
	if err != nil || len(presets) != 1 {
		t.Fatalf("LoadLaunchPresets = (%v, %v), want the one authored preset", presets, err)
	}
	p := presets[0]
	if p.ID != id {
		t.Fatalf("custody id = %q, want %q", p.ID, id)
	}
	if p.Options["model"] != "opus" || p.Options["permission-mode"] != "plan" {
		t.Errorf("authored options = %v, want model=opus and permission-mode=plan: without an "+
			"authoring flag the Options field is nil in ALL production custody and the preset-path "+
			"R-POL.4 denylist is unreachable", p.Options)
	}

	// The options are policy content: authoring them must move the revision.
	bare := p
	bare.Options = nil
	if daemon.PresetRevision(p) == daemon.PresetRevision(bare) {
		t.Error("options did not change the preset revision; an option edit would then not " +
			"produce stale_preset on an already-confirmed phone")
	}

	exit, _, errOut = runPresets(t, stateDir)
	if exit == 0 || !strings.Contains(errOut, "--option") {
		t.Errorf("presets usage does not name --option (exit %d, stderr=%q); an unnameable "+
			"authoring flag is undiscoverable setup UX", exit, errOut)
	}
}

// TestR5Round3_PresetsEditReplacesOptionsAndKeepsThemWithoutTheFlag: edit --option
// REPLACES the set (so an entry can be removed by re-authoring without it); an edit
// that passes no --option keeps the authored options untouched.
func TestR5Round3_PresetsEditReplacesOptionsAndKeepsThemWithoutTheFlag(t *testing.T) {
	stateDir := t.TempDir()
	root := t.TempDir()

	exit, out, errOut := runPresets(t, stateDir, "add",
		"--name", "API repo", "--agent", "claude", "--root", root,
		"--option", "model=opus", "--option", "permission-mode=plan")
	if exit != 0 {
		t.Fatalf("presets add exit = %d; stderr=%q", exit, errOut)
	}
	id := mintedPresetID(t, out)

	exit, _, errOut = runPresets(t, stateDir, "edit", id, "--name", "API repo (fast)")
	if exit != 0 {
		t.Fatalf("presets edit (no --option) exit = %d; stderr=%q", exit, errOut)
	}
	presets, err := daemon.LoadLaunchPresets(stateDir)
	if err != nil || len(presets) != 1 {
		t.Fatalf("LoadLaunchPresets = (%v, %v)", presets, err)
	}
	if presets[0].Options["model"] != "opus" || presets[0].Options["permission-mode"] != "plan" {
		t.Errorf("edit without --option changed the authored options to %v; a flag left off keeps "+
			"the authored value (the verb's own stated rule)", presets[0].Options)
	}

	exit, _, errOut = runPresets(t, stateDir, "edit", id, "--option", "model=sonnet")
	if exit != 0 {
		t.Fatalf("presets edit --option exit = %d; stderr=%q", exit, errOut)
	}
	presets, err = daemon.LoadLaunchPresets(stateDir)
	if err != nil || len(presets) != 1 {
		t.Fatalf("LoadLaunchPresets = (%v, %v)", presets, err)
	}
	got := presets[0].Options
	if got["model"] != "sonnet" {
		t.Errorf("edited options = %v, want model=sonnet", got)
	}
	if _, still := got["permission-mode"]; still {
		t.Errorf("edit --option merged instead of replacing (%v); a merge can never REMOVE an "+
			"authored option, so a policy entry would be un-withdrawable", got)
	}
}

// TestR5Round3_PresetsMalformedOptionRefusesAtAuthoringTime: no '=', empty key, and
// the reserved `worktree` key each refuse naming the offending entry, and author
// nothing -- the file's own setup-time principle applied to its new flag.
func TestR5Round3_PresetsMalformedOptionRefusesAtAuthoringTime(t *testing.T) {
	stateDir := t.TempDir()
	root := t.TempDir()

	for _, bad := range []string{"modelopus", "=opus", "worktree=true"} {
		exit, _, errOut := runPresets(t, stateDir, "add",
			"--name", "API repo", "--agent", "claude", "--root", root, "--option", bad)
		if exit == 0 {
			t.Errorf("presets add --option %q exited 0; want a refusal at authoring time", bad)
		}
		if !strings.Contains(errOut, strings.SplitN(bad, "=", 2)[0]) && !strings.Contains(errOut, bad) {
			t.Errorf("refusal for --option %q does not name the offending entry: %q", bad, errOut)
		}
		presets, err := daemon.LoadLaunchPresets(stateDir)
		if err != nil {
			t.Fatalf("LoadLaunchPresets: %v", err)
		}
		if len(presets) != 0 {
			t.Fatalf("a refused add still authored %d preset(s)", len(presets))
		}
	}
}
