package tui

import (
	"errors"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/protocol"
)

// options.go is the session-board options window: the one place the board's
// grouping and ordering are chosen. `o` on the general view opens it (owner
// decision 2026-08-27: one window with arrow navigation, not one key per
// setting). It renders like the new-session form, applies on Enter and
// discards on Esc. The choice lives with the running client only.

type contextGuardSettingsClient interface {
	Capabilities() []string
	ContextGuardSettings() (protocol.ContextGuardSettings, error)
	SetContextGuardSettings(expectedRevision uint64, autoCompact protocol.ContextGuardAutoCompact) (protocol.ContextGuardSettings, error)
}

const (
	optionsFocusGroup = iota
	optionsFocusOrder
	optionsFocusAutoCompact
	optionsFocusThreshold
)

// optionsModel is the open form: the focused row and the pending choices.
type optionsModel struct {
	focus    int // 0 = group, 1 = order
	grouping groupingMode
	ordering orderingMode

	contextGuard contextGuardOptions
}

type contextGuardOptions struct {
	available, loaded, loading, saving bool
	generation, revision               uint64
	autoCompact, savedCompact          protocol.ContextGuardAutoCompact
	threshold                          lineEditor
	err                                string
}

type contextGuardSettingsLoadedMsg struct {
	generation uint64
	settings   protocol.ContextGuardSettings
	err        error
}

type contextGuardSettingsSavedMsg struct {
	generation uint64
	settings   protocol.ContextGuardSettings
	err        error
}

func newOptionsModel(grouping groupingMode, ordering orderingMode, c Client, generation uint64) (optionsModel, tea.Cmd) {
	o := optionsModel{grouping: grouping, ordering: ordering}
	settingsClient, ok := c.(contextGuardSettingsClient)
	if !ok || !hasContextGuardSettingsCap(settingsClient.Capabilities()) {
		return o, nil
	}
	o.contextGuard.available = true
	o.contextGuard.loading = true
	o.contextGuard.generation = generation
	return o, loadContextGuardSettingsCmd(settingsClient, generation)
}

func hasContextGuardSettingsCap(caps []string) bool {
	for _, cap := range caps {
		if cap == protocol.CapContextGuardSettings {
			return true
		}
	}
	return false
}

func loadContextGuardSettingsCmd(c contextGuardSettingsClient, generation uint64) tea.Cmd {
	return func() tea.Msg {
		settings, err := c.ContextGuardSettings()
		return contextGuardSettingsLoadedMsg{generation: generation, settings: settings, err: err}
	}
}

func saveContextGuardSettingsCmd(c contextGuardSettingsClient, generation, revision uint64, compact protocol.ContextGuardAutoCompact) tea.Cmd {
	return func() tea.Msg {
		settings, err := c.SetContextGuardSettings(revision, compact)
		return contextGuardSettingsSavedMsg{generation: generation, settings: settings, err: err}
	}
}

// optionsHint is the constant footer: both rows are arrow pickers.
const optionsHint = "←→ change · tab/↑↓ next · enter apply · esc cancel"

func (o optionsModel) hint() string {
	if !o.contextGuard.available || !o.contextGuard.loaded {
		return optionsHint
	}
	return "←→ change · space toggle · type threshold · tab/↑↓ next · enter apply · esc cancel"
}

// updateOptions handles a keypress while the options window is open.
func (m rootModel) updateOptions(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	o := &m.options
	switch k.Code {
	case tea.KeyEsc:
		return m, m.enterGeneral()
	case tea.KeyUp:
		o.focus = wrapIndex(o.focus-1, o.focusCount())
		return m, nil
	case tea.KeyDown, tea.KeyTab:
		o.focus = wrapIndex(o.focus+1, o.focusCount())
		return m, nil
	}
	// A load/save owns the settings snapshot. The owner may still navigate and cancel,
	// but no key may mutate it or begin a second same-revision CAS while it is in flight.
	if o.contextGuard.loading || o.contextGuard.saving {
		return m, nil
	}
	switch k.Code {
	case tea.KeyEnter:
		m.general.setLayout(o.grouping, o.ordering)
		if o.contextGuard.err != "" {
			return m, nil
		}
		if !o.contextGuard.dirty() {
			return m, m.enterGeneral()
		}
		return m.beginContextGuardSave()
	case tea.KeyLeft, tea.KeyRight:
		if o.focus == optionsFocusThreshold && o.contextGuard.loaded {
			if o.contextGuard.threshold.update(k).changed {
				o.contextGuard.err = ""
			}
		} else {
			o.cycle(optionStep(k))
		}
	case tea.KeySpace:
		if o.focus == optionsFocusAutoCompact && o.contextGuard.loaded {
			o.contextGuard.autoCompact.Enabled = !o.contextGuard.autoCompact.Enabled
		}
	default:
		if o.focus == optionsFocusThreshold && o.contextGuard.loaded {
			if o.contextGuard.threshold.update(k).changed {
				o.contextGuard.err = ""
			}
		}
	}
	return m, nil
}

func optionStep(k tea.KeyPressMsg) int {
	if k.Code == tea.KeyLeft {
		return -1
	}
	return 1
}

func (o optionsModel) focusCount() int {
	if o.contextGuard.available && o.contextGuard.loaded {
		return 4
	}
	return 2
}

// cycle steps the focused picker, wrapping at both ends.
func (o *optionsModel) cycle(step int) {
	switch o.focus {
	case optionsFocusGroup:
		o.grouping = groupingMode(wrapIndex(int(o.grouping)+step, len(groupingLabels)))
	case optionsFocusOrder:
		o.ordering = orderingMode(wrapIndex(int(o.ordering)+step, len(orderingLabels)))
	case optionsFocusAutoCompact:
		if o.contextGuard.loaded {
			o.contextGuard.autoCompact.Enabled = !o.contextGuard.autoCompact.Enabled
		}
	}
}

