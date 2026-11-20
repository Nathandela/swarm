package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/schedule"
)

type scheduleChoice struct {
	value, label string
}

var (
	scheduleKinds       = []scheduleChoice{{schedule.CalendarOnce, "Once"}, {schedule.CalendarDaily, "Daily"}, {schedule.CalendarWeekdays, "Weekdays"}, {schedule.CalendarWeekly, "Weekly"}, {schedule.CalendarCron, "Cron"}}
	scheduleDays        = []scheduleChoice{{"Monday", "Monday"}, {"Tuesday", "Tuesday"}, {"Wednesday", "Wednesday"}, {"Thursday", "Thursday"}, {"Friday", "Friday"}, {"Saturday", "Saturday"}, {"Sunday", "Sunday"}}
	scheduleWorkspaces  = []scheduleChoice{{schedule.WorkspaceDirectory, "Selected directory"}, {schedule.WorkspaceWorktree, "Isolated Git worktree"}}
	scheduleAuthorities = []scheduleChoice{{schedule.AuthorityObserve, "Observe"}, {schedule.AuthorityWorkspaceWrite, "Workspace write"}, {schedule.AuthorityAgentDefault, "Configured agent permissions"}}
	scheduleMissed      = []scheduleChoice{{"skip", "Skip missed runs"}, {"catch-up", "Catch up the latest within 1 hour"}}
	scheduleOverlap     = []scheduleChoice{{"off", "Skip overlaps"}, {"on", "Allow isolated overlaps"}}
	scheduleHarnesses   = []scheduleChoice{{schedule.RouteCodex, "Codex"}, {schedule.RouteClaude, "Claude Code"}, {schedule.RouteAgy, "agy"}, {schedule.RouteHermes, "Hermes"}, {schedule.RouteOpenCode, "OpenCode"}}
)

type scheduleDraft struct {
	name, prompt, directory                                lineEditor
	clock, date, cron                                      lineEditor
	kind, day, zone, workspace, authority, missed, overlap string
	agents                                                 []string
	primary                                                string
	jobID, revision                                        string
	state                                                  schedule.JobState
	grace                                                  time.Duration
	advanced                                               bool
}

func blankSchedule(directory string) scheduleDraft {
	return newScheduleDraft(schedule.JobSpec{
		Directory: directory, Calendar: schedule.Calendar{Kind: schedule.CalendarDaily, TimeZone: schedule.HostTimeZone, Time: "09:00"},
		Workspace: schedule.WorkspaceDirectory, Authority: schedule.AuthorityObserve,
		Agents: []string{schedule.RouteCodex},
	})
}

func newScheduleDraft(spec schedule.JobSpec) scheduleDraft {
	d := scheduleDraft{
		name: newLineEditor(spec.Name), prompt: newLineEditor(spec.Prompt), directory: newLineEditor(spec.Directory),
		clock: newLineEditor(spec.Calendar.Time), date: newLineEditor(spec.Calendar.Date), cron: newLineEditor(spec.Calendar.Cron),
		kind: spec.Calendar.Kind, day: spec.Calendar.Weekday, zone: spec.Calendar.TimeZone,
		workspace: spec.Workspace, authority: spec.Authority,
		missed: "skip", overlap: "off", grace: spec.Grace, agents: slices.Clone(spec.Agents),
	}
	if len(d.agents) != 0 {
		d.primary = d.agents[0]
	}
	if spec.Grace > 0 {
		d.missed = "catch-up"
	}
	if spec.AllowOverlap {
		d.overlap = "on"
	}
	for _, day := range scheduleDays {
		if strings.EqualFold(d.day, day.value) || strings.EqualFold(d.day, day.value[:3]) {
			d.day = day.value
		}
	}
	if d.day == "" {
		d.day = "Monday"
	}
	return d
}

