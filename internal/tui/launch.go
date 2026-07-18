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
	agentIdx int  // index into agents of the chosen agent
	detected bool // whether detection has landed (else the picker shows "checking...")

	cwd      string
	optSpecs []adapter.OptionSpec // the chosen agent's declarative schema
	options  map[string]string    // option key -> current value
	prompt   string
	worktree bool

	apiKeyInEnv bool // ANTHROPIC_API_KEY present in the client env (auth indicator)

	focus  int    // field focus index (see field-index helpers below)
	errMsg string // inline validation error (e.g. cwd does not exist)
	width  int
}

// newLaunchModel builds a fresh form from the cached agent detection, defaulting
// the agent to the first usable (installed and in-range) one and seeding options
// from that agent's schema. The directory is prefilled with the client's working
// directory (os.Getwd) so a bare Enter launches in the current directory, and the
// inherited ANTHROPIC_API_KEY is noted for the auth indicator. detected reports
// whether detection has landed yet; a cold form shows "checking..." for the agent.
func newLaunchModel(agents []AgentInfo, detected bool, width int) launchModel {
	m := launchModel{agents: agents, detected: detected, width: width}
	if wd, err := os.Getwd(); err == nil {
		m.cwd = wd
	}
	m.apiKeyInEnv = os.Getenv("ANTHROPIC_API_KEY") != ""
	m.agentIdx = firstUsable(agents)
	m.loadAgentOptions()
	return m
}

// refreshAgents folds a fresh detection result into an open form, so agent
// availability greys/ungreys live. It preserves the selected agent by name and
// carries over any values the user has already typed, so a background refresh
// never jumps the picker or clobbers the form.
func (m *launchModel) refreshAgents(agents []AgentInfo) {
	prevName := m.currentAgentName()
	prevOpts := m.options
	// Capture the user's SEMANTIC focus BEFORE the option schema (and thus the field
	// indices) shifts under this re-detection. A grown optSpecs slides prompt/worktree
	// down, so keeping the raw focus index would re-index the focused field and
	// misroute typed runes/Space (L3/Opus MEDIUM).
	wasDir := m.isDir()
	wasAgent := m.isAgent()
	wasPrompt := m.isPrompt()
	wasWorktree := m.isWorktree()

	m.agents = agents
	m.detected = true
	m.agentIdx = firstUsable(agents)
	for i, a := range agents {
		if a.Name == prevName {
			m.agentIdx = i
			break
		}
	}
	m.loadAgentOptions()
	for k := range m.options {
		if v, ok := prevOpts[k]; ok {
			m.options[k] = v // keep what the user already entered for a still-present key
		}
	}

	// Re-anchor focus onto the same semantic field after the re-index. Directory,
	// agent, prompt and worktree map to their new indices; an option focus (the field
	// most likely to have moved or vanished as the list changed beneath it) clamps to
	// the directory field.
	switch {
	case wasDir:
		m.focus = 0
	case wasAgent:
		m.focus = 1
	case wasPrompt:
		m.focus = m.promptIndex()
	case wasWorktree:
		m.focus = m.worktreeIndex()
	default:
		m.focus = 0
	}
}

