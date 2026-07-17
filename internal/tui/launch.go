package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

// launchModel is the new-session form. Fields are collected in the L-1 order:
// directory, agent, options..., prompt, worktree. The cwd is free text with "~"
// expansion; an invalid cwd is refused inline (L-3).
type launchModel struct {
	agents   []AgentInfo
	agentIdx int // index into agents of the chosen agent

	cwd      string
	optSpecs []adapter.OptionSpec // the chosen agent's declarative schema
	options  map[string]string    // option key -> current value
	prompt   string
	worktree bool

	focus  int    // field focus index (see field-index helpers below)
	errMsg string // inline validation error (e.g. cwd does not exist)
	width  int
}

// newLaunchModel builds a fresh form, defaulting the agent to the first usable
// (installed and in-range) one and seeding options from that agent's schema.
func newLaunchModel(agents []AgentInfo, width int) launchModel {
	m := launchModel{agents: agents, width: width}
	m.agentIdx = firstUsable(agents)
	m.loadAgentOptions()
	return m
}

func firstUsable(agents []AgentInfo) int {
	for i, a := range agents {
		if a.usable() {
			return i
		}
	}
	return 0
}

// loadAgentOptions resets the option schema/values to the currently chosen
// agent's defaults.
func (m *launchModel) loadAgentOptions() {
	m.optSpecs = nil
	m.options = map[string]string{}
	if m.agentIdx < 0 || m.agentIdx >= len(m.agents) {
		return
	}
	m.optSpecs = m.agents[m.agentIdx].Options
	for _, o := range m.optSpecs {
		m.options[o.Key] = o.Default
	}
}

// ---------------------------------------------------------------------------
// Field indexing: directory, agent, [options...], prompt, worktree.
// ---------------------------------------------------------------------------

func (m launchModel) fieldCount() int    { return 4 + len(m.optSpecs) }
func (m launchModel) isDir() bool        { return m.focus == 0 }
func (m launchModel) isAgent() bool      { return m.focus == 1 }
func (m launchModel) promptIndex() int   { return 2 + len(m.optSpecs) }
func (m launchModel) worktreeIndex() int { return 3 + len(m.optSpecs) }
func (m launchModel) isPrompt() bool     { return m.focus == m.promptIndex() }
func (m launchModel) isWorktree() bool   { return m.focus == m.worktreeIndex() }
func (m launchModel) optionFocus() (int, bool) {
	if m.focus >= 2 && m.focus < 2+len(m.optSpecs) {
		return m.focus - 2, true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Router glue: keyboard handling for the launch screen.
// ---------------------------------------------------------------------------

func (m rootModel) updateLaunch(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	lm := &m.launch
	switch {
	case k.Code == tea.KeyEsc:
		m.screen = screenGeneral
	case k.Code == tea.KeyEnter:
		return m.submitLaunch()
	case k.Code == tea.KeyTab:
		if n := lm.fieldCount(); n > 0 {
			lm.focus = (lm.focus + 1) % n
		}
	case k.Code == tea.KeyBackspace:
		if lm.isDir() {
			lm.cwd = dropLast(lm.cwd)
			lm.errMsg = ""
		} else if lm.isPrompt() {
			lm.prompt = dropLast(lm.prompt)
		}
	case k.Code == tea.KeyLeft:
		lm.cycleField(false)
	case k.Code == tea.KeyRight:
		lm.cycleField(true)
	case k.Text != "":
		switch {
		case lm.isWorktree() && k.Text == " ":
			lm.worktree = !lm.worktree
		case lm.isDir():
			lm.cwd += k.Text
			lm.errMsg = ""
		case lm.isPrompt():
			lm.prompt += k.Text
		}
	}
	return m, nil
}

// cycleField steps a choice option or the agent picker (left/right).
func (m *launchModel) cycleField(forward bool) {
	if m.isAgent() {
		m.cycleAgent(forward)
		return
	}
	if si, ok := m.optionFocus(); ok {
		m.cycleOption(si, forward)
	}
}

// cycleAgent moves to the next/previous usable agent, reloading its options.
func (m *launchModel) cycleAgent(forward bool) {
	n := len(m.agents)
	if n == 0 {
		return
	}
	step := 1
	if !forward {
		step = -1
	}
	for i := 1; i <= n; i++ {
		idx := ((m.agentIdx+step*i)%n + n) % n
		if m.agents[idx].usable() {
			m.agentIdx = idx
			m.focus = 1 // stay on the agent field
			m.loadAgentOptions()
			return
		}
	}
}

// cycleOption advances a choice option's value; non-choice options are untouched.
func (m *launchModel) cycleOption(specIdx int, forward bool) {
	spec := m.optSpecs[specIdx]
	if spec.Type != "choice" || len(spec.Choices) == 0 {
		return
	}
	cur := 0
	for i, c := range spec.Choices {
		if c == m.options[spec.Key] {
			cur = i
			break
		}
	}
	step := 1
	if !forward {
		step = -1
	}
	next := ((cur+step)%len(spec.Choices) + len(spec.Choices)) % len(spec.Choices)
	m.options[spec.Key] = spec.Choices[next]
}

// submitLaunch validates the form and, if the cwd exists, composes and fires the
// LaunchReq (agent, expanded cwd, schema options, initial prompt) then returns to
// the general view. An invalid cwd is refused inline with no launch (L-3).
func (m rootModel) submitLaunch() (tea.Model, tea.Cmd) {
	lm := &m.launch
	expanded := expandTilde(strings.TrimSpace(lm.cwd))
	if expanded == "" {
		lm.errMsg = "directory is required"
		return m, nil
	}
	info, err := os.Stat(expanded)
	if err != nil || !info.IsDir() {
		lm.errMsg = "directory " + expanded + " does not exist"
		return m, nil
	}

	agent := ""
	if lm.agentIdx >= 0 && lm.agentIdx < len(lm.agents) {
		agent = lm.agents[lm.agentIdx].Name
	}
	opts := make(map[string]string, len(lm.options))
	for k, v := range lm.options {
		opts[k] = v
	}
	req := protocol.LaunchReq{
		Agent:         agent,
		Cwd:           expanded,
		Options:       opts,
		InitialPrompt: lm.prompt,
	}
	m.screen = screenGeneral
	return m, launchCmd(m.client, req)
}

func launchCmd(c Client, req protocol.LaunchReq) tea.Cmd {
	return func() tea.Msg {
		_, _ = c.Launch(req)
		return nil
	}
}

// expandTilde expands a leading "~" to the user's home directory.
func expandTilde(p string) string {
	home := userHome()
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return home + p[1:]
	}
	return p
}

func dropLast(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return string(r[:len(r)-1])
}

// ---------------------------------------------------------------------------
// Rendering.
// ---------------------------------------------------------------------------

const launchLabelW = 12

func (m launchModel) view() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("swarm") + styleDim.Render(" · new session") + "\n\n")

	b.WriteString(m.fieldLine("directory", m.dirValue(), m.isDir()))
	b.WriteString(m.fieldLine("agent", m.agentValue(), m.isAgent()))
	for i, spec := range m.optSpecs {
		b.WriteString(m.fieldLine(spec.Label, m.optionValue(spec), m.focus == 2+i))
	}
	b.WriteString(m.fieldLine("prompt", m.promptValue(), m.isPrompt()))
	b.WriteString(m.fieldLine("worktree", m.worktreeValue(), m.isWorktree()))

	b.WriteString("\n")
	if m.errMsg != "" {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(colNeedsInput).Render(m.errMsg) + "\n\n")
	}
	b.WriteString("  " + styleDim.Render("⏎ launch   tab next field   esc cancel"))
	return b.String()
}

