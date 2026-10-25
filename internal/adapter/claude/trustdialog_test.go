package claude

// FAILING-FIRST (TDD RED, GG-5) for the folder-trust LAUNCH GATE (bead swarm-1mq).
//
// A claude launched in a directory it has never been trusted in draws its folder-trust
// dialog before anything else. claude 2.1.258 PRESELECTS "No, exit" (2.1.231 preselected
// "1. Yes, I trust this folder"), so the reflexive Enter that used to accept now exits the
// CLI with status 1 and the session dies at birth. The adapter's answer is the keys that
// select "Yes, I trust this folder" and confirm it, READ OFF THE GRID: where the selection
// marker sits decides whether a cursor move precedes the confirm. Every grid that is not
// positively that dialog, with both options on screen and exactly one of them marked, is a
// refusal -- a key returned for the wrong screen is typed into whatever has focus.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

const trustDialogDir = "testdata/trustdialog"

// trustSnap loads one recorded grid by path.
func trustSnap(t *testing.T, path string) *vt.Snap {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	snap, err := vt.DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
	return snap
}

func TestTrustGate_2_1_258_NoPreselected_MovesToYesThenConfirms(t *testing.T) {
	snap := trustSnap(t, filepath.Join(trustDialogDir, "trust-dialog-2.1.258.snap.json"))
	keys, ok := New().LaunchGateKeys(snap)
	if !ok {
		t.Fatal("the recorded 2.1.258 folder-trust dialog was not recognized")
	}
	if keys != "\x1b[B\r" {
		t.Fatalf("keys = %q; want Down then Enter: the marker sits on \"No, exit\" and \"Yes, I trust this folder\" is the row below it", keys)
	}
}

func TestTrustGate_2_1_310_StillRecognizedByBoxTitle(t *testing.T) {
	snap := trustSnap(t, filepath.Join(trustDialogDir, "trust-dialog-2.1.310.snap.json"))
	if snap.Cols != 150 || snap.Rows != 44 {
		t.Fatalf("fixture grid = %dx%d; want captured PTY size 150x44", snap.Cols, snap.Rows)
	}
	keys, ok := New().LaunchGateKeys(snap)
	if !ok || keys != "\x1b[B\r" {
		t.Fatalf("native 2.1.310 dialog: (%q, %v); want Down then Enter", keys, ok)
	}
}

func TestTrustGate_2_1_231_YesPreselected_Confirms(t *testing.T) {
	snap := trustSnap(t, filepath.Join(permDialogDir, "neg-trust-dialog-2.1.231.snap.json"))
	keys, ok := New().LaunchGateKeys(snap)
	if !ok {
		t.Fatal("the recorded 2.1.231 folder-trust dialog was not recognized")
	}
	if keys != "\r" {
		t.Fatalf("keys = %q; want a bare Enter: \"1. Yes, I trust this folder\" is already the marked row", keys)
	}
}

func TestTrustGate_RefusesEveryOtherRecordedGrid(t *testing.T) {
	for _, name := range []string{
		"bash-approval-2.1.231", "edit-approval-2.1.231",
		"neg-composer-idle-2.1.231", "neg-working-2.1.231",
	} {
		snap := trustSnap(t, filepath.Join(permDialogDir, name+".snap.json"))
		if keys, ok := New().LaunchGateKeys(snap); ok || keys != "" {
			t.Errorf("%s: recognized as the folder-trust gate with keys %q; it is not that dialog", name, keys)
		}
	}
	if keys, ok := New().LaunchGateKeys(nil); ok || keys != "" {
		t.Errorf("nil grid: (%q, %v); want a refusal", keys, ok)
	}
	if keys, ok := New().LaunchGateKeys(&vt.Snap{}); ok || keys != "" {
		t.Errorf("empty grid: (%q, %v); want a refusal", keys, ok)
	}
}

func TestTrustGate_RefusesADialogWithoutASelectionMarker(t *testing.T) {
	snap := trustSnap(t, filepath.Join(trustDialogDir, "trust-dialog-2.1.258.snap.json"))
	rewriteRow(t, snap, "❯ No, exit", "  No, exit")
	if keys, ok := New().LaunchGateKeys(snap); ok || keys != "" {
		t.Fatalf("a dialog with no marked option answered (%q, %v); without the marker the cursor's position is a guess", keys, ok)
	}
}

