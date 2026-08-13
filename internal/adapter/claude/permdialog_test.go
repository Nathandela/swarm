package claude

// M1.1 (bead agents-tracker-dwwv.2.1): the permission-dialog recognizer, driven by
// RECORDED grids of a real claude 2.1.231 (testdata/permdialog, captured by
// internal/smoke/permdialog_test.go under the realcli gate).
//
// M1.2 injects the dialog's own keys into the PTY when the phone answers an
// approval, and gates that injection on the live grid STILL showing the dialog.
// This is that gate's reader, so its contract is asymmetric on purpose: matching a
// dialog that is not there types keystrokes into the composer, while failing to
// match one that is there merely refuses the tap. Hence every negative fixture
// here, and hence the destructive controls: a grid the recognizer does not
// POSITIVELY match is a refusal, never a guess.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

const permDialogDir = "testdata/permdialog"

// loadPermGrid decodes one recorded snapshot fixture into the *vt.Snap projection
// the daemon hands a reader at runtime.
func loadPermGrid(t *testing.T, name string) *vt.Snap {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(permDialogDir, name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	snap, err := vt.DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("decoding fixture %s: %v", name, err)
	}
	return snap
}

// rewriteRow replaces the first row whose trimmed text equals old with a single
// run holding new. It mutates ONLY the in-memory snapshot the caller just decoded
// -- the destructive controls never touch a file.
func rewriteRow(t *testing.T, snap *vt.Snap, old, new string) *vt.Snap {
	t.Helper()
	for i, line := range snap.Lines {
		var b strings.Builder
		for _, r := range line.Runs {
			b.WriteString(r.Text)
		}
		if strings.TrimSpace(b.String()) != old {
			continue
		}
		snap.Lines[i] = vt.Line{Runs: []vt.Run{{Text: " " + new, Width: len(new) + 1}}}
		return snap
	}
	t.Fatalf("fixture has no row %q to rewrite; the control is blind", old)
	return nil
}

// TestRecognizePermissionDialog_BashVariant — the recorded Bash approval is
// recognized, and its key map is the one the live CLI actually obeyed
// (docs/verification/mirror-m1.md: bare "1" allowed, bare "3" denied).
func TestRecognizePermissionDialog_BashVariant(t *testing.T) {
	got, ok := RecognizePermissionDialog(loadPermGrid(t, "bash-approval-2.1.231.snap.json"))
	if !ok {
		t.Fatalf("recognizer refused the recorded Bash approval grid; M1.2 could never apply a phone answer")
	}
	if got.Variant != VariantBash {
		t.Errorf("Variant = %q, want %q", got.Variant, VariantBash)
	}
	if got.AllowKeys != "1" {
		t.Errorf("AllowKeys = %q, want %q (the digit of the rendered \"1. Yes\" row)", got.AllowKeys, "1")
	}
	if got.DenyKeys != "3" {
		t.Errorf("DenyKeys = %q, want %q (the digit of the rendered \"3. No\" row)", got.DenyKeys, "3")
	}
}

// TestRecognizePermissionDialog_EditVariant — the Edit approval renders a diff and
// a DIFFERENT question row, and is recognized as its own variant.
func TestRecognizePermissionDialog_EditVariant(t *testing.T) {
	got, ok := RecognizePermissionDialog(loadPermGrid(t, "edit-approval-2.1.231.snap.json"))
	if !ok {
		t.Fatalf("recognizer refused the recorded Edit approval grid")
	}
	if got.Variant != VariantEdit {
		t.Errorf("Variant = %q, want %q", got.Variant, VariantEdit)
	}
	if got.AllowKeys != "1" || got.DenyKeys != "3" {
		t.Errorf("key map = allow %q / deny %q, want 1 / 3", got.AllowKeys, got.DenyKeys)
	}
}