// fieldLine renders one labelled field, marking the focused one with a bar.
func (m launchModel) fieldLine(label, value string, focused bool) string {
	prefix := "  "
	if focused {
		prefix = lipgloss.NewStyle().Foreground(colAmber).Render("▌") + " "
	}
	return prefix + styleDim.Render(padRight(label, launchLabelW)) + value + "\n"
}

func (m launchModel) dirValue() string {
	v := m.cwd
	if m.isDir() {
		v += "█" // cursor on the focused text field
	}
	return v
}

func (m launchModel) agentValue() string {
	parts := make([]string, 0, len(m.agents))
	for i, a := range m.agents {
		var mark, text string
		switch {
		case a.usable() && i == m.agentIdx:
			mark = "●"
		case a.usable():
			mark = "○"
		case !a.Installed:
			mark = "✕"
		default:
			mark = "○" // installed but out of supported range
		}
		text = mark + " " + a.Name
		if !a.usable() && a.InstallHint != "" {
			text += " (" + a.InstallHint + ")"
		}
		if a.usable() && i == m.agentIdx {
			text = lipgloss.NewStyle().Foreground(colAmber).Render(text)
		} else if !a.usable() {
			text = styleDim.Render(text)
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "   ")
}

func (m launchModel) optionValue(spec adapter.OptionSpec) string {
	v := m.options[spec.Key]
	if spec.Type == "choice" {
		return v + " " + styleDim.Render("▾")
	}
	return v
}

func (m launchModel) promptValue() string {
	v := m.prompt
	if m.isPrompt() {
		return v + "█"
	}
	if v == "" {
		return styleDim.Render("(optional)")
	}
	return v
}

func (m launchModel) worktreeValue() string {
	box := "[ ]"
	if m.worktree {
		box = "[x]"
	}
	return box + " " + styleDim.Render("isolate in a git worktree")
}
