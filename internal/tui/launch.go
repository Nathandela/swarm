package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	edlib "github.com/hbollon/go-edlib"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

// launchModel is the new-session form. Fields are collected in the L-1 order:
// directory, name, tag, agent, options..., prompt, worktree. The cwd is free text
// with "~" expansion; an invalid cwd is refused inline (L-3). The name is an
// optional label; left empty it defaults to "<agent>-<base cwd>" at submit (P2).
// The tag is the optional grouping label `t` also sets on a live session; given
// here it rides the launch request, so the session is tagged from birth.
type launchModel struct {
	agents   []AgentInfo
	agentIdx int  // index into agents of the chosen agent
	detected bool // whether detection has landed (else the picker shows "checking...")

	cwd      lineEditor
	name     lineEditor            // optional session label; empty defaults to "<agent>-<base cwd>" at submit (P2)
	tag      lineEditor            // optional grouping label; empty (or blank) launches untagged
	optSpecs []adapter.OptionSpec  // the chosen agent's declarative schema
	options  map[string]lineEditor // option key -> current value/editor; non-string cursors are unused
	prompt   lineEditor
	worktree bool

	dirCands  []string // candidate basenames for the typed cwd prefix, ReadDir (sorted) order
	dirGhost  string   // completion remainder rendered after the cursor
	dirParent string   // typed-form parent prefix incl trailing "/", never tilde-expanded
	dirFuzzy  bool     // dirCands came from a typo check, so even one candidate renders and arrows choose it
	createCwd string   // expanded missing path armed by the immediately preceding Enter

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
		m.cwd.set(wd)
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
	wasName := m.isName()
	wasTag := m.isTag()
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
	// name, tag, agent, prompt and worktree map to their new indices; an option focus
	// (the field most likely to have moved or vanished as the list changed beneath
	// it) clamps to the directory field.
	switch {
	case wasDir:
		m.focus = 0
	case wasName:
		m.focus = 1
	case wasTag:
		m.focus = m.tagIndex()
	case wasAgent:
		m.focus = m.agentIndex()
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

// agentReason is the human-readable cause an agent is unusable. It is shown
// greyed in the picker and quoted in the launch-guard refusal so the user learns
// why an agent cannot launch (L-2).
func agentReason(a AgentInfo) string {
	return a.Reason
}

// loadAgentOptions resets the option schema/values to the currently chosen
// agent's defaults.
func (m *launchModel) loadAgentOptions() {
	m.optSpecs = nil
	m.options = map[string]lineEditor{}
	if m.agentIdx < 0 || m.agentIdx >= len(m.agents) {
		return
	}
	m.optSpecs = m.agents[m.agentIdx].Options
	for _, o := range m.optSpecs {
		m.options[o.Key] = newLineEditor(o.Default)
	}
}

// ---------------------------------------------------------------------------
// Field indexing: directory, name, tag, agent, [options...], prompt, worktree.
// The option block's base index is named once (optionsIndex) so a field inserted
// ahead of it moves every dependent index together.
// ---------------------------------------------------------------------------

func (m launchModel) fieldCount() int    { return 6 + len(m.optSpecs) }
func (m launchModel) isDir() bool        { return m.focus == 0 }
func (m launchModel) isName() bool       { return m.focus == 1 }
func (m launchModel) tagIndex() int      { return 2 }
func (m launchModel) isTag() bool        { return m.focus == m.tagIndex() }
func (m launchModel) agentIndex() int    { return 3 }
func (m launchModel) isAgent() bool      { return m.focus == m.agentIndex() }
func (m launchModel) optionsIndex() int  { return 4 }
func (m launchModel) promptIndex() int   { return m.optionsIndex() + len(m.optSpecs) }
func (m launchModel) worktreeIndex() int { return m.promptIndex() + 1 }
func (m launchModel) isPrompt() bool     { return m.focus == m.promptIndex() }
func (m launchModel) isWorktree() bool   { return m.focus == m.worktreeIndex() }
func (m launchModel) optionFocus() (int, bool) {
	if m.focus >= m.optionsIndex() && m.focus < m.optionsIndex()+len(m.optSpecs) {
		return m.focus - m.optionsIndex(), true
	}
	return 0, false
}

// moveFocus steps the field focus by delta (Tab/Down = +1, Up = -1) with wrapping,
// so up/down navigate fields exactly like Tab.
func (m *launchModel) moveFocus(delta int) {
	if n := m.fieldCount(); n > 0 {
		m.focus = ((m.focus+delta)%n + n) % n
	}
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
	if k.Code != tea.KeyEnter {
		// Creation is deliberately a consecutive-Enter confirmation. Navigation,
		// editing, or pasting makes the user reconfirm the path.
		lm.createCwd = ""
	}
	switch {
	case k.Code == tea.KeyEsc:
		cmd := m.enterGeneral()
		return m, cmd
	case k.Code == tea.KeyEnter:
		return m.submitLaunch()
	case k.Code == tea.KeyTab:
		// Tab is reserved for shell-like path completion in the directory field.
		// Field navigation stays on Up/Down, so a completion attempt can never
		// unexpectedly move the user into another form control.
		if lm.isDir() {
			lm.cycleDirCompletion(true)
		}
	case k.Code == tea.KeyDown:
		lm.moveFocus(1)
	case k.Code == tea.KeyUp:
		lm.moveFocus(-1)
	case (k.Code == tea.KeyLeft || k.Code == tea.KeyRight) && k.Mod == 0 && lm.isDir():
		// Directory keeps its established shell-like plain-arrow completion. Only
		// exact unmodified arrows are reserved; Command/Meta arrows reach the editor.
		lm.cycleDirCompletion(k.Code == tea.KeyRight)
	case (k.Code == tea.KeyLeft || k.Code == tea.KeyRight) && k.Mod == 0 && lm.plainArrowsCycleField():
		// Pickers and curated string suggestions retain their established arrows.
		lm.cycleField(k.Code == tea.KeyRight)
	case k.Text != "":
		switch {
		case lm.isWorktree() && k.Text == " ":
			lm.worktree = !lm.worktree
		case k.Text == " " && lm.toggleBoolOption():
			// handled: Space toggled the focused bool option
		default:
			lm.updateFocusedText(k)
		}
	default:
		// Backspace, plain cursor arrows on ordinary text, fast editing keys, and
		// unsupported modified editing gestures all share one implementation.
		lm.updateFocusedText(k)
	}
	return m, nil
}

// plainArrowsCycleField reports whether exact unmodified arrows belong to a
// picker/suggestion list rather than the focused line editor.
func (m launchModel) plainArrowsCycleField() bool {
	if m.isAgent() || m.isChoiceFocused() {
		return true
	}
	if si, ok := m.focusedOptionOfType("string"); ok {
		return len(m.optSpecs[si].Suggest) > 0
	}
	return false
}

// updateFocusedText delegates a key to the focused writable line. Its only
// screen-specific mutation hook is directory completion/validation refresh.
func (m *launchModel) updateFocusedText(k tea.KeyPressMsg) lineEditResult {
	var result lineEditResult
	switch {
	case m.isDir():
		result = m.cwd.update(k)
	case m.isName():
		result = m.name.update(k)
	case m.isTag():
		result = m.tag.update(k)
	case m.isPrompt():
		result = m.prompt.update(k)
	default:
		if si, ok := m.focusedOptionOfType("string"); ok {
			key := m.optSpecs[si].Key
			editor := m.options[key]
			result = editor.update(k)
			m.options[key] = editor // map values are not addressable: copy/update/write back
		}
	}
	if result.changed && m.isDir() {
		m.errMsg = ""
		m.refreshDirCompletion()
	}
	return result
}

// toggleBoolOption flips the focused bool option ("true"/"false") and reports
// whether it did so, generalizing the worktree checkbox to any Type "bool" option.
func (m *launchModel) toggleBoolOption() bool {
	si, ok := m.focusedOptionOfType("bool")
	if !ok {
		return false
	}
	key := m.optSpecs[si].Key
	editor := m.options[key]
	if editor.text == "true" {
		editor.set("false")
	} else {
		editor.set("true")
	}
	m.options[key] = editor
	return true
}

// paste delivers bracketed-paste content into the focused text field (directory,
// prompt, or an editable string option), stripping the CR/LF that single-line
// fields must never carry.
func (m *launchModel) paste(s string) {
	var changed bool
	switch {
	case m.isDir():
		changed = m.cwd.paste(s)
	case m.isName():
		changed = m.name.paste(s)
	case m.isTag():
		changed = m.tag.paste(s)
	case m.isPrompt():
		changed = m.prompt.paste(s)
	default:
		if si, ok := m.focusedOptionOfType("string"); ok {
			key := m.optSpecs[si].Key
			editor := m.options[key]
			changed = editor.paste(s)
			m.options[key] = editor
		}
	}
	if changed && m.isDir() {
		m.errMsg = ""
		m.refreshDirCompletion()
	}
}

// cycleDirCompletion drives the directory field's arrows: Right on a non-empty
// ghost accepts it (drill-down, recomputing one level down); otherwise, with more
// than one candidate, the arrows cycle the typed text through the candidates' full
// paths without re-reading the directory, so the menu stays anchored to the prefix
// that produced it. Left never accepts a ghost — only Right does.
func (m *launchModel) cycleDirCompletion(forward bool) {
	if m.dirFuzzy && len(m.dirCands) > 0 {
		fullPaths := make([]string, len(m.dirCands))
		for i, c := range m.dirCands {
			fullPaths[i] = m.dirParent + c
		}
		m.cwd.set(cycleValue(fullPaths, m.cwd.text, forward))
		m.createCwd = ""
		m.errMsg = ""
		return
	}
	if forward && m.dirGhost != "" {
		m.cwd.set(m.cwd.text + m.dirGhost)
		m.errMsg = ""
		m.refreshDirCompletion()
		return
	}
	if len(m.dirCands) > 1 {
		fullPaths := make([]string, len(m.dirCands))
		for i, c := range m.dirCands {
			fullPaths[i] = m.dirParent + c
		}
		m.cwd.set(cycleValue(fullPaths, m.cwd.text, forward))
		m.errMsg = ""
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

// cycleAgent moves to the next/previous agent, reloading its options. The arrows
// land on unusable agents too (L-2 design a): the picker greys them and shows the
// reason, and submit is refused inline with that reason — so an arrow on a broken
// agent is never a silent no-op, and the user learns why it cannot launch.
func (m *launchModel) cycleAgent(forward bool) {
	n := len(m.agents)
	if n == 0 {
		return
	}
	step := 1
	if !forward {
		step = -1
	}
	m.agentIdx = ((m.agentIdx+step)%n + n) % n
	m.focus = m.agentIndex() // stay on the agent field
	m.loadAgentOptions()
}

// cycleOption advances a choice option through its Choices, or an editable string
// option through its curated Suggest values; other options are untouched. For a
// string option whose current value is not among the suggestions, the first
// forward step lands on the first suggestion (last on a backward step).
func (m *launchModel) cycleOption(specIdx int, forward bool) {
	spec := m.optSpecs[specIdx]
	editor := m.options[spec.Key]
	switch {
	case spec.Type == "choice" && len(spec.Choices) > 0:
		editor.set(cycleValue(spec.Choices, editor.text, forward))
	case spec.Type == "string" && len(spec.Suggest) > 0:
		editor.set(cycleValue(spec.Suggest, editor.text, forward))
	}
	m.options[spec.Key] = editor
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
	expanded := expandTilde(strings.TrimSpace(lm.cwd.text))
	if expanded == "" {
		lm.errMsg = "directory is required"
		return m, nil
	}
	info, err := os.Stat(expanded)
	if err != nil {
		if !os.IsNotExist(err) {
			lm.errMsg = "cannot access directory " + expanded + ": " + err.Error()
			return m, nil
		}
		if lm.createCwd != expanded {
			lm.armDirectoryCreation(expanded)
			return m, nil
		}
		if err := os.MkdirAll(expanded, 0o755); err != nil {
			lm.errMsg = "cannot create directory " + expanded + ": " + err.Error()
			return m, nil
		}
		info, err = os.Stat(expanded)
		if err != nil {
			lm.errMsg = "cannot access created directory " + expanded + ": " + err.Error()
			return m, nil
		}
	}
	if !info.IsDir() {
		lm.errMsg = expanded + " exists but is not a directory"
		return m, nil
	}
	if lm.agentIdx < 0 || lm.agentIdx >= len(lm.agents) || !lm.agents[lm.agentIdx].usable() {
		lm.errMsg = "no installed, supported agent selected"
		if lm.agentIdx >= 0 && lm.agentIdx < len(lm.agents) {
			if r := agentReason(lm.agents[lm.agentIdx]); r != "" {
				lm.errMsg += ": " + r
			}
		}
		return m, nil
	}

	opts := make(map[string]string, len(lm.options))
	for k, v := range lm.options {
		opts[k] = v.text
	}
	agent := lm.agents[lm.agentIdx].Name
	cols, rows := launchDims(m.width, m.height)
	req := protocol.LaunchReq{
		Agent:         agent,
		Name:          launchName(lm.name.text, agent, expanded),
		Tag:           strings.TrimSpace(lm.tag.text), // blank is untagged; the server re-sanitizes
		Cwd:           expanded,
		Options:       opts,
		InitialPrompt: lm.prompt.text,
		Env:           os.Environ(), // so the daemon can resolve the agent binary on PATH
		Cols:          cols,
		Rows:          rows,
		Worktree:      lm.worktree,
	}
	cmd := m.enterGeneral()
	return m, tea.Batch(launchCmd(m.client, req), cmd)
}

// launchName resolves the submitted session label: the user's typed name (trimmed)
// when present, else the default "<agent>-<base cwd>" so every session gets a
// disambiguating label (P2). Composed at submit — the empty field never travels.
func launchName(typed, agent, cwd string) string {
	if n := strings.TrimSpace(typed); n != "" {
		return n
	}
	return agent + "-" + filepath.Base(cwd)
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
		id, name, err := c.Launch(req)
		if name == "" {
			name = req.Name // skew fallback: an older daemon's reply carries no canonical name
		}
		return launchResultMsg{id: id, agent: req.Agent, name: name, err: err}
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

// refreshDirCompletion recomputes dirCands/dirGhost/dirParent from the current cwd
// text. It reads the typed parent directory (tilde-expanded for the ReadDir call
// only — dirParent itself keeps the typed form) and lists its subdirectories that
// match the typed base name. Called after every edit of cwd while the directory
// field is focused; never at form open, so a fresh form touches no filesystem.
func (m *launchModel) refreshDirCompletion() {
	m.dirFuzzy = false
	m.createCwd = ""
	i := strings.LastIndex(m.cwd.text, "/")
	if i < 0 {
		m.dirCands, m.dirGhost, m.dirParent = nil, "", ""
		return
	}
	m.dirParent = m.cwd.text[:i+1]
	base := m.cwd.text[i+1:]

	// ponytail: symlinked directories are skipped by e.IsDir() (it reflects the
	// symlink itself, not its target); resolving via os.Stat is left for if
	// symlinked repos matter.
	entries, err := os.ReadDir(expandTilde(m.dirParent))
	if err != nil {
		m.dirCands, m.dirGhost, m.dirParent = nil, "", ""
		return
	}
	m.dirCands = nil
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, base) {
			continue
		}
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		m.dirCands = append(m.dirCands, name)
	}

	switch len(m.dirCands) {
	case 0:
		m.dirGhost = ""
	case 1:
		m.dirGhost = m.dirCands[0][len(base):] + "/"
	default:
		m.dirGhost = longestCommonPrefix(m.dirCands)[len(base):]
	}
}

// armDirectoryCreation turns a missing path into a typo guard instead of a hard
// stop. Close sibling directory names are ranked by edit distance and exposed
// through the existing arrow picker; the exact missing path is armed for creation
// only by an immediately consecutive Enter.
func (m *launchModel) armDirectoryCreation(expanded string) {
	if len(m.dirCands) > 0 && !m.dirFuzzy {
		// Prefix completion already has a stronger answer than fuzzy matching.
		// Keep its ghost/menu intact so Right retains its established drill-down
		// behavior, while still making a second Enter an explicit create.
		m.createCwd = expanded
		m.errMsg = "directory " + expanded + " does not exist; arrows complete or enter again to create"
		return
	}
	typed := strings.TrimSpace(m.cwd.text)
	i := strings.LastIndex(typed, "/")
	if i >= 0 {
		m.dirParent = typed[:i+1]
	} else {
		m.dirParent = ""
	}
	m.dirCands = fuzzySiblingDirectories(filepath.Dir(expanded), filepath.Base(expanded))
	m.dirGhost = ""
	m.dirFuzzy = len(m.dirCands) > 0
	m.createCwd = expanded
	if len(m.dirCands) > 0 {
		m.errMsg = "directory " + expanded + " does not exist; did you mean " +
			strings.Join(m.dirCands, ", ") + "? arrows choose or enter again to create"
		return
	}
	m.errMsg = "directory " + expanded + " does not exist; enter again to create"
}

type fuzzyDirectory struct {
	name     string
	distance int
}

// fuzzySiblingDirectories returns at most five visible directories whose names
// are close enough to typed to plausibly be typos. Ranking is stable: smallest
// edit distance first, then lexical name.
func fuzzySiblingDirectories(parent, typed string) []string {
	entries, err := os.ReadDir(parent)
	if err != nil || typed == "" || typed == "." || typed == string(filepath.Separator) {
		return nil
	}
	threshold := (len([]rune(typed)) + 2) / 3
	if threshold < 1 {
		threshold = 1
	}
	if threshold > 3 {
		threshold = 3
	}
	lowerTyped := strings.ToLower(typed)
	var ranked []fuzzyDirectory
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(typed, ".") {
			continue
		}
		isDir := entry.IsDir()
		if !isDir && entry.Type()&os.ModeSymlink != 0 {
			if info, statErr := os.Stat(filepath.Join(parent, name)); statErr == nil {
				isDir = info.IsDir()
			}
		}
		if !isDir {
			continue
		}
		distance := edlib.DamerauLevenshteinDistance(lowerTyped, strings.ToLower(name))
		if distance <= threshold {
			ranked = append(ranked, fuzzyDirectory{name: name, distance: distance})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].distance != ranked[j].distance {
			return ranked[i].distance < ranked[j].distance
		}
		return ranked[i].name < ranked[j].name
	})
	if len(ranked) > 5 {
		ranked = ranked[:5]
	}
	result := make([]string, len(ranked))
	for i := range ranked {
		result[i] = ranked[i].name
	}
	return result
}

// longestCommonPrefix returns the longest string prefix shared by every entry in
// ss (ss is non-empty).
func longestCommonPrefix(ss []string) string {
	prefix := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
}

// ---------------------------------------------------------------------------
// Rendering.
// ---------------------------------------------------------------------------

const launchLabelW = 12

// agentRowIndent is the cell offset at which the agent picker VALUE is rendered on its
// field line: the 2-cell focus prefix plus the padded label column. The value must fit
// the remaining form width (m.width - agentRowIndent) or it wraps and pushes the
// selected agent off-screen (item 5).
const agentRowIndent = 2 + launchLabelW

// reasonWrapCells is the fixed cell cost of the " (…)" wrapper the picker draws around
// an agent's reason, subtracted when budgeting the selected agent's clamped reason.
const reasonWrapCells = len(" ()")

func (m launchModel) view() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("swarm") + styleDim.Render(" · new session") + "\n\n")

	b.WriteString(m.launchFieldLine("directory", m.dirValue(), m.isDir()))
	if m.isDir() && (len(m.dirCands) > 1 || m.dirFuzzy && len(m.dirCands) > 0) {
		row := strings.Join(m.dirCands, "  ")
		if m.width > 2+launchLabelW {
			row = clampCells(row, m.width-(2+launchLabelW))
		}
		b.WriteString(strings.Repeat(" ", 2+launchLabelW) + styleDim.Render(row) + "\n")
	}
	b.WriteString(m.launchFieldLine("name", m.nameValue(), m.isName()))
	b.WriteString(m.launchFieldLine("tag", m.tagValue(), m.isTag()))
	b.WriteString(m.launchFieldLine("agent", m.agentValue(), m.isAgent()))
	for i, spec := range m.optSpecs {
		focused := m.focus == m.optionsIndex()+i
		b.WriteString(m.launchFieldLine(spec.Label, m.optionValue(spec, focused), focused))
	}
	b.WriteString(m.launchFieldLine("prompt", m.promptValue(), m.isPrompt()))
	b.WriteString(m.launchFieldLine("worktree", m.worktreeValue(), m.isWorktree()))

	if note := m.agentNote(); note != "" {
		b.WriteString("\n" + note + "\n")
	}

	b.WriteString("\n")
	if m.errMsg != "" {
		b.WriteString("  " + styleError.Render(m.errMsg) + "\n\n")
	}
	// The contextual field hint is promoted to the router's persistent bottom bar
	// (composeBoard uses m.hint()), so it is no longer rendered inline here.
	return b.String()
}

