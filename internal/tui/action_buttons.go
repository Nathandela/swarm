package tui

import (
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type actionButton struct {
	label, key  string
	x, y, width int
}

func (b actionButton) text() string {
	shortcut := b.key
	switch shortcut {
	case "enter":
		shortcut = "⏎"
	case "x":
		shortcut = "ctrl+x"
	case "back":
		shortcut = "esc"
	case "up":
		shortcut = "↑"
	case "down":
		shortcut = "↓"
	}
	return "[" + b.label + " (" + shortcut + ")]"
}

func sessionButtons(s protocol.SessionView) []actionButton {
	buttons := []actionButton{{label: "Rename", key: "e"}, {label: "Tag", key: "t"}}
	if s.Status.Process != status.ProcessRunning {
		buttons = append(buttons, actionButton{label: "Resume", key: "r"})
	}
	buttons = append(buttons, actionButton{label: "Handoff", key: "h"})
	label := "Delete"
	if s.Status.Process == status.ProcessRunning {
		label = "Kill"
	}
	return append(buttons, actionButton{label: label, key: "x"})
}

// The same cell positions paint buttons and resolve pointer targets.
func (m rootModel) boardButtons() []actionButton {
	buttons := []actionButton{{label: "New", key: "n"}}
	if label := m.boardOpenLabel(); label != "—" {
		key := "enter"
		if s, ok := m.general.selected(); ok && m.general.focusVisible() && s.Status.Process != status.ProcessRunning {
			label, key = "Resume", "r"
		}
		buttons = append(buttons, actionButton{label: label, key: key})
	}
	buttons = append(buttons, actionButton{label: "More", key: "m"})
	width := 0
	for _, b := range buttons {
		width += ansi.StringWidth(b.text()) + 1
	}
	if m.width > 0 && width-1 > m.width {
		buttons = buttons[1:]
	}
	if s, ok := m.general.selected(); ok && m.general.focusVisible() {
		for _, b := range sessionButtons(s) {
			if b.key != "r" {
				buttons = append(buttons, b)
			}
		}
	}
	buttons = append(buttons, actionButton{label: "Options", key: "o"}, actionButton{label: "Attention", key: "g"}, actionButton{label: "Quit", key: "esc"})
	x := 0
	for i := range buttons {
		b := &buttons[i]
		b.width = ansi.StringWidth(b.text())
		if m.width > 0 && x+b.width > m.width {
			return buttons[:i]
		}
		b.x, b.y = x, m.height-1
		x += b.width + 1
	}
	return buttons
}

func (m rootModel) moreButtons() []actionButton {
	buttons := []actionButton{{label: "Back", key: "back"}, {label: "Up", key: "up"}, {label: "Down", key: "down"}}
	x, y := 0, 0
	for i := range buttons {
		b := &buttons[i]
		b.width = ansi.StringWidth(b.text())
		if m.width > 0 && x+b.width > m.width {
			x = 0
			y++
		}
		b.x, b.y = x, y
		x += b.width + 1
	}
	return buttons
}

func actionKey(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "x":
		return tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	case "esc", "back":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	return tea.KeyPressMsg{Text: key}
}
