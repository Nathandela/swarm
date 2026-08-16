package main

// FAILING-FIRST (TDD RED, GG-5) for Wave R5 deliverable 1's terminal half (bead
// agents-tracker-hggx.6, playbook :215-218): `swarm remote presets` -- the SETUP UX by
// which the MACHINE authors launch presets. The terminal authors; the phone only ever
// selects and confirms. These tests drive the same runRemote seam every other remote
// verb is tested through and use NO undefined Go symbols, so they fail BEHAVIORALLY
// today: runRemote answers `unknown command "presets"` (exit 2) to every one of them.
//
// The verb contract:
//
//   swarm remote presets add --name <display> --agent <provider> --root <dir>
//       mints a preset with a machine-generated stable opaque id, the root stored as
//       its CANONICAL (symlink-resolved) real path, and prints the id and the content
//       revision. A nonexistent root refuses -- authoring a preset nobody can launch
//       is a setup error surfaced at setup time, not at the phone's confirm.
//   swarm remote presets list
//       prints every authored preset -- id, display name, provider, canonical root,
//       revision -- and an EXPLICIT empty-state line when none exist ("no presets"),
//       because a silent empty list is indistinguishable from a broken read (the D3
//       lesson applied to the terminal side).
//
// Durability is asserted the honest way: a SECOND process-shaped invocation (a fresh
// runRemote call over the same state dir) sees what the first authored.

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
)

// runPresets invokes `swarm remote presets <args...>` against stateDir.
func runPresets(t *testing.T, stateDir string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(daemon.EnvStateDir, stateDir)
	var stdout, stderr bytes.Buffer
	exit := runRemote(append([]string{"presets"}, args...), &stdout, &stderr)
	return exit, stdout.String(), stderr.String()
}

// TestR5PresetsCLI_UsageNamesTheVerb: the `swarm remote` usage text names presets --
// an authoring verb nobody can discover is a setup UX that does not exist.
func TestR5PresetsCLI_UsageNamesTheVerb(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_ = runRemote(nil, &stdout, &stderr)
	if !strings.Contains(stderr.String()+stdout.String(), "presets") {
		t.Errorf("`swarm remote` usage does not mention the presets verb:\n%s%s", stderr.String(), stdout.String())
	}
}

// TestR5PresetsCLI_AddThenListRoundTrip: authoring mints a stable opaque id plus a
// revision, and a FRESH invocation lists the preset back with all five facts.
func TestR5PresetsCLI_AddThenListRoundTrip(t *testing.T) {
	stateDir := t.TempDir()
	root := t.TempDir()

	exit, out, errOut := runPresets(t, stateDir, "add", "--name", "API repo", "--agent", "claude", "--root", root)
	if exit != 0 {
		t.Fatalf("presets add exit = %d, want 0; stderr=%q", exit, errOut)
	}
	if !strings.Contains(out, "preset-") {
		t.Errorf("presets add stdout %q does not print a minted preset- id; the phone selects by "+
			"this opaque id and the operator needs to see it exist", out)
	}
	// The revision is the staleness coordinate the phone will echo; authoring must
	// surface it so an operator can correlate a stale_preset refusal with an edit.
	if !regexp.MustCompile(`(?i)revision\b`).MatchString(out) {
		t.Errorf("presets add stdout %q does not name the content revision", out)
	}

	exit, out, errOut = runPresets(t, stateDir, "list")
	if exit != 0 {
		t.Fatalf("presets list exit = %d, want 0; stderr=%q", exit, errOut)
	}
	resolvedRoot, rerr := filepath.EvalSymlinks(root)
	if rerr != nil {
		t.Fatalf("EvalSymlinks(%s): %v", root, rerr)
	}
	for _, want := range []string{"API repo", "claude", resolvedRoot} {
		if !strings.Contains(out, want) {
			t.Errorf("presets list output does not carry %q:\n%s", want, out)
		}
	}
}

// TestR5PresetsCLI_AddRefusesNonexistentRoot: a root that does not exist refuses at
// authoring time, naming the path, and authors nothing.
func TestR5PresetsCLI_AddRefusesNonexistentRoot(t *testing.T) {
	stateDir := t.TempDir()
	ghost := filepath.Join(t.TempDir(), "never-created")

	exit, _, errOut := runPresets(t, stateDir, "add", "--name", "Ghost", "--agent", "claude", "--root", ghost)
	if exit == 0 {
		t.Fatal("presets add accepted a nonexistent root; want a refusal at authoring time")
	}
	if !strings.Contains(errOut, ghost) {
		t.Errorf("refusal stderr %q does not name the offending root %q", errOut, ghost)
	}

	exit, out, _ := runPresets(t, stateDir, "list")
	if exit != 0 {
		t.Fatalf("presets list after refused add: exit %d", exit)
	}
	if strings.Contains(out, "Ghost") {
		t.Errorf("a refused add still authored a preset:\n%s", out)
	}
}

// TestR5PresetsCLI_AddStoresTheCanonicalRoot: a symlinked root is stored RESOLVED --
// the canonical real path is what the policy checks and what the shim receives (D8's
// no check-on-resolved/use-on-original gap), so it is also what the operator sees.
func TestR5PresetsCLI_AddStoresTheCanonicalRoot(t *testing.T) {
	stateDir := t.TempDir()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "ws-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", real, err)
	}

	if exit, _, errOut := runPresets(t, stateDir, "add", "--name", "Linked", "--agent", "claude", "--root", link); exit != 0 {
		t.Fatalf("presets add via symlink exit = %d; stderr=%q", exit, errOut)
	}
	_, out, _ := runPresets(t, stateDir, "list")
	if !strings.Contains(out, resolvedReal) {
		t.Errorf("presets list shows no canonical root %q for the symlinked add:\n%s", resolvedReal, out)
	}
}

// TestR5PresetsCLI_ListEmptyStateIsExplicit: zero presets says so, exit 0 -- the
// first-run state is an answer, not an error and not silence.
func TestR5PresetsCLI_ListEmptyStateIsExplicit(t *testing.T) {
	exit, out, errOut := runPresets(t, t.TempDir(), "list")
	if exit != 0 {
		t.Fatalf("presets list on a fresh machine exit = %d, want 0; stderr=%q", exit, errOut)
	}
	if !regexp.MustCompile(`(?i)no presets`).MatchString(out) {
		t.Errorf("empty-state list output %q does not say `no presets` explicitly", out)
	}
}
