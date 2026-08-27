package tui

import (
	"fmt"
	"os"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// handoffModel is the deliberately small handoff form. It captures the source
// session by identity and exposes only the receiving CLI, its model, the
// supervision mode (ADR-010 Amendment 3 C1) and the method (Amendment 4 E2).
// Launch scope, permission bypasses, directories, prompts, and worktree choices do
// not belong here: B2's second half stands for both methods, so hands-off is not
// the door through which a sandbox or permission-bypass flag arrives.
type handoffModel struct {
	sourceID string
	// The source's identity, FROZEN when the form opens. sourceAgent decides the E4
	// cross-CLI confirmation and sourceCwd is where the successor is launched;
	// neither can change for a given session, and freezing them keeps a submission
	// independent of a roster row that may vanish under it.
	sourceAgent string
	sourceCwd   string
	// sourceRunning drives the E6 warning. Frozen with the method and for the same
	// reason: the form is one snapshot the human decides against, and the warning is
	// worded as "may still be running" precisely because swarm does not re-check it.
	sourceRunning bool

	agents      []AgentInfo
	targetIdx   int
	detected    bool
	model       string
	supervision string
	method      string
	focus       int // 0 target, 1 model, 2 supervision, 3 method

	// confirmTarget and confirmModel are the pending E4 disclosure confirmation, and they
	// hold WHAT WAS ASKED rather than merely THAT something was asked. A bare boolean
	// authorized "a launch" instead of "this launch": a detectMsg arriving while the
	// question was on screen could reselect a different target underneath it, and the
	// human's yes would then apply to a CLI they were never shown. A disclosure decision
	// has to name what it authorizes. Empty confirmTarget means nothing is pending.
	confirmTarget string
	confirmModel  string

	errMsg string
	width  int
}

// handoffSupervisionModes is the closed vocabulary the form cycles through, passive
// first because it is the default.
var handoffSupervisionModes = []string{protocol.SupervisionPassive, protocol.SupervisionManual, protocol.SupervisionNone}

// The two handoff methods (ADR-010 Amendment 4). supervised asks the source agent to
// author context and invoke the CLI itself; hands-off asks the source for nothing at
// all and launches a successor that is told where the old conversation lives.
const (
	handoffMethodSupervised = "supervised"
	handoffMethodHandsOff   = "hands-off"
)

var handoffMethods = []string{handoffMethodSupervised, handoffMethodHandsOff}

func newHandoffModel(source protocol.SessionView, agents []AgentInfo, detected bool, width int) handoffModel {
	m := handoffModel{
		sourceID:      source.ID,
		sourceAgent:   source.Agent,
		sourceCwd:     source.Cwd,
		sourceRunning: source.Status.Process == status.ProcessRunning,
		agents:        append([]AgentInfo(nil), agents...),
		detected:      detected,
		supervision:   protocol.SupervisionPassive,
		method:        handoffMethodSupervised,
		width:         width,
	}
	// ADR-010 Amendment 4 E2: B3's eligibility predicate is the method's DEFAULT, not
	// a gate on the feature. It is evaluated ONCE, here, and then frozen for the life
	// of the form -- a roster event arriving while the form is open must never change
	// the branch Enter is about to take, or the human performs an action they did not
	// see. Only the human changes the method after this point.
	if allowed, _ := handoffSourceEligibility(source); !allowed {
		m.method = handoffMethodHandsOff
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
	// Belt to the frozen pair's braces: if a pending disclosure confirmation's subject
	// moves underneath it, cancel it outright and say so, rather than leaving a question
	// on screen that names one target while the form has selected another.
	defer func() {
		if m.confirmPending() && (m.targetName() != m.confirmTarget || m.model != m.confirmModel) {
			m.confirmTarget, m.confirmModel = "", ""
			m.errMsg = "target changed while the cross-CLI confirmation was open - nothing launched, review and submit again"
		}
	}()
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

func (m *handoffModel) cycleSupervision(forward bool) {
	m.supervision = cycleValue(handoffSupervisionModes, m.supervision, forward)
	m.errMsg = ""
}

// confirmPending reports whether an E4 disclosure confirmation is awaiting an answer.
func (m handoffModel) confirmPending() bool { return m.confirmTarget != "" }

func (m *handoffModel) cycleMethod(forward bool) {
	m.method = cycleValue(handoffMethods, m.method, forward)
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
	title := styleTitle.Render("swarm") + styleDim.Render(" · handoff")
	b.WriteString(m.clampLine(title) + "\n\n")
	b.WriteString(m.fieldLine("target", m.targetValue(), m.focus == 0))
	b.WriteString(m.fieldLine("model", m.modelValue(), m.focus == 1))
	b.WriteString(m.fieldLine("supervision", m.supervisionValue(), m.focus == 2))
	b.WriteString(m.fieldLine("method", m.methodValue(), m.focus == 3))
	// E6: two live writers in one checkout. The source is left alive on purpose, so
	// the mitigation is honesty, not enforcement -- this warns and never blocks.
	if m.method == handoffMethodHandsOff && m.sourceRunning {
		b.WriteString("\n" + m.clampLine("  "+styleDim.Render(handoffRunningWarning)) + "\n")
	}
	// E4: the disclosure question, naming both CLIs. It is a separate thing from the
	// warning above -- that one is informational, this one blocks the submit.
	if m.confirmPending() {
		b.WriteString("\n" + m.clampLine("  "+styleAmber.Render(m.crossCLIQuestion())) + "\n")
	}
	if m.errMsg != "" {
		b.WriteString("\n" + m.clampLine("  "+styleError.Render(m.errMsg)) + "\n")
	}
	return b.String()
}

// handoffRunningWarning is E6's honesty. It is deliberately in the conditional: the
// source is left alive on purpose and its state is frozen when the form opens, so
// the form says what MAY be true rather than asserting a fact swarm did not re-check
// at render time. The daemon-composed prompt tells the successor the same thing.
const handoffRunningWarning = "the source may still be running and editing this checkout - the successor is told to check git status first"

func (m handoffModel) crossCLIQuestion() string {
	return "cross-CLI handoff: " + m.confirmTarget + " will be told to read a transcript written by " +
		m.sourceAgent + " - y confirm, n cancel"
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

func (m handoffModel) supervisionValue() string {
	// E3: a hands-off handoff has no supervisor by construction -- no agent was ever
	// in a position to choose one -- so the child's supervision is left EMPTY rather
	// than `none`, and this field has nothing to say. Rendering the cycled value here
	// would promise a supervision relationship the launch does not create.
	if m.method == handoffMethodHandsOff {
		return styleDim.Render("not applicable - no supervisor exists")
	}
	return styleDim.Render("◂ ") + styleAmber.Render(m.supervision) + styleDim.Render(" ▸")
}

func (m handoffModel) methodValue() string {
	return styleDim.Render("◂ ") + styleAmber.Render(m.method) + styleDim.Render(" ▸")
}

func (m handoffModel) hint() string {
	if m.confirmPending() {
		return "y confirm the cross-CLI transcript disclosure · n or esc cancel"
	}
	switch m.focus {
	case 0:
		return "arrows change target · tab/↑↓ next · enter continue · esc cancel"
	case 2:
		return "arrows change supervision · tab/↑↓ next · enter continue · esc cancel"
	case 3:
		return "arrows change method · tab/↑↓ next · enter continue · esc cancel"
	}
	if spec, ok := m.modelSpec(); ok && spec.Type == "string" {
		return "type, paste, or use arrows · tab/↑↓ next · enter continue · esc cancel"
	}
	return "arrows change model · tab/↑↓ next · enter continue · esc cancel"
}

func (m rootModel) updateHandoff(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	h := &m.handoff
	// The E4 disclosure confirmation is MODAL while it is open, like the board's
	// kill/delete confirm: it decides whether one vendor's raw transcript is shown to
	// another, so no keypress may leak into the form beneath it and nothing but an
	// explicit yes launches. Cancelling launches nothing at all.
	if h.confirmPending() {
		switch {
		case k.Text == "y":
			target, model := h.confirmTarget, h.confirmModel
			h.confirmTarget, h.confirmModel = "", ""
			// The FROZEN pair, not the live selection: what launches is what was shown.
			return m.launchHandsOffWith(target, model)
		case k.Text == "n", k.Code == tea.KeyEsc:
			h.confirmTarget, h.confirmModel = "", ""
		}
		return m, nil
	}
	switch {
	case k.Code == tea.KeyEsc:
		return m, m.enterGeneral()
	case k.Code == tea.KeyEnter:
		return m.submitHandoff()
	case k.Code == tea.KeyTab || k.Code == tea.KeyDown:
		h.focus = (h.focus + 1) % 4
	case k.Code == tea.KeyUp:
		h.focus = (h.focus + 3) % 4
	case k.Code == tea.KeyLeft, k.Code == tea.KeyRight:
		forward := k.Code == tea.KeyRight
		switch h.focus {
		case 0:
			h.cycleTarget(forward)
		case 1:
			h.cycleModel(forward)
		case 2:
			h.cycleSupervision(forward)
		default:
			h.cycleMethod(forward)
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
	if h.method == handoffMethodHandsOff {
		return m.submitHandsOff()
	}
	// SUPERVISED, unchanged. Amendment 4 E2 explicitly leaves B3's revalidation
	// clause alone for this method: the captured row is re-resolved immediately
	// before submission and a permission request, an active or unknown turn, and an
	// ended process are still refused with the same separate messages. What changed
	// is only what a refusal MEANS -- the other method is one field away in the same
	// form -- so the refusal no longer needs to be the end of the road.
	if allowed, why := handoffSourceEligibility(source); !allowed {
		h.errMsg = why
		return m, nil
	}
	if h.targetIdx < 0 || h.targetIdx >= len(h.agents) || !h.agents[h.targetIdx].usable() {
		h.errMsg = "no installed, supported target agent"
		return m, nil
	}
	prompt, err := renderHandoffPrompt(h.targetName(), h.model, h.supervision)
	if err != nil {
		h.errMsg = err.Error()
		return m, nil
	}
	cmd := handoffCmd(m.client, h.sourceID, prompt)
	return m, tea.Batch(cmd, m.enterGeneral())
}

// submitHandsOff performs the checks the hands-off method owns, then either takes
// the E4 confirmation or launches. Every refusal here is NAMED and launches nothing
// (E7): a refusal that degraded to a bare launch would put an agent in the owner's
// checkout with no idea what it is continuing, which the design names as the worst
// outcome available -- worse than no handoff, since the owner would believe the work
// was carried over.
func (m rootModel) submitHandsOff() (tea.Model, tea.Cmd) {
	h := &m.handoff
	if h.targetIdx < 0 || h.targetIdx >= len(h.agents) || !h.agents[h.targetIdx].usable() {
		h.errMsg = "no installed, supported target agent"
		return m, nil
	}
	// The compatibility boundary (protocol.md, E7): an older daemon does not know the
	// handoff_from option key and would silently IGNORE it, performing a bare,
	// context-free launch. Refuse visibly instead -- and refuse BEFORE the disclosure
	// question, so the owner is never asked to approve a disclosure that cannot happen.
	//
	// The TUI offers this capability at hello (cmd/swarm/main.go tuiCaps), on both its
	// startup dial and the post-auto-upgrade one, so reaching here means the DAEMON
	// did not intersect it -- an older daemon, not a misconfigured client. The message
	// says so, and points at the supervised method, which is one field away and works
	// against any daemon.
	if !handsOffNegotiated(m.client) {
		h.errMsg = "daemon is too old for hands-off handoff - restart the daemon, or use the supervised method"
		return m, nil
	}
	// E4: pointing one CLI at another's raw transcript is a disclosure decision, so
	// the owner takes it knowingly at the form. The rule is target CLI != source CLI
	// and deliberately not a CLI-to-vendor map: opencode and agy route to different
	// providers depending on the chosen model, so the CLI name is not a reliable
	// vendor boundary and the conservative rule is the honest one. A same-CLI handoff
	// takes no confirmation -- the content reaches nobody new.
	if h.targetName() != h.sourceAgent {
		h.confirmTarget, h.confirmModel = h.targetName(), h.model
		return m, nil
	}
	return m.launchHandsOffWith(h.targetName(), h.model)
}

// launchHandsOffWith issues the one launch against an EXPLICIT target and model rather
// than re-reading the form, so a confirmed launch cannot drift between the question and
// the answer.
func (m rootModel) launchHandsOffWith(target, model string) (tea.Model, tea.Cmd) {
	h := &m.handoff
	cmd := handsOffLaunchCmd(m.client, h.sourceID, h.sourceCwd, target, model, m.width, m.height)
	return m, tea.Batch(cmd, m.enterGeneral())
}

// handsOffNegotiated reports whether the daemon negotiated CapHandsOffHandoff at
// hello. The TUI's Client interface deliberately does not carry the accessor -- the
// concrete *protocol.Client does -- so it is discovered by type assertion, the same
// shape `swarm reattach` uses for external-resume. FAIL-CLOSED: a client that cannot
// even be asked counts as a daemon that did not negotiate it.
func handsOffNegotiated(c Client) bool {
	capable, ok := c.(interface{ Capabilities() []string })
	return ok && slices.Contains(capable.Capabilities(), protocol.CapHandsOffHandoff)
}

// handsOffLaunchCmd issues the hands-off handoff's ONE launch (E1). It carries the
// NAMESPACED source id in handoff_from -- protocol.md's shape; the daemon resolves it
// and converts it to the LOCAL id for spawned_from -- plus the form's chosen model.
// It stamps no lineage of its own: spawned_from, spawn_intent and the deliberately
// empty supervision (E3) are the daemon's to write, and the whole prompt is composed
// daemon-side from an embedded template so no client can forge it (E5). The source is
// never signalled: this command's only call is Launch.
func handsOffLaunchCmd(c Client, sourceID, cwd, agent, model string, cols, rows int) tea.Cmd {
	options := map[string]string{protocol.OptionHandoffFrom: sourceID}
	// An empty model means the agent's OWN default, so the key is omitted rather than
	// sent empty -- the form's selection is carried when there is one, and absence
	// means absence.
	if model != "" {
		options["model"] = model
	}
	if cols <= 0 {
		cols = defaultResumeCols
	}
	if rows <= 0 {
		rows = defaultResumeRows
	}
	req := protocol.LaunchReq{
		Agent:   agent,
		Cwd:     cwd,
		Options: options,
		Env:     os.Environ(),
		Cols:    cols,
		Rows:    rows,
	}
	return func() tea.Msg {
		id, name, err := c.Launch(req)
		return launchResultMsg{id: id, agent: agent, name: name, err: err}
	}
}

// handoffSourceEligibility uses raw status dimensions rather than the display group.
// In particular, needs_input intentionally combines ordinary prompts and permission
// requests; only the former is safe to receive an automatically submitted instruction.
//
// Amendment 4 E2 narrowed what this predicate DECIDES. It is no longer a gate on the
// handoff feature -- `h` opens the form on any row -- and it no longer gates the
// hands-off method at all. It is the method's default suggestion at open, and the
// supervised method's revalidation at submit; both callers are in this file.
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
