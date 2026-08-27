package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func replaceLaunchDirectory(t *testing.T, m tea.Model, path string) tea.Model {
	t.Helper()
	for launchOf(m).cwd != "" {
		m = send(m, keyBackspace)
	}
	return sendType(m, path)
}

func TestLaunch_MissingDirectorySuggestsCloseSiblingBeforeCreate(t *testing.T) {
	parent := t.TempDir()
	want := filepath.Join(parent, "swarm")
	if err := os.Mkdir(want, 0o755); err != nil {
		t.Fatalf("mkdir suggestion: %v", err)
	}
	typed := filepath.Join(parent, "swrma")
	f := newFakeClient()
	m := replaceLaunchDirectory(t, openLaunch(t, f), typed)

	m = send(m, keyEnter)

	lm := launchOf(m)
	if got := strings.Join(lm.dirCands, ","); got != "swarm" {
		t.Fatalf("fuzzy candidates = %q, want close sibling %q", got, "swarm")
	}
	if !strings.Contains(view(m), "did you mean") || !strings.Contains(view(m), "swarm") {
		t.Fatalf("missing path must render its fuzzy suggestion:\n%s", view(m))
	}
	if _, err := os.Stat(typed); !os.IsNotExist(err) {
		t.Fatalf("first submit created %q; it must only arm confirmation (stat err=%v)", typed, err)
	}
	if got := len(f.launchReqs()); got != 0 {
		t.Fatalf("first submit launched %d sessions, want 0", got)
	}
}

func TestLaunch_FuzzyArrowChoosesExistingDirectory(t *testing.T) {
	parent := t.TempDir()
	want := filepath.Join(parent, "swarm")
	if err := os.Mkdir(want, 0o755); err != nil {
		t.Fatalf("mkdir suggestion: %v", err)
	}
	f := newFakeClient()
	m := replaceLaunchDirectory(t, openLaunch(t, f), filepath.Join(parent, "swrma"))
	m = send(m, keyEnter)

	m = send(m, keyRight)
	if got := launchOf(m).cwd; got != want {
		t.Fatalf("Right selected %q, want suggested path %q", got, want)
	}
	_, cmd := m.Update(keyEnter)
	execCmd(cmd)
	if reqs := f.launchReqs(); len(reqs) != 1 || reqs[0].Cwd != want {
		t.Fatalf("selected suggestion launch requests = %+v, want one launch in %q", reqs, want)
	}
}

func TestLaunch_SecondEnterCreatesUnchangedDirectoryAndLaunches(t *testing.T) {
	typed := filepath.Join(t.TempDir(), "new-project")
	f := newFakeClient()
	m := replaceLaunchDirectory(t, openLaunch(t, f), typed)

	m = send(m, keyEnter)
	if !strings.Contains(view(m), "enter again to create") {
		t.Fatalf("first submit must ask for explicit creation confirmation:\n%s", view(m))
	}
	_, cmd := m.Update(keyEnter)
	execCmd(cmd)

	info, err := os.Stat(typed)
	if err != nil || !info.IsDir() {
		t.Fatalf("confirmed path was not created as a directory: info=%v err=%v", info, err)
	}
	if reqs := f.launchReqs(); len(reqs) != 1 || reqs[0].Cwd != typed {
		t.Fatalf("confirmed creation launch requests = %+v, want one launch in %q", reqs, typed)
	}
}

func TestLaunch_DirectoryEditInvalidatesCreateConfirmation(t *testing.T) {
	base := filepath.Join(t.TempDir(), "new-project")
	f := newFakeClient()
	m := replaceLaunchDirectory(t, openLaunch(t, f), base)
	m = send(m, keyEnter)

	m = sendType(m, "-edited")
	send(m, keyEnter)

	edited := base + "-edited"
	if _, err := os.Stat(edited); !os.IsNotExist(err) {
		t.Fatalf("one submit after an edit created %q; edit must invalidate confirmation (stat err=%v)", edited, err)
	}
	if got := len(f.launchReqs()); got != 0 {
		t.Fatalf("one submit after an edit launched %d sessions, want 0", got)
	}
}

func TestFuzzySiblingDirectories_RanksAdjacentTranspositionAsOneTypo(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"storm", "swarm"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}

	got := fuzzySiblingDirectories(parent, "sawrm")
	if len(got) == 0 || got[0] != "swarm" {
		t.Fatalf("fuzzy ranking for adjacent transposition = %v, want swarm first", got)
	}
}
