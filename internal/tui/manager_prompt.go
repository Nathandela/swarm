package tui

import (
	"strings"
	"unicode"

	"github.com/Nathandela/swarm/internal/voice"
)

// WithManagerSettingsDir uses the local supervisor state for manager settings.
func WithManagerSettingsDir(dir string) Option {
	return func(m *rootModel) { m.managerSettingsDir = dir }
}

func (m *rootModel) loadManagerPrompt() {
	if m.managerSettingsDir == "" {
		return
	}
	o := &m.options
	o.managerPromptAvailable = true
	value, err := voice.LoadManagerStartingPrompt(m.managerSettingsDir)
	if err != nil {
		o.managerPromptErr = err.Error()
		return
	}
	o.managerPrompt.set(value)
}

func (m *rootModel) pasteManagerPrompt(text string) {
	m.options.managerPrompt.insert(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ReplaceAll(text, "\r\n", "\n")))
	m.options.managerPromptSaved = false
}