func (d scheduleDraft) spec() schedule.JobSpec {
	calendar := schedule.Calendar{Kind: d.kind, TimeZone: d.zone}
	switch d.kind {
	case schedule.CalendarCron:
		calendar.Cron = strings.TrimSpace(d.cron.text)
	case schedule.CalendarOnce:
		calendar.Date = strings.TrimSpace(d.date.text)
		calendar.Time = strings.TrimSpace(d.clock.text)
	case schedule.CalendarWeekly:
		calendar.Weekday = d.day
		calendar.Time = strings.TrimSpace(d.clock.text)
	default:
		calendar.Time = strings.TrimSpace(d.clock.text)
	}
	grace := d.grace
	if d.missed == "skip" {
		grace = 0
	} else if grace == 0 {
		grace = time.Hour
	}
	return schedule.JobSpec{
		Name: strings.TrimSpace(d.name.text), Prompt: d.prompt.text, Directory: strings.TrimSpace(d.directory.text),
		Workspace: d.workspace, Authority: d.authority, Agents: slices.Clone(d.agents),
		Calendar: calendar, Grace: grace, AllowOverlap: d.overlap == "on",
	}
}

type scheduleField struct {
	id, label string
	text      *lineEditor
	choice    *string
	choices   []scheduleChoice
}

func (d *scheduleDraft) fields() []scheduleField {
	fields := []scheduleField{
		{id: "name", label: "Job name", text: &d.name},
		{id: "prompt", label: "Task instructions", text: &d.prompt},
		{id: "dir", label: "Working directory", text: &d.directory},
		{id: "kind", label: "Cadence", choice: &d.kind, choices: scheduleKinds},
	}
	switch d.kind {
	case schedule.CalendarWeekly:
		fields = append(fields, scheduleField{id: "day", label: "Day", choice: &d.day, choices: scheduleDays})
	case schedule.CalendarOnce:
		fields = append(fields, scheduleField{id: "date", label: "Date (YYYY-MM-DD)", text: &d.date})
	}
	if d.kind == schedule.CalendarCron {
		fields = append(fields, scheduleField{id: "cron", label: "Five-field cron", text: &d.cron})
	} else {
		fields = append(fields, scheduleField{id: "time", label: "Time (HH:MM)", text: &d.clock})
	}
	fields = append(fields,
		scheduleField{id: "zone", label: "Time zone"},
		scheduleField{id: "primary", label: "Primary harness", choice: &d.primary, choices: scheduleHarnesses},
		scheduleField{id: "authority", label: "Authority", choice: &d.authority, choices: scheduleAuthorities},
		scheduleField{id: "advanced", label: "Advanced"},
	)
	if !d.advanced {
		return fields
	}
	fields = append(fields,
		scheduleField{id: "workspace", label: "Workspace", choice: &d.workspace, choices: scheduleWorkspaces},
		scheduleField{id: "overlap", label: "Overlap", choice: &d.overlap, choices: scheduleOverlap},
		scheduleField{id: "missed", label: "Missed runs", choice: &d.missed, choices: scheduleMissed},
	)
	for _, agent := range d.agents {
		if agent != d.primary {
			fields = append(fields, scheduleField{id: "fallback-" + agent, label: scheduleChoiceLabel(agent, scheduleHarnesses)})
		}
	}
	for _, agent := range scheduleHarnesses {
		if !slices.Contains(d.agents, agent.value) {
			fields = append(fields, scheduleField{id: "fallback-" + agent.value, label: agent.label})
		}
	}
	return fields
}

func scheduleChoiceLabel(value string, choices []scheduleChoice) string {
	for _, choice := range choices {
		if choice.value == value {
			return choice.label
		}
	}
	if value == "" {
		return "Choose"
	}
	return value
}

func scheduleZoneLabel(c schedule.Calendar) string {
	if c.TimeZone == schedule.HostTimeZone {
		return "Host wall time"
	}
	return c.TimeZone + " (fixed)"
}

func (s scheduleModel) editorZoneLabel() string {
	if s.draft.zone == schedule.HostTimeZone && s.hostZone != "" {
		return "Host wall time (" + s.hostZone + ")"
	}
	return scheduleZoneLabel(s.draft.spec().Calendar)
}

func scheduleCadence(c schedule.Calendar) string {
	zone := scheduleZoneLabel(c)
	switch c.Kind {
	case schedule.CalendarCron:
		return c.Cron + " · " + zone
	case schedule.CalendarOnce:
		return c.Date + " " + c.Time + " · " + zone
	case schedule.CalendarWeekly:
		return c.Weekday + " " + c.Time + " · " + zone
	}
	return scheduleChoiceLabel(c.Kind, scheduleKinds) + " " + c.Time + " · " + zone
}