// hint is the contextual footer for the focused field: text fields prompt to type
// or paste, the agent and choice pickers to use the arrows, and bool options to
// toggle with Space. The tab/enter/esc tail is constant across fields.
func (m launchModel) hint() string {
	const tail = " · ↑↓ next · enter launch · esc cancel"
	if m.isDir() && m.createCwd != "" {
		if m.dirFuzzy {
			return "tab/←→ choose · " + lineEditFastHint + " · enter create directory · esc cancel"
		}
		if m.dirGhost != "" || len(m.dirCands) > 0 {
			return "tab/←→ complete · " + lineEditFastHint + " · enter create directory · esc cancel"
		}
		return lineEditFastHint + " · enter create directory · esc cancel"
	}
	if m.isDir() && (m.dirGhost != "" || len(m.dirCands) > 1) {
		return "tab/←→ complete · " + lineEditFastHint + tail
	}
	if m.isAgent() || m.isChoiceFocused() {
		return "arrows change" + tail
	}
	if m.isWorktree() {
		return "space toggle" + tail
	}
	if _, ok := m.focusedOptionOfType("bool"); ok {
		return "space toggle" + tail
	}
	if m.isDir() {
		return "type or paste · " + lineEditFastHint + tail
	}
	if si, ok := m.focusedOptionOfType("string"); ok && len(m.optSpecs[si].Suggest) > 0 {
		return "type or paste · ←→ suggest · " + lineEditFastHint + tail
	}
	return "type or paste · ←→ move · " + lineEditFastHint + tail
}