func wrapIndex(i, n int) int { return ((i % n) + n) % n }

func (c contextGuardOptions) dirty() bool {
	return c.loaded && (c.autoCompact != c.savedCompact || c.threshold.text != strconv.Itoa(c.savedCompact.ThresholdPercent))
}

func (m rootModel) beginContextGuardSave() (tea.Model, tea.Cmd) {
	o := &m.options.contextGuard
	threshold, err := strconv.Atoi(o.threshold.text)
	if err != nil || threshold < 40 || threshold > 95 {
		o.err = "threshold must be an integer from 40–95"
		return m, nil
	}
	settingsClient, ok := m.client.(contextGuardSettingsClient)
	if !ok {
		o.err = "context guard settings unavailable"
		return m, nil
	}
	m.optionsGeneration++
	o.generation = m.optionsGeneration
	o.saving = true
	o.err = ""
	compact := o.autoCompact
	compact.ThresholdPercent = threshold
	return m, saveContextGuardSettingsCmd(settingsClient, o.generation, o.revision, compact)
}

func (m rootModel) applyContextGuardSettingsLoaded(msg contextGuardSettingsLoadedMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenOptions || !m.options.contextGuard.available || msg.generation != m.options.contextGuard.generation {
		return m, nil
	}
	o := &m.options.contextGuard
	o.loading = false
	if msg.err != nil {
		o.err = "context guard settings unavailable"
		return m, nil
	}
	o.loaded = true
	o.revision = msg.settings.Revision
	o.autoCompact = msg.settings.AutoCompact
	o.savedCompact = msg.settings.AutoCompact
	o.threshold.set(strconv.Itoa(msg.settings.AutoCompact.ThresholdPercent))
	o.err = ""
	return m, nil
}

func (m rootModel) applyContextGuardSettingsSaved(msg contextGuardSettingsSavedMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenOptions || !m.options.contextGuard.available || msg.generation != m.options.contextGuard.generation {
		return m, nil
	}
	o := &m.options.contextGuard
	o.saving = false
	if msg.err != nil {
		if errors.Is(msg.err, protocol.ErrContextGuardSettingsStaleRevision) {
			o.err = "context guard settings changed elsewhere; reopen options to reload"
		} else {
			o.err = "context guard settings unavailable"
		}
		return m, nil
	}
	o.revision = msg.settings.Revision
	o.autoCompact = msg.settings.AutoCompact
	o.savedCompact = msg.settings.AutoCompact
	o.threshold.set(strconv.Itoa(msg.settings.AutoCompact.ThresholdPercent))
	o.err = ""
	return m, m.enterGeneral()
}

// view renders the form in the new-session form's idiom: title, focus bar, and
// one arrow picker per row. width is the terminal width (0 = not yet known); each
// picker gets the same value budget as the agent picker so a narrow terminal can
// never wrap a row and push the status bar off the board.
func (o optionsModel) view(width int) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("swarm") + styleDim.Render(" · options") + "\n\n")
	b.WriteString(fieldLine("group", picker(groupingLabels[:], int(o.grouping), width-agentRowIndent), o.focus == optionsFocusGroup))
	b.WriteString(fieldLine("order", picker(orderingLabels[:], int(o.ordering), width-agentRowIndent), o.focus == optionsFocusOrder))
	if !o.contextGuard.available {
		return b.String()
	}
	b.WriteString("\n" + styleDim.Render("Context guard: Codex telemetry only · automatic action pending safety verification") + "\n")
	if o.contextGuard.loading {
		b.WriteString(styleDim.Render("  loading context guard settings…") + "\n")
		return b.String()
	}
	if !o.contextGuard.loaded {
		b.WriteString(styleDim.Render("  "+o.contextGuard.err) + "\n")
		return b.String()
	}
	compact := "[ ] disabled"
	if o.contextGuard.autoCompact.Enabled {
		compact = "[x] enabled"
	}
	b.WriteString(fieldLine("auto compact", compact, o.focus == optionsFocusAutoCompact))
	threshold := o.contextGuard.threshold.text + "%"
	if o.focus == optionsFocusThreshold {
		threshold = o.contextGuard.threshold.cursorView() + "%"
	}
	b.WriteString(fieldLine("threshold", threshold, o.focus == optionsFocusThreshold))
	if o.contextGuard.saving {
		b.WriteString(styleDim.Render("  saving context guard settings…") + "\n")
	}
	if o.contextGuard.err != "" {
		b.WriteString(styleError.Render("  "+o.contextGuard.err) + "\n")
	}
	return b.String()
}

// picker renders a choice row the way the agent picker does: the selection as an
// accent-filled dot, the rest as hollow dots, flanked by the cycle-arrow affordances.
// When the full row exceeds budget (> 0), it degrades to the handoff form's compact
// "◂ selected ▸" so the selection always stays visible; a plain clamp would clip it.
func picker(labels []string, sel, budget int) string {
	parts := make([]string, len(labels))
	for i, l := range labels {
		if i == sel {
			parts[i] = styleAccent.Render("● " + l)
		} else {
			parts[i] = "○ " + l
		}
	}
	row := styleDim.Render("◂ ") + strings.Join(parts, "   ") + styleDim.Render(" ▸")
	if budget > 0 && lipgloss.Width(row) > budget {
		row = styleDim.Render("◂ ") + styleAccent.Render(labels[sel]) + styleDim.Render(" ▸")
	}
	return row
}