func scheduleTextField(label string, text lineEditor, selected bool, width int) string {
	prefix := label + " · "
	text.text = strings.NewReplacer("\n", "↵", "\t", "⇥").Replace(text.text)
	if selected {
		return prefix + editViewport(text.text, text.cursor, max(1, width-2-len([]rune(prefix))))
	}
	return prefix + text.text
}

func (d scheduleDraft) authorityDescription() string {
	switch d.authority {
	case schedule.AuthorityObserve:
		return "Read-only filesystem. Network disabled."
	case schedule.AuthorityWorkspaceWrite:
		return "Writes allowed in the workspace. Network disabled."
	default:
		return "Filesystem and network access follow the harness configuration."
	}
}

func (s scheduleModel) editorContent(width int, agents []AgentInfo) (string, []scheduleRow) {
	d := s.draft
	title := "Automations / New"
	if d.jobID != "" {
		title = "Automations / Edit"
	}
	var rows []scheduleRow
	for _, field := range d.fields() {
		selected := len(rows) == s.sel
		switch {
		case field.text != nil:
			rows = append(rows, scheduleRow{field.id, scheduleTextField(field.label, *field.text, selected, width)})
		case field.choice != nil:
			label := field.label + " · ‹ " + scheduleChoiceLabel(*field.choice, field.choices) + " ›"
			if field.id == "primary" {
				label += scheduleHarnessHint(*field.choice, agents)
			}
			if field.id == "missed" && d.missed == "catch-up" && d.grace > 0 && d.grace != time.Hour {
				label = field.label + " · ‹ Catch up the latest within " + d.grace.String() + " ›"
			}
			rows = append(rows, scheduleRow{field.id, label})
		case field.id == "zone":
			rows = append(rows, scheduleRow{field.id, field.label + " · " + s.editorZoneLabel()})
			if d.zone != schedule.HostTimeZone {
				rows = append(rows, scheduleRow{"convert-zone", "Use host wall time…"})
			}
			rows = append(rows, scheduleTextRows([]string{s.nextFiring}, width)...)
		case field.id == "advanced":
			label := "▸ Advanced · " + scheduleChoiceLabel(d.workspace, scheduleWorkspaces)
			if d.advanced {
				label = "▾ Advanced"
			}
			rows = append(rows, scheduleRow{field.id, label})
		default:
			agent := strings.TrimPrefix(field.id, "fallback-")
			label := "+ Add " + field.label + " fallback"
			if rank := slices.Index(d.agents, agent); rank > 0 {
				label = fmt.Sprintf("Fallback %d · %s", rank, field.label)
			}
			rows = append(rows, scheduleRow{field.id, label + scheduleHarnessHint(agent, agents)})
		}
		if field.id == "dir" && s.resolvedDirectory != "" && s.resolvedDirectory != strings.TrimSpace(d.directory.text) {
			rows = append(rows, scheduleTextRows([]string{"Resolved · " + s.resolvedDirectory}, width)...)
		}
		if field.id == "authority" {
			rows = append(rows, scheduleTextRows([]string{d.authorityDescription(), "Timer · " + s.timerStatus}, width)...)
			if s.preflightPending {
				rows = append(rows, scheduleRow{text: "Checking settings…"})
			}
			for _, notice := range s.preflight.Blocking {
				rows = append(rows, scheduleTextRows([]string{"Needs attention · " + notice}, width)...)
			}
			for _, notice := range s.preflight.Warnings {
				rows = append(rows, scheduleTextRows([]string{"Warning · " + notice}, width)...)
			}
		}
	}
	draftLabel, enableLabel := s.editorSaveLabels()
	rows = append(rows, scheduleRow{"save-enable", enableLabel}, scheduleRow{"save-draft", draftLabel})
	return title, rows
}

func scheduleHarnessHint(route string, agents []AgentInfo) string {
	for _, agent := range agents {
		if !strings.EqualFold(agent.Name, route) || agent.usable() {
			continue
		}
		if agent.Reason != "" {
			return " · " + agent.Reason
		}
		return " · Install or update " + scheduleChoiceLabel(route, scheduleHarnesses)
	}
	return ""
}

