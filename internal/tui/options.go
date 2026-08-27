package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// options.go is the session-board options window: the one place the board's
// grouping and ordering are chosen. `o` on the general view opens it (owner
// decision 2026-08-27: one window with arrow navigation, not one key per
// setting). It renders like the new-session form, applies on Enter and
// discards on Esc. The choice lives with the running client only.

// optionsModel is the open form: the focused row and the pending choices.
type optionsModel struct {
	focus    int // 0 = group, 1 = order
	grouping groupingMode
	ordering orderingMode
}

// optionsHint is the constant footer: both rows are arrow pickers.
const optionsHint = "←→ change · tab/↑↓ next · enter apply · esc cancel"

// updateOptions handles a keypress while the options window is open.
func (m rootModel) updateOptions(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	o := &m.options
	switch k.Code {
	case tea.KeyEsc:
		return m, m.enterGeneral()
	case tea.KeyEnter:
		m.general.setLayout(o.grouping, o.ordering)
		return m, m.enterGeneral()
	case tea.KeyUp, tea.KeyDown, tea.KeyTab:
		// ponytail: two rows, so either arrow lands on the other one; a wrapping
		// moveFocus when a third row arrives.
		o.focus = 1 - o.focus
	case tea.KeyLeft:
		o.cycle(-1)
	case tea.KeyRight:
		o.cycle(1)
	}
	return m, nil
}

// cycle steps the focused picker, wrapping at both ends.
func (o *optionsModel) cycle(step int) {
	if o.focus == 0 {
		o.grouping = groupingMode(wrapIndex(int(o.grouping)+step, len(groupingLabels)))
		return
	}
	o.ordering = orderingMode(wrapIndex(int(o.ordering)+step, len(orderingLabels)))
}

func wrapIndex(i, n int) int { return ((i % n) + n) % n }

// view renders the form in the new-session form's idiom: title, focus bar, and
// one arrow picker per row. width is the terminal width (0 = not yet known); each
// picker gets the same value budget as the agent picker so a narrow terminal can
// never wrap a row and push the status bar off the board.
func (o optionsModel) view(width int) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("swarm") + styleDim.Render(" · options") + "\n\n")
	b.WriteString(fieldLine("group", picker(groupingLabels[:], int(o.grouping), width-agentRowIndent), o.focus == 0))
	b.WriteString(fieldLine("order", picker(orderingLabels[:], int(o.ordering), width-agentRowIndent), o.focus == 1))
	return b.String()
}

// picker renders a choice row the way the agent picker does: the selection as an
// amber filled dot, the rest as hollow dots, flanked by the cycle-arrow affordances.
// When the full row exceeds budget (> 0), it degrades to the handoff form's compact
// "◂ selected ▸" so the selection always stays visible; a plain clamp would clip it.
func picker(labels []string, sel, budget int) string {
	parts := make([]string, len(labels))
	for i, l := range labels {
		if i == sel {
			parts[i] = styleAmber.Render("● " + l)
		} else {
			parts[i] = "○ " + l
		}
	}
	row := styleDim.Render("◂ ") + strings.Join(parts, "   ") + styleDim.Render(" ▸")
	if budget > 0 && lipgloss.Width(row) > budget {
		row = styleDim.Render("◂ ") + styleAmber.Render(labels[sel]) + styleDim.Render(" ▸")
	}
	return row
}