// TestRecognizePermissionDialog_RefusesEveryNonDialogGrid — the recorded screens
// that are NOT a tool approval, including the folder-trust dialog: modal, numbered
// and ❯-preselected, and answering it with an approval key map would answer the
// wrong question.
func TestRecognizePermissionDialog_RefusesEveryNonDialogGrid(t *testing.T) {
	for _, name := range []string{
		"neg-composer-idle-2.1.231.snap.json",
		"neg-working-2.1.231.snap.json",
		"neg-trust-dialog-2.1.231.snap.json",
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := RecognizePermissionDialog(loadPermGrid(t, name)); ok {
				t.Errorf("recognized %+v on a grid with no tool approval on it; a phone tap would type into it", got)
			}
		})
	}
	if _, ok := RecognizePermissionDialog(nil); ok {
		t.Error("recognized a permission dialog on a nil grid")
	}
	if _, ok := RecognizePermissionDialog(&vt.Snap{}); ok {
		t.Error("recognized a permission dialog on an empty grid")
	}
}

// TestRecognizePermissionDialog_RefusesAnUnknownLayout — the destructive controls,
// in memory only. Each mutation leaves a dialog that is still obviously A dialog
// but no longer one this recognizer has a recorded key map for; every one must be
// refused rather than answered with the nearest-looking keys.
func TestRecognizePermissionDialog_RefusesAnUnknownLayout(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
	}{
		{"unknown tool title", "Bash command", "Frobnicate command"},
		{"no allow row", "❯ 1. Yes", "❯ 1. Sure thing"},
		{"no deny row", "3. No", "3. Nope, and tell Claude why"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap := rewriteRow(t, loadPermGrid(t, "bash-approval-2.1.231.snap.json"), c.old, c.new)
			if got, ok := RecognizePermissionDialog(snap); ok {
				t.Errorf("recognized %+v after %q became %q; an unrecognized layout must be a refusal, not a guess",
					got, c.old, c.new)
			}
		})
	}
}

// TestRecognizedDialog_IsPermissionToTheStatusEngineToo — the recognizer is the
// STRICTER reader of the same screen the status engine already classifies, not a
// second opinion about it. Every grid it calls a dialog must ALSO be
// interaction=permission to the engine's claude grid signature (ADR-007), so
// M1.2's gate can never disagree with the status the phone is looking at.
func TestRecognizedDialog_IsPermissionToTheStatusEngineToo(t *testing.T) {
	for _, name := range []string{
		"bash-approval-2.1.231.snap.json",
		"edit-approval-2.1.231.snap.json",
	} {
		t.Run(name, func(t *testing.T) {
			grid := loadPermGrid(t, name)
			if _, ok := RecognizePermissionDialog(grid); !ok {
				t.Fatalf("recognizer refused %s; the subset claim is untestable", name)
			}
			var got status.Status
			eng := engine.New(engine.Config{
				StalenessThreshold: 0,
				Emit:               func(_ string, s status.Status) { got = s },
			})
			eng.RegisterSession("s1", "tok", 0, newAdapter().SignalSources())
			eng.OnOutput("s1", grid)
			if got.Interaction != status.InteractionPermission {
				t.Errorf("engine read interaction=%q on a grid the recognizer calls a dialog; want %q",
					got.Interaction, status.InteractionPermission)
			}
		})
	}
}

// declaredVersionRE reads the CLI version testdata/permdialog/README.md declares.
var declaredVersionRE = regexp.MustCompile(`(?m)^CLI version: ([0-9][0-9.]*)`)

// TestPermissionDialogFixtures_AreVersionStamped — the key map is a per-version
// fact, so a fixture that stops naming its version is a fixture nobody can date.
// Re-recording against a newer CLI must move the README's declaration AND the
// filenames together, or this fails.
func TestPermissionDialogFixtures_AreVersionStamped(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(permDialogDir, "README.md"))
	if err != nil {
		t.Fatalf("reading fixture README: %v", err)
	}
	m := declaredVersionRE.FindStringSubmatch(string(doc))
	if m == nil {
		t.Fatalf("README.md declares no \"CLI version: x.y.z\"; the fixtures are undated")
	}
	entries, err := os.ReadDir(permDialogDir)
	if err != nil {
		t.Fatalf("reading fixture dir: %v", err)
	}
	fixtures := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".snap.json") {
			continue
		}
		fixtures++
		if !strings.Contains(e.Name(), m[1]) {
			t.Errorf("fixture %s does not carry the declared CLI version %s", e.Name(), m[1])
		}
	}
	if fixtures < 5 {
		t.Errorf("found %d recorded grids; want at least the 2 positives and 3 negatives M1.1 recorded", fixtures)
	}
}
