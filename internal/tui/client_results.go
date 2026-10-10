package tui

import tea "charm.land/bubbletea/v2"

// Mutations may have landed before a disconnect; their old replies cannot replay UI effects.
func clientResultGeneration(msg tea.Msg) (uint64, bool) {
	switch m := msg.(type) {
	case launchResultMsg:
		return m.generation, true
	case deleteDoneMsg:
		return m.generation, true
	case killDoneMsg:
		return m.generation, true
	case renameDoneMsg:
		return m.generation, true
	case tagDoneMsg:
		return m.generation, true
	case handoffDoneMsg:
		return m.generation, true
	}
	return 0, false
}
func stampClientResults(cmd tea.Cmd, generation uint64) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		switch m := msg.(type) {
		case tea.BatchMsg:
			for i, c := range m {
				m[i] = stampClientResults(c, generation)
			}
			return m
		case launchResultMsg:
			m.generation = generation
			return m
		case deleteDoneMsg:
			m.generation = generation
			return m
		case killDoneMsg:
			m.generation = generation
			return m
		case renameDoneMsg:
			m.generation = generation
			return m
		case tagDoneMsg:
			m.generation = generation
			return m
		case handoffDoneMsg:
			m.generation = generation
			return m
		}
		return msg
	}
}