func (s scheduleModel) editorSaveLabels() (draft, enable string) {
	if s.draft.state == schedule.JobActive {
		return "Save paused", "Save changes"
	}
	return "Save draft", "Save and enable"
}

func (m rootModel) updateScheduleDraft(key tea.KeyPressMsg, rowID string) (tea.Model, tea.Cmd) {
	s := &m.schedules
	switch key.String() {
	case "ctrl+t":
		return m.showScheduleTemplates(scheduleEditor)
	case "ctrl+e":
		s.template = schedule.Template{}
		s.templateName = newLineEditor(s.draft.name.text)
		s.templatePrompt = newLineEditor(s.draft.prompt.text)
		s.saveDraftTemplate = true
		s.gotoPage(scheduleTemplateEdit)
		return m, nil
	case "ctrl+s":
		return m.requestEditorEnable()
	case "ctrl+d":
		return m.saveSchedule(false, false)
	case "ctrl+r":
		return m.checkScheduleEditor()
	}
	if key.String() == "enter" || key.String() == "space" {
		switch rowID {
		case "save-enable":
			return m.requestEditorEnable()
		case "save-draft":
			return m.saveSchedule(false, false)
		case "advanced":
			s.draft.advanced = !s.draft.advanced
			return m, nil
		case "convert-zone":
			s.gotoPage(scheduleConvertZone)
			return m, nil
		}
	}

	fields := s.draft.fields()
	index := slices.IndexFunc(fields, func(field scheduleField) bool { return field.id == rowID })
	if index < 0 {
		return m, nil
	}
	field := fields[index]
	if field.text != nil {
		if key.String() == "alt+enter" && field.id == "prompt" {
			field.text.insert("\n")
			return m.checkScheduleEditor()
		}
		if field.text.update(key).changed {
			return m.checkScheduleEditor()
		}
	}
	if field.choice != nil && (key.Code == tea.KeyLeft || key.Code == tea.KeyRight || key.Code == ' ') {
		previousPrimary := s.draft.primary
		delta := 1
		if key.Code == tea.KeyLeft {
			delta = -1
		}
		stepScheduleChoice(field, delta)
		if field.id == "primary" && previousPrimary != s.draft.primary {
			next := []string{s.draft.primary}
			for _, route := range s.draft.agents {
				if route != previousPrimary && route != s.draft.primary {
					next = append(next, route)
				}
			}
			s.draft.agents = next
		}
		return m.checkScheduleEditor()
	}
	if strings.HasPrefix(rowID, "fallback-") {
		agent := strings.TrimPrefix(rowID, "fallback-")
		rank := slices.Index(s.draft.agents, agent)
		switch key.Code {
		case ' ', tea.KeyEnter:
			if rank > 0 {
				s.draft.agents = slices.Delete(slices.Clone(s.draft.agents), rank, rank+1)
			} else if rank < 0 {
				s.draft.agents = append(slices.Clone(s.draft.agents), agent)
			}
		case '-', '+':
			next := rank + 1
			if key.Code == '-' {
				next = rank - 1
			}
			if rank <= 0 || next <= 0 || next >= len(s.draft.agents) {
				return m, nil
			}
			s.draft.agents = slices.Clone(s.draft.agents)
			s.draft.agents[rank], s.draft.agents[next] = s.draft.agents[next], s.draft.agents[rank]
		default:
			return m, nil
		}
		for i, row := range m.scheduleLayout().rows {
			if row.id == rowID {
				s.sel = i
				break
			}
		}
		return m.checkScheduleEditor()
	}
	if key.String() == "enter" {
		s.move(1, m.scheduleLayout())
	}
	return m, nil
}

func stepScheduleChoice(field scheduleField, delta int) {
	current := slices.IndexFunc(field.choices, func(candidate scheduleChoice) bool { return candidate.value == *field.choice })
	if current < 0 {
		current = len(field.choices) - 1
		if delta < 0 {
			current = 0
		}
	}
	next := (current + len(field.choices) + delta) % len(field.choices)
	*field.choice = field.choices[next].value
}