func TestTrustGate_RefusesMalformed2_1_310Dialog(t *testing.T) {
	load := func() *vt.Snap {
		return trustSnap(t, filepath.Join(trustDialogDir, "trust-dialog-2.1.310.snap.json"))
	}
	refuse := func(name string, snap *vt.Snap) {
		t.Helper()
		if keys, ok := New().LaunchGateKeys(snap); ok || keys != "" {
			t.Errorf("%s: answered (%q, %v); want refusal", name, keys, ok)
		}
	}
	t.Run("missing marker", func(t *testing.T) {
		snap := load()
		rewriteRow(t, snap, "❯ No, exit", "No, exit")
		refuse("missing marker", snap)
	})
	t.Run("missing box title", func(t *testing.T) {
		snap := load()
		rewriteRow(t, snap, "Accessing workspace:", "Other workspace:")
		refuse("missing box title", snap)
	})
	t.Run("double marker", func(t *testing.T) {
		snap := load()
		rewriteRow(t, snap, "Yes, I trust this folder", "❯ Yes, I trust this folder")
		refuse("double marker", snap)
	})
	t.Run("nonadjacent options", func(t *testing.T) {
		snap := load()
		rewriteRow(t, snap, "Yes, I trust this folder", "")
		rewriteRow(t, snap, "Enter to confirm · Esc to cancel", "Yes, I trust this folder")
		refuse("nonadjacent options", snap)
	})
	t.Run("unknown option", func(t *testing.T) {
		snap := load()
		rewriteRow(t, snap, "Yes, I trust this folder", "Proceed anyway")
		refuse("unknown option", snap)
	})
	t.Run("unframed title", func(t *testing.T) {
		snap := load()
		for i := range snap.Lines {
			if isBoxRule(rowText(snap.Lines[i])) {
				snap.Lines[i] = vt.Line{}
				refuse("unframed title", snap)
				return
			}
		}
		t.Fatal("native frame rule not found")
	})
	t.Run("quoted box title", func(t *testing.T) {
		snap := load()
		rewriteRow(t, snap, "Accessing workspace:", "Transcript: Accessing workspace:")
		refuse("quoted box title", snap)
	})
}

func TestTrustGate_RefusesWhenTheYesOptionIsNotOnScreen(t *testing.T) {
	snap := trustSnap(t, filepath.Join(trustDialogDir, "trust-dialog-2.1.258.snap.json"))
	rewriteRow(t, snap, "Yes, I trust this folder", "")
	if keys, ok := New().LaunchGateKeys(snap); ok || keys != "" {
		t.Fatalf("a screen without the Yes option answered (%q, %v); there is nothing to select", keys, ok)
	}
}

func TestTrustGate_TheClaudeAdapterIsALaunchGateAnswerer(t *testing.T) {
	if _, ok := adapter.AsLaunchGateAnswerer(New()); !ok {
		t.Fatal("the claude adapter does not implement adapter.LaunchGateAnswerer")
	}
}

// TestTrustDialogFixtures_AreVersionStamped mirrors the permdialog rule: every recorded
// trust-gate grid carries the CLI version its README declares.
func TestTrustDialogFixtures_AreVersionStamped(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(trustDialogDir, "README.md"))
	if err != nil {
		t.Fatalf("reading fixture README: %v", err)
	}
	versions := map[string]bool{}
	for _, m := range declaredVersionRE.FindAllStringSubmatch(string(doc), -1) {
		versions[m[1]] = true
	}
	if len(versions) == 0 {
		t.Fatal("README.md declares no \"CLI version: x.y.z\"; the fixtures are undated")
	}
	entries, err := os.ReadDir(trustDialogDir)
	if err != nil {
		t.Fatalf("reading fixture dir: %v", err)
	}
	fixtures := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".snap.json") {
			continue
		}
		fixtures++
		stamped := false
		for version := range versions {
			stamped = stamped || strings.Contains(e.Name(), version)
		}
		if !stamped {
			t.Errorf("fixture %s does not carry a version declared in README.md (%v)", e.Name(), versions)
		}
	}
	if fixtures == 0 {
		t.Error("no recorded trust-gate grid found")
	}
}