// isChoiceFocused reports whether the focused field is a choice option.
func (m launchModel) isChoiceFocused() bool {
	_, ok := m.focusedOptionOfType("choice")
	return ok
}

// agentNote surfaces one short, neutral per-agent note below the form fields:
// which auth a claude launch will inherit from the client env, or a known
// status-signal-quality limitation for adapters that have one. It states facts
// without advice — swarm mirrors the launching terminal (spec scenario 18) and
// never alters the env. Empty when the selected agent has nothing to say.
func (m launchModel) agentNote() string {
	var note string
	switch m.currentAgentName() {
	case "claude":
		if !m.apiKeyInEnv {
			return ""
		}
		note = "auth: ANTHROPIC_API_KEY from env (API billing)"
	case "opencode":
		// R-H4 committee finding: opencode declares no idle rule (T-4), so a
		// settled turn reads unknown and never trips the completion banner.
		note = "status: busy-only (no idle signal)"
	default:
		return ""
	}
	return "  " + styleAccent.Render(note)
}

// fieldLine renders one labelled field, marking the focused one with a bar.
func fieldLine(label, value string, focused bool) string {
	prefix := "  "
	if focused {
		prefix = styleAccent.Render("▌") + " "
	}
	return prefix + styleDim.Render(padLabel(label)) + value + "\n"
}