// currentAgentName is the selected agent's name, or "" when none is selected.
func (m launchModel) currentAgentName() string {
	if m.agentIdx >= 0 && m.agentIdx < len(m.agents) {
		return m.agents[m.agentIdx].Name
	}
	return ""
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

// focusedOptionOfType returns the focused option's schema index when it is of the
// given Type (e.g. "string", "bool"), else ok=false.
func (m launchModel) focusedOptionOfType(typ string) (int, bool) {
	if si, ok := m.optionFocus(); ok && m.optSpecs[si].Type == typ {
		return si, true
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
		cmd := m.enterGeneral()
		return m, cmd
	case k.Code == tea.KeyEnter:
		return m.submitLaunch()
	case k.Code == tea.KeyTab:
		if n := lm.fieldCount(); n > 0 {
			lm.focus = (lm.focus + 1) % n
		}
	case k.Code == tea.KeyBackspace:
		switch {
		case lm.isDir():
			lm.cwd = dropLast(lm.cwd)
			lm.errMsg = ""
		case lm.isPrompt():
			lm.prompt = dropLast(lm.prompt)
		default:
			if si, ok := lm.focusedOptionOfType("string"); ok {
				key := lm.optSpecs[si].Key
				lm.options[key] = dropLast(lm.options[key])
			}
		}
	case k.Code == tea.KeyLeft:
		lm.cycleField(false)
	case k.Code == tea.KeyRight:
		lm.cycleField(true)
	case k.Text != "":
		switch {
		case lm.isWorktree() && k.Text == " ":
			lm.worktree = !lm.worktree
		case k.Text == " " && lm.toggleBoolOption():
			// handled: Space toggled the focused bool option
		case lm.isDir():
			lm.cwd += k.Text
			lm.errMsg = ""
		case lm.isPrompt():
			lm.prompt += k.Text
		default:
			if si, ok := lm.focusedOptionOfType("string"); ok {
				lm.options[lm.optSpecs[si].Key] += k.Text
			}
		}
	}
	return m, nil
}

// toggleBoolOption flips the focused bool option ("true"/"false") and reports
// whether it did so, generalizing the worktree checkbox to any Type "bool" option.
func (m *launchModel) toggleBoolOption() bool {
	si, ok := m.focusedOptionOfType("bool")
	if !ok {
		return false
	}
	key := m.optSpecs[si].Key
	if m.options[key] == "true" {
		m.options[key] = "false"
	} else {
		m.options[key] = "true"
	}
	return true
}

// paste delivers bracketed-paste content into the focused text field (directory,
// prompt, or an editable string option), stripping the CR/LF that single-line
// fields must never carry.
func (m *launchModel) paste(s string) {
	s = strings.NewReplacer("\r", "", "\n", "").Replace(s)
	if s == "" {
		return
	}
	switch {
	case m.isDir():
		m.cwd += s
		m.errMsg = ""
	case m.isPrompt():
		m.prompt += s
	default:
		if si, ok := m.focusedOptionOfType("string"); ok {
			m.options[m.optSpecs[si].Key] += s
		}
	}
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

// cycleOption advances a choice option through its Choices, or an editable string
// option through its curated Suggest values; other options are untouched. For a
// string option whose current value is not among the suggestions, the first
// forward step lands on the first suggestion (last on a backward step).
func (m *launchModel) cycleOption(specIdx int, forward bool) {
	spec := m.optSpecs[specIdx]
	switch {
	case spec.Type == "choice" && len(spec.Choices) > 0:
		m.options[spec.Key] = cycleValue(spec.Choices, m.options[spec.Key], forward)
	case spec.Type == "string" && len(spec.Suggest) > 0:
		m.options[spec.Key] = cycleValue(spec.Suggest, m.options[spec.Key], forward)
	}
}

// cycleValue returns the value one step from cur within values (wrapping). When
// cur is absent, a forward step yields the first value and a backward step the
// last, so cycling from free text enters the suggestion list at a sensible end.
func cycleValue(values []string, cur string, forward bool) string {
	idx := -1
	for i, v := range values {
		if v == cur {
			idx = i
			break
		}
	}
	var next int
	switch {
	case idx < 0:
		if forward {
			next = 0
		} else {
			next = len(values) - 1
		}
	default:
		step := 1
		if !forward {
			step = -1
		}
		next = ((idx+step)%len(values) + len(values)) % len(values)
	}
	return values[next]
}

// submitLaunch validates the form and, if it passes, composes and fires the
// LaunchReq (agent, expanded cwd, schema options, initial prompt) then returns to
// the general view. An invalid cwd (L-3) or an unusable agent (L-2) is refused
// inline with no launch, so the client never composes a request against a
// missing or out-of-range agent.
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
	if lm.agentIdx < 0 || lm.agentIdx >= len(lm.agents) || !lm.agents[lm.agentIdx].usable() {
		lm.errMsg = "no installed, supported agent selected"
		return m, nil
	}

	opts := make(map[string]string, len(lm.options))
	for k, v := range lm.options {
		opts[k] = v
	}
	cols, rows := launchDims(m.width, m.height)
	req := protocol.LaunchReq{
		Agent:         lm.agents[lm.agentIdx].Name,
		Cwd:           expanded,
		Options:       opts,
		InitialPrompt: lm.prompt,
		Env:           os.Environ(), // so the daemon can resolve the agent binary on PATH
		Cols:          cols,
		Rows:          rows,
	}
	cmd := m.enterGeneral()
	return m, tea.Batch(launchCmd(m.client, req), cmd)
}

// launchDims resolves the session terminal size from the current UI size, falling
// back to a sane default before the first WindowSizeMsg arrives (the daemon rejects
// a zero/out-of-range size).
func launchDims(w, h int) (int, int) {
	if w <= 0 {
		w = defaultResumeCols
	}
	if h <= 0 {
		h = defaultResumeRows
	}
	return w, h
}

func launchCmd(c Client, req protocol.LaunchReq) tea.Cmd {
	return func() tea.Msg {
		_, err := c.Launch(req)
		return launchResultMsg{err: err}
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
		focused := m.focus == 2+i
		b.WriteString(m.fieldLine(spec.Label, m.optionValue(spec, focused), focused))
	}
	b.WriteString(m.fieldLine("prompt", m.promptValue(), m.isPrompt()))
	b.WriteString(m.fieldLine("worktree", m.worktreeValue(), m.isWorktree()))

	if auth := m.authLine(); auth != "" {
		b.WriteString("\n" + auth + "\n")
	}

	b.WriteString("\n")
	if m.errMsg != "" {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(colNeedsInput).Render(m.errMsg) + "\n\n")
	}
	// The contextual field hint is promoted to the router's persistent bottom bar
	// (composeBoard uses m.hint()), so it is no longer rendered inline here.
	return b.String()
}

