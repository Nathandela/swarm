package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestResumeMissingDirectoryOffersRelocation(t *testing.T) {
	for _, process := range []status.Process{status.ProcessExited, status.ProcessLost} {
		t.Run(string(process), func(t *testing.T) {
			missing := filepath.Join(t.TempDir(), "removed-checkout")
			source := sCompleted("endpoint/source", "codex", missing, "", time.Hour)
			source.Status.Process = process
			source.Name = "Continue my work"
			f := newFakeClient(source)
			f.launchID = "endpoint/relocated"
			runner := &recordingRunner{}
			m := New(f, detectMixed(), WithAttachRunner(runner.run))
			m = send(m, tea.WindowSizeMsg{Width: testCols, Height: testRows})
			m, cmd := m.Update(keyRune('r'))
			execCmd(cmd)
			if len(f.launchReqs()) != 0 {
				t.Fatal("resume attempted to launch in the missing directory")
			}
			if v := view(m); !strings.Contains(v, "working directory is gone") || !strings.Contains(v, source.Name) || !strings.Contains(v, "directory") || !strings.Contains(v, "enter relocate") {
				t.Fatalf("missing-directory recovery is not visible:\n%s", v)
			}
			if m.(rootModel).boardTakesMouse() {
				t.Fatal("relocation form must own input")
			}
			// A roster update must not move the relocation onto another session.
			m = send(m, eventMsg{ev: protocol.Event{Session: sCompleted("endpoint/neighbor", "claude", t.TempDir(), "", 0)}})
			destination := filepath.Join(t.TempDir(), "moved repo; $(literal)-y")
			if err := os.Mkdir(destination, 0o755); err != nil {
				t.Fatal(err)
			}
			m = send(m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
			m = send(m, tea.PasteMsg{Content: strings.TrimSuffix(destination, "y")})
			m = send(m, keyRune('y')) // 'y' remains ordinary path text in the shared selector.
			if len(f.launchReqs()) != 0 || m.(rootModel).resume.directory.cwd.text != destination {
				t.Fatal("typing y in the directory field must not confirm relocation")
			}
			m, cmd = m.Update(keyEnter)
			if cmd == nil {
				t.Fatal("valid relocation produced no launch")
			}
			m, attachCmd := m.Update(cmd())
			if m.(rootModel).screen != screenAttach {
				t.Fatal("successful relocation did not enter attach mode")
			}
			execCmd(attachCmd)
			reqs := f.launchReqs()
			if len(reqs) != 1 || reqs[0].Cwd != destination || reqs[0].Options[protocol.OptionResumeFrom] != source.ID || reqs[0].Options["resume_relocate"] != "true" || reqs[0].Name != source.Name {
				t.Fatalf("relocation lost the source or destination: %+v", reqs)
			}
			if calls := runner.recorded(); len(calls) != 1 || calls[0].session.ID != f.launchID {
				t.Fatalf("relocated session was not auto-attached: %+v", calls)
			}
			if _, err := os.Stat(missing); !os.IsNotExist(err) {
				t.Fatal("relocation recreated the old checkout")
			}
		})
	}
}

func TestResumeRelocationRejectsInvalidDirectoryAndCanCancel(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"", filepath.Join(root, "missing"), file} {
		t.Run(directory, func(t *testing.T) {
			f := newFakeClient(sCompleted("endpoint/source", "claude", filepath.Join(root, "gone"), "", time.Hour))
			m := send(newModel(t, f, detectMixed()), keyRune('r'))
			m = send(m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
			m = send(m, tea.PasteMsg{Content: directory})
			for i := 0; i < 2; i++ {
				var cmd tea.Cmd
				m, cmd = m.Update(keyEnter)
				execCmd(cmd)
			}
			if len(f.launchReqs()) != 0 || m.(rootModel).screen == screenGeneral {
				t.Fatal("invalid directory must keep recovery open without launching")
			}
			m, cmd := m.Update(keyEsc)
			if cmdQuits(cmd) || m.(rootModel).screen != screenGeneral || len(f.deleted) != 0 {
				t.Fatal("cancel must return to the board and retain the source")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatal("repeated submit must not create a relocation destination")
	}
}

func TestResumeFailureOffersDirectoryRecovery(t *testing.T) {
	// The visible cwd can exist while the daemon's saved worktree is missing.
	source := sCompleted("endpoint/source", "codex", t.TempDir(), "", time.Hour)
	f := newFakeClient(source)
	f.launchErr = errors.New("resume: source checkout is no longer available")
	m := newModel(t, f, detectMixed())
	m, cmd := m.Update(keyRune('r'))
	m = send(m, cmd())
	if v := view(m); !strings.Contains(v, "relocate") || !strings.Contains(v, "source checkout") {
		t.Fatalf("daemon refusal did not leave an editable recovery form:\n%s", v)
	}
	m = send(m, keyEsc)
	if m.(rootModel).screen != screenGeneral {
		t.Fatal("could not cancel recovery")
	}
}