type launchFieldLayout struct {
	label      string
	valueWidth int
}

// fieldLayout computes one label and value budget together. At ordinary widths
// padLabel remains byte-identical to the established layout. At narrow widths a
// dynamic adapter label is clamped with its separator while reserving at least
// one cell for a focused editor's cursor.
func (m launchModel) fieldLayout(label string) launchFieldLayout {
	padded := padLabel(label)
	if m.width <= 0 {
		return launchFieldLayout{label: padded}
	}
	available := m.width - 2 // two-cell focus prefix
	if available <= 0 {
		return launchFieldLayout{}
	}
	maxLabelWidth := available - 1 // preserve one value/cursor cell
	if maxLabelWidth < 0 {
		maxLabelWidth = 0
	}
	if lipgloss.Width(padded) > maxLabelWidth {
		labelWidth := maxLabelWidth - 1 // separating space
		if labelWidth < 0 {
			labelWidth = 0
		}
		padded = clampCells(label, labelWidth)
		if maxLabelWidth > 0 {
			padded += " "
		}
	}
	valueWidth := available - lipgloss.Width(padded)
	if valueWidth < 0 {
		valueWidth = 0
	}
	return launchFieldLayout{label: padded, valueWidth: valueWidth}
}

func (m launchModel) launchFieldLine(label, value string, focused bool) string {
	prefix := "  "
	if focused {
		prefix = styleAccent.Render("▌") + " "
	}
	layout := m.fieldLayout(label)
	if m.width > 0 {
		value = ansi.Truncate(value, layout.valueWidth, "")
	}
	return prefix + styleDim.Render(layout.label) + value + "\n"
}

