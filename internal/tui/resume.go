package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nathandela/swarm/internal/protocol"
)

// resumeModel pins the source by identity while the user chooses a new checkout.
// Its path field is the same selector used by the new-session form; Enter confirms it.
type resumeModel struct {
	source    protocol.SessionView
	directory launchModel
	reason    string
	busy      bool
}

type resumeResultMsg struct {
	source protocol.SessionView
	result launchResultMsg
}

func (m rootModel) beginResume(s protocol.SessionView) (tea.Model, tea.Cmd) {
	if !sessionAllowsSuccessor(s) {
		return m, nil
	}
	directory := expandTilde(strings.TrimSpace(s.Cwd))
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return m.openResume(s, ""), nil
	}
	return m, resumeCmd(m.client, s, m.width, m.height, false)
}

func (m rootModel) openResume(s protocol.SessionView, errMsg string) rootModel {
	directory := launchModel{width: m.width}
	m.resume = resumeModel{
		source:    s,
		directory: directory,
		reason:    errMsg,
	}
	path := expandTilde(strings.TrimSpace(s.Cwd))
	if path == "" {
		path = userHome()
	}
	path, _ = filepath.Abs(path)
	for {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	m.resume.directory.cwd.set(path)
	m.resume.directory.refreshDirCompletion()
	m.screen = screenResume
	return m
}

func (m rootModel) updateResume(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.resume.busy && k.Code != tea.KeyEsc {
		return m, nil
	}
	switch k.Code {
	case tea.KeyEsc:
		m.resume = resumeModel{}
		cmd := m.enterGeneral()
		return m, cmd
	case tea.KeyEnter:
		return m.submitResume()
	default:
		m.resume.directory.updateDirectory(k)
	}
	return m, nil
}

func (m *resumeModel) pasteDirectory(path string) {
	m.directory.paste(path)
}

func (m rootModel) submitResume() (tea.Model, tea.Cmd) {
	directory := &m.resume.directory
	cwd := expandTilde(strings.TrimSpace(directory.cwd.text))
	if cwd == "" {
		directory.errMsg = "directory is required"
		return m, nil
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		directory.errMsg = "cannot resolve directory: " + err.Error()
		return m, nil
	}
	info, err := os.Stat(cwd)
	if err != nil {
		directory.errMsg = "cannot access directory " + cwd + ": " + err.Error()
		return m, nil
	}
	if !info.IsDir() {
		directory.errMsg = cwd + " exists but is not a directory"
		return m, nil
	}
	source := m.resume.source
	source.Cwd = cwd
	m.resume.busy = true
	directory.errMsg = ""
	return m, resumeCmd(m.client, source, m.width, m.height, true)
}

func (m resumeModel) view() string {
	var b strings.Builder
	prompt := fmt.Sprintf("Session %q cannot be resumed because its working directory is gone.\nSelect a new one to relocate it.", displayName(m.source))
	if m.reason != "" {
		prompt = fmt.Sprintf("Session %q cannot be resumed.\nSelect a new working directory to relocate it.", displayName(m.source))
	}
	width := m.directory.width - 4
	if width <= 0 {
		width = 76
	}
	b.WriteString("  " + strings.ReplaceAll(ansi.Wrap(prompt, width, ""), "\n", "\n  ") + "\n\n")
	b.WriteString(m.directory.directoryView())
	if m.reason != "" {
		b.WriteString("\n  " + styleError.Render(m.reason) + "\n")
	}
	if m.directory.errMsg != "" {
		b.WriteString("\n  " + styleError.Render(m.directory.errMsg) + "\n")
	}
	return b.String()
}

func (m resumeModel) hint() string {
	if m.busy {
		return "resuming..."
	}
	return m.directory.directoryEditHint() + " · enter relocate · esc cancel"
}