// hint is the contextual footer for the focused field: text fields prompt to type
// or paste, the agent and choice pickers to use the arrows, and bool options to
// toggle with Space. The tab/enter/esc tail is constant across fields.
func (m launchModel) hint() string {
	const tail = " · tab next · enter launch · esc cancel"
	if m.isAgent() || m.isChoiceFocused() {
		return "arrows change" + tail
	}
	if m.isWorktree() {
		return "space toggle" + tail
	}
	if _, ok := m.focusedOptionOfType("bool"); ok {
		return "space toggle" + tail
	}
	return "type or paste" + tail
}

// isChoiceFocused reports whether the focused field is a choice option.
func (m launchModel) isChoiceFocused() bool {
	_, ok := m.focusedOptionOfType("choice")
	return ok
}

// authLine surfaces which auth a claude launch will inherit from the client env.
// It is neutral and purely informational: swarm mirrors the launching terminal
// (spec scenario 18) and never alters the env, so it states the fact without any
// advice. Shown only when the selected agent is claude and the key is present.
func (m launchModel) authLine() string {
	if !m.apiKeyInEnv || m.currentAgentName() != "claude" {
		return ""
	}
	return "  " + lipgloss.NewStyle().Foreground(colAmber).Render("auth: ANTHROPIC_API_KEY from env (API billing)")
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
	if !m.detected {
		return styleDim.Render("checking...")
	}
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
	// Flank the list with arrow affordances so left/right cycling is discoverable.
	return styleDim.Render("◂ ") + strings.Join(parts, "   ") + styleDim.Render(" ▸")
}

func (m launchModel) optionValue(spec adapter.OptionSpec, focused bool) string {
	v := m.options[spec.Key]
	switch spec.Type {
	case "bool":
		if v == "true" {
			return "[x]"
		}
		return "[ ]"
	case "choice":
		return v + " " + styleDim.Render("▾")
	default: // editable string (possibly with curated suggestions)
		if focused {
			return v + "█" // cursor on the focused text field
		}
		if v == "" {
			return styleDim.Render("(default)")
		}
		return v
	}
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