// padLabel pads a field label to the label column, always keeping at least one
// separating space before the value. padRight leaves a label as wide as (or wider
// than) the column flush against its value, which jammed codex's 12-char "Sandbox
// mode" into "Sandbox modeworkspace-write" (bead 41b); a sub-column label already
// ends in padding, so this only affects the wide-label case.
func padLabel(label string) string {
	padded := padRight(label, launchLabelW)
	if !strings.HasSuffix(padded, " ") {
		padded += " "
	}
	return padded
}

// focusedEditorValue keeps the insertion cursor inside the field's visible
// value budget. editViewport is rune-safe and pins a cursor that moved into a
// long prefix/suffix to the corresponding edge.
func (m launchModel) focusedEditorValue(label string, editor lineEditor) string {
	if m.width <= 0 {
		return editor.cursorView()
	}
	return editViewport(editor.text, editor.cursor, m.fieldLayout(label).valueWidth)
}

func (m launchModel) dirValue() string {
	if !m.isDir() {
		return m.cwd.text
	}
	v := m.focusedEditorValue("directory", m.cwd)
	if m.dirGhost != "" {
		// An empty ghost must render as nothing, not as an empty-content styled span
		// (lipgloss.Render("") still emits open/close SGR codes), so the style_hoist
		// golden for the no-completion case stays byte-identical.
		ghost := m.dirGhost
		if m.width > 0 {
			budget := m.fieldLayout("directory").valueWidth
			ghost = clampCells(ghost, budget-lipgloss.Width(v))
		}
		if ghost != "" {
			v += styleDim.Render(ghost)
		}
	}
	return v
}

