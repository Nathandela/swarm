package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// handoffModel is the deliberately small source-side form. It captures the source
// session by identity and exposes only the receiving CLI and its model. Launch scope,
// permission bypasses, directories, prompts, and worktree choices do not belong here:
// the source agent authors context and invokes the first-class CLI itself.
type handoffModel struct {
	sourceID string

	agents    []AgentInfo
	targetIdx int
	detected  bool
	model     string
	focus     int // 0 target, 1 model

	errMsg string
	width  int
}

func newHandoffModel(source protocol.SessionView, agents []AgentInfo, detected bool, width int) handoffModel {
	m := handoffModel{
		sourceID: source.ID,
		agents:   append([]AgentInfo(nil), agents...),
		detected: detected,
		width:    width,
	}
	m.targetIdx = firstUsableHandoff(m.agents)
	m.loadModelDefault()
	return m
}

func firstUsableHandoff(agents []AgentInfo) int {
	for i, a := range agents {
		if a.usable() {
			return i
		}
	}
	return -1
}

func (m handoffModel) targetName() string {
	if m.targetIdx < 0 || m.targetIdx >= len(m.agents) {
		return ""
	}
	return m.agents[m.targetIdx].Name
}

func (m handoffModel) modelSpec() (adapter.OptionSpec, bool) {
	if m.targetIdx < 0 || m.targetIdx >= len(m.agents) {
		return adapter.OptionSpec{}, false
	}
	for _, spec := range m.agents[m.targetIdx].Options {
		if spec.Key == "model" {
			return spec, true
		}
	}
	return adapter.OptionSpec{}, false
}

func (m *handoffModel) loadModelDefault() {
	m.model = ""
	if spec, ok := m.modelSpec(); ok {
		m.model = spec.Default
		if m.model == "" {
			values := handoffModelValues(spec)
			if len(values) > 0 {
				m.model = values[0]
			}
		}
	}
}

func handoffModelValues(spec adapter.OptionSpec) []string {
	values := make([]string, 0, 1+len(spec.Choices)+len(spec.Suggest))
	seen := map[string]bool{}
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	add(spec.Default)
	for _, v := range spec.Choices {
		add(v)
	}
	for _, v := range spec.Suggest {
		add(v)
	}
	return values
}

// refreshAgents updates availability without losing the semantic selection or an
// edited model when the same target remains usable.
func (m *handoffModel) refreshAgents(agents []AgentInfo) {
	previousTarget, previousModel := m.targetName(), m.model
	m.agents = append([]AgentInfo(nil), agents...)
	m.detected = true
	m.targetIdx = -1
	for i, a := range m.agents {
		if a.Name == previousTarget && a.usable() {
			m.targetIdx = i
			break
		}
	}
	if m.targetIdx < 0 {
		m.targetIdx = firstUsableHandoff(m.agents)
	}
	if m.targetName() == previousTarget {
		m.model = previousModel
	} else {
		m.loadModelDefault()
	}
	if m.targetIdx >= 0 {
		m.errMsg = ""
	}
}

func (m *handoffModel) cycleTarget(forward bool) {
	if len(m.agents) == 0 {
		return
	}
	step := 1
	if !forward {
		step = -1
	}
	start := m.targetIdx
	if start < 0 {
		start = 0
		if !forward {
			start = len(m.agents) - 1
		}
	}
	for n := 1; n <= len(m.agents); n++ {
		i := (start + step*n) % len(m.agents)
		if i < 0 {
			i += len(m.agents)
		}
		if m.agents[i].usable() {
			m.targetIdx = i
			m.loadModelDefault()
			m.errMsg = ""
			return
		}
	}
}

func (m *handoffModel) cycleModel(forward bool) {
	spec, ok := m.modelSpec()
	if !ok {
		return
	}
	values := handoffModelValues(spec)
	if len(values) == 0 {
		return
	}
	m.model = cycleValue(values, m.model, forward)
	m.errMsg = ""
}

func (m *handoffModel) paste(s string) {
	if m.focus != 1 {
		return
	}
	spec, ok := m.modelSpec()
	if !ok || spec.Type != "string" {
		return
	}
	s = strings.NewReplacer("\r", "", "\n", "").Replace(s)
	m.model += s
	m.errMsg = ""
}

func (m handoffModel) view() string {
	var b strings.Builder
	title := styleTitle.Render("swarm") + styleDim.Render(" · supervised handoff")
	b.WriteString(m.clampLine(title) + "\n\n")
	b.WriteString(m.fieldLine("target", m.targetValue(), m.focus == 0))
	b.WriteString(m.fieldLine("model", m.modelValue(), m.focus == 1))
	if m.errMsg != "" {
		b.WriteString("\n" + m.clampLine("  "+styleError.Render(m.errMsg)) + "\n")
	}
	return b.String()
}

