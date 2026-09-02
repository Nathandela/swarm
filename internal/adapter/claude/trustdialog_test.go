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
	m := declaredVersionRE.FindStringSubmatch(string(doc))
	if m == nil {
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
		if !strings.Contains(e.Name(), m[1]) {
			t.Errorf("fixture %s does not carry the declared CLI version %s", e.Name(), m[1])
		}
	}
	if fixtures == 0 {
		t.Error("no recorded trust-gate grid found")
	}
}