func (m launchModel) agentValue() string {
	if !m.detected {
		return styleDim.Render("checking...")
	}
	full := m.buildAgentRow(true, -1)
	if m.width <= agentRowIndent {
		return full // width unknown / too narrow to budget: leave the row unclamped
	}
	budget := m.width - agentRowIndent
	if lipgloss.Width(full) <= budget {
		return full // fits: byte-identical to the pre-clamp row (the golden stays valid)
	}
	// Overflow (item 5): a crashing agent's long reason must not wrap the row and push the
	// selected agent off-screen. Shed the OTHER agents' reasons first, keeping every
	// dot+name and the selected agent's own reason.
	if trimmed := m.buildAgentRow(false, -1); lipgloss.Width(trimmed) <= budget {
		return trimmed
	}
	// The selected agent's own reason still overflows: clamp just that reason to the space
	// left after the fixed parts (dots, names, flanks, separators, and the "(…)" wrapper).
	noReason := m.buildAgentRow(false, 0)
	selBudget := budget - lipgloss.Width(noReason) - reasonWrapCells
	if selBudget < 1 {
		return clampCells(noReason, budget) // no room for any reason: fit the dots+names alone
	}
	return m.buildAgentRow(false, selBudget)
}

// buildAgentRow renders the agent picker row: each agent as "<mark> <name>[ (reason)]",
// joined and flanked with the cycle-arrow affordances. showOtherReasons includes the
// non-selected agents' reasons; selReasonBudget controls the SELECTED agent's reason
// (<0 = full, >=0 = clamp the reason text to that many cells before wrapping it). This
// lets agentValue shed reasons by priority when the row would overflow the form width.
func (m launchModel) buildAgentRow(showOtherReasons bool, selReasonBudget int) string {
	parts := make([]string, 0, len(m.agents))
	for i, a := range m.agents {
		var mark string
		switch {
		case i == m.agentIdx:
			mark = "●" // selected: the filled dot marks the cursor, usable or not (bead 41b)
		case !a.Installed:
			mark = "✕"
		default:
			mark = "○" // installed (usable or out of supported range) but not the selection
		}
		text := mark + " " + a.Name
		if r := agentReason(a); !a.usable() && r != "" {
			if i == m.agentIdx {
				if selReasonBudget >= 0 {
					r = clampCells(r, selReasonBudget) // plain reason clamped before styling (ANSI-safe)
				}
				if r != "" {
					text += " (" + r + ")"
				}
			} else if showOtherReasons {
				text += " (" + r + ")"
			}
		}
		if a.usable() && i == m.agentIdx {
			text = styleAccent.Render(text)
		} else if !a.usable() {
			text = styleDim.Render(text)
		}
		parts = append(parts, text)
	}
	// Flank the list with arrow affordances so left/right cycling is discoverable.
	return styleDim.Render("◂ ") + strings.Join(parts, "   ") + styleDim.Render(" ▸")
}