func (m handoffModel) fieldLine(label, value string, focused bool) string {
	prefix := "  "
	if focused {
		prefix = styleAmber.Render("▌") + " "
	}
	return m.clampLine(prefix+styleDim.Render(padLabel(label))+value) + "\n"
}

func (m handoffModel) clampLine(line string) string {
	if m.width > 0 {
		return clampCells(line, m.width)
	}
	return line
}

func (m handoffModel) targetValue() string {
	if !m.detected {
		return styleDim.Render("checking...")
	}
	if m.targetIdx < 0 {
		return styleError.Render("no installed, supported agent")
	}
	return styleDim.Render("◂ ") + styleAmber.Render(m.targetName()) + styleDim.Render(" ▸")
}

func (m handoffModel) modelValue() string {
	spec, ok := m.modelSpec()
	if !ok {
		return styleDim.Render("(agent default)")
	}
	value := m.model
	if value == "" {
		value = "(agent default)"
	}
	if m.focus == 1 && spec.Type == "string" {
		return value + "█"
	}
	if len(handoffModelValues(spec)) > 1 {
		return value + " " + styleDim.Render("◂ ▸")
	}
	return value
}

func (m handoffModel) hint() string {
	if m.focus == 0 {
		return "arrows change target · tab/↑↓ next · enter continue · esc cancel"
	}
	if spec, ok := m.modelSpec(); ok && spec.Type == "string" {
		return "type, paste, or use arrows · tab/↑↓ next · enter continue · esc cancel"
	}
	return "arrows change model · tab/↑↓ next · enter continue · esc cancel"
}

func (m rootModel) updateHandoff(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	h := &m.handoff
	switch {
	case k.Code == tea.KeyEsc:
		return m, m.enterGeneral()
	case k.Code == tea.KeyEnter:
		return m.submitHandoff()
	case k.Code == tea.KeyTab || k.Code == tea.KeyDown:
		h.focus = (h.focus + 1) % 2
	case k.Code == tea.KeyUp:
		h.focus = (h.focus + 1) % 2
	case k.Code == tea.KeyLeft:
		if h.focus == 0 {
			h.cycleTarget(false)
		} else {
			h.cycleModel(false)
		}
	case k.Code == tea.KeyRight:
		if h.focus == 0 {
			h.cycleTarget(true)
		} else {
			h.cycleModel(true)
		}
	case k.Code == tea.KeyBackspace:
		if h.focus == 1 {
			if spec, ok := h.modelSpec(); ok && spec.Type == "string" {
				h.model = dropLast(h.model)
				h.errMsg = ""
			}
		}
	case k.Text != "" && h.focus == 1:
		if spec, ok := h.modelSpec(); ok && spec.Type == "string" {
			h.model += k.Text
			h.errMsg = ""
		}
	}
	return m, nil
}

func (m rootModel) submitHandoff() (tea.Model, tea.Cmd) {
	h := &m.handoff
	source, ok := m.general.sessionByID(h.sourceID)
	if !ok {
		h.errMsg = "source session is no longer in the roster"
		return m, nil
	}
	if allowed, why := handoffSourceEligibility(source); !allowed {
		h.errMsg = why
		return m, nil
	}
	if h.targetIdx < 0 || h.targetIdx >= len(h.agents) || !h.agents[h.targetIdx].usable() {
		h.errMsg = "no installed, supported target agent"
		return m, nil
	}
	prompt, err := renderHandoffPrompt(h.targetName(), h.model)
	if err != nil {
		h.errMsg = err.Error()
		return m, nil
	}
	cmd := handoffCmd(m.client, h.sourceID, prompt)
	return m, tea.Batch(cmd, m.enterGeneral())
}

// handoffSourceEligibility uses raw status dimensions rather than the display group.
// In particular, needs_input intentionally combines ordinary prompts and permission
// requests; only the former is safe to receive an automatically submitted instruction.
func handoffSourceEligibility(source protocol.SessionView) (bool, string) {
	s := source.Status
	switch {
	case s.Process != status.ProcessRunning:
		return false, "session has ended - handoff requires a running source"
	case s.Turn != status.TurnIdle:
		return false, "session is busy - wait for its current turn"
	case s.Interaction == status.InteractionPermission:
		return false, "resolve the permission request before handoff"
	case s.Interaction == status.InteractionPrompt,
		s.Interaction == status.InteractionNone,
		s.Interaction == status.InteractionUnknown:
		return true, ""
	default:
		return false, fmt.Sprintf("session interaction state %q is not safe for handoff", s.Interaction)
	}
}

type handoffDoneMsg struct{ err error }

func handoffCmd(c Client, id, prompt string) tea.Cmd {
	return func() tea.Msg {
		return handoffDoneMsg{err: c.SendInput(id, protocol.SendInputReq{Text: prompt, Submit: true})}
	}
}