func (m launchModel) optionValue(spec adapter.OptionSpec, focused bool) string {
	editor := m.options[spec.Key]
	v := editor.text
	switch spec.Type {
	case "bool":
		box := "[ ]"
		if v == "true" {
			box = "[x]"
		}
		// Show the toggle affordance inline so it is discoverable on the row itself,
		// not only in the focused-field hint bar (bead 3sr).
		return box + " " + styleDim.Render("space")
	case "choice":
		return v + " " + styleDim.Render("▾")
	default: // editable string (possibly with curated suggestions)
		if focused {
			return m.focusedEditorValue(spec.Label, editor)
		}
		if v == "" {
			return styleDim.Render("(default)")
		}
		return v
	}
}

func (m launchModel) nameValue() string {
	v := m.name.text
	if m.isName() {
		return m.focusedEditorValue("name", m.name)
	}
	if v == "" {
		return styleDim.Render("(optional · defaults to agent-dir)")
	}
	return v
}

// tagValue renders the grouping-label row. An empty tag is stated as the no-op it
// is -- the board's tag grouping files an untagged session under "(untagged)" --
// rather than suggesting a default that does not exist.
func (m launchModel) tagValue() string {
	v := m.tag.text
	if m.isTag() {
		return m.focusedEditorValue("tag", m.tag)
	}
	if v == "" {
		return styleDim.Render("(optional · groups the board under o)")
	}
	return v
}

func (m launchModel) promptValue() string {
	v := m.prompt.text
	if m.isPrompt() {
		return m.focusedEditorValue("prompt", m.prompt)
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
	// The worktree checkbox is a bool row: show its toggle affordance inline (bead 3sr).
	return box + " " + styleDim.Render("space · isolate in a git worktree")
}
