package tui

import (
	"fmt"
	"os"
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
	scheduleKinds       = []scheduleChoice{{schedule.CalendarOnce, "Once"}, {schedule.CalendarDaily, "Daily"}, {schedule.CalendarWeekdays, "Weekdays"}, {schedule.CalendarWeekly, "Weekly"}, {schedule.CalendarCron, "Advanced cron"}}
	scheduleDays        = []scheduleChoice{{"Monday", "Monday"}, {"Tuesday", "Tuesday"}, {"Wednesday", "Wednesday"}, {"Thursday", "Thursday"}, {"Friday", "Friday"}, {"Saturday", "Saturday"}, {"Sunday", "Sunday"}}
	scheduleWorkspaces  = []scheduleChoice{{schedule.WorkspaceWorktree, "Isolated Git worktree"}, {schedule.WorkspaceDirectory, "Selected directory"}}
	scheduleAuthorities = []scheduleChoice{{schedule.AuthorityObserve, "Observation and report"}, {schedule.AuthorityWorkspaceWrite, "Workspace changes for review"}, {schedule.AuthorityAgentDefault, "Configured agent permissions"}}
	scheduleMissed      = []scheduleChoice{{"skip", "Skip missed runs"}, {"catch-up", "Catch up latest within 1 hour"}}
	scheduleOverlap     = []scheduleChoice{{"off", "Skip overlapping runs"}, {"on", "Allow independent Git worktrees"}}
	scheduleAgents      = []string{schedule.RouteCodex, schedule.RouteClaude, schedule.RouteHermes, schedule.RouteAgy, schedule.RouteOpenCode}
)

type scheduleDraft struct {
	name, prompt, directory                          lineEditor
	zone, clock, date, cron                          lineEditor
	kind, day, workspace, authority, missed, overlap string
	agents                                           []string
	jobID, revision                                  string
	grace                                            time.Duration
}

func blankSchedule(directory string) scheduleDraft {
	return newScheduleDraft(schedule.JobSpec{
		Directory:    directory,
		Calendar:     schedule.Calendar{TimeZone: scheduleHostTimeZone()},
		AllowOverlap: true,
	})
}

func scheduleHostTimeZone() string {
	localtime, _ := os.Readlink("/etc/localtime")
	etcTimezone, _ := os.ReadFile("/etc/timezone")
	for _, candidate := range []string{os.Getenv("TZ"), localtime, string(etcTimezone)} {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), ":")
		if _, name, found := strings.Cut(candidate, "/zoneinfo/"); found {
			candidate = name
		}
		if candidate == "" || candidate == "Local" {
			continue
		}
		if _, err := time.LoadLocation(candidate); err == nil {
			return candidate
		}
	}
	return "UTC"
}

func newScheduleDraft(spec schedule.JobSpec) scheduleDraft {
	d := scheduleDraft{
		name: newLineEditor(spec.Name), prompt: newLineEditor(spec.Prompt), directory: newLineEditor(spec.Directory),
		zone: newLineEditor(spec.Calendar.TimeZone), clock: newLineEditor(spec.Calendar.Time),
		date: newLineEditor(spec.Calendar.Date), cron: newLineEditor(spec.Calendar.Cron),
		kind: spec.Calendar.Kind, day: spec.Calendar.Weekday,
		workspace: spec.Workspace, authority: spec.Authority,
		missed: "skip", overlap: "off", grace: spec.Grace, agents: slices.Clone(spec.Agents),
	}
	if d.kind == "" {
		d.kind = schedule.CalendarDaily
	}
	if d.zone.text == "" {
		d.zone.set("UTC")
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
	return d
}

func (d scheduleDraft) spec() schedule.JobSpec {
	calendar := schedule.Calendar{Kind: d.kind, TimeZone: strings.TrimSpace(d.zone.text)}
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

func (d *scheduleDraft) fields(page schedulePage) []scheduleField {
	switch page {
	case scheduleTask:
		return []scheduleField{
			{id: "name", label: "Job name", text: &d.name},
			{id: "prompt", label: "Instructions", text: &d.prompt},
			{id: "dir", label: "Working directory", text: &d.directory},
		}
	case scheduleCalendar:
		fields := []scheduleField{{id: "kind", label: "Frequency", choice: &d.kind, choices: scheduleKinds}}
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
		return append(fields,
			scheduleField{id: "zone", label: "IANA time zone", text: &d.zone},
			scheduleField{id: "missed", label: "Missed runs", choice: &d.missed, choices: scheduleMissed},
		)
	case scheduleRoute:
		fields := []scheduleField{
			{id: "workspace", label: "Workspace", choice: &d.workspace, choices: scheduleWorkspaces},
			{id: "authority", label: "Authority", choice: &d.authority, choices: scheduleAuthorities},
			{id: "overlap", label: "Overlap", choice: &d.overlap, choices: scheduleOverlap},
		}
		for _, agent := range d.agents {
			fields = append(fields, scheduleField{id: agent, label: agent})
		}
		for _, agent := range scheduleAgents {
			if !slices.Contains(d.agents, agent) {
				fields = append(fields, scheduleField{id: agent, label: agent})
			}
		}
		return fields
	}
	return nil
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

func (d scheduleDraft) content(page schedulePage, selected, width int) (string, []scheduleRow) {
	mode := "New"
	if d.jobID != "" {
		mode = "Edit"
	}
	label := ""
	switch page {
	case scheduleTask:
		label = "1/4 · Task"
	case scheduleCalendar:
		label = "2/4 · Schedule"
	case scheduleRoute:
		label = "3/4 · Agent and workspace"
	}
	fields := d.fields(page)
	rows := make([]scheduleRow, 0, len(fields))
	for i, field := range fields {
		var line string
		switch {
		case field.text != nil:
			line = scheduleTextField(field.label, *field.text, i == selected, width)
		case field.choice != nil:
			line = field.label + " · ‹ " + scheduleChoiceLabel(*field.choice, field.choices) + " ›"
		default:
			if rank := slices.Index(d.agents, field.id); rank >= 0 {
				line = fmt.Sprintf("%d. %s", rank+1, field.label)
			} else {
				line = "+ " + field.label
			}
		}
		rows = append(rows, scheduleRow{field.id, line})
	}
	return "Automations / " + mode + " · " + label, rows
}

func scheduleTextField(label string, text lineEditor, selected bool, width int) string {
	prefix := label + " · "
	// Preserve rune offsets when a multiline prompt is represented on one row.
	text.text = strings.NewReplacer("\n", "↵", "\t", "⇥").Replace(text.text)
	if selected {
		return prefix + editViewport(text.text, text.cursor, max(1, width-2-len([]rune(prefix))))
	}
	return prefix + text.text
}

func scheduleCadence(c schedule.Calendar) string {
	switch c.Kind {
	case schedule.CalendarCron:
		return c.Cron + " · " + c.TimeZone
	case schedule.CalendarOnce:
		return c.Date + " " + c.Time + " · " + c.TimeZone
	case schedule.CalendarWeekly:
		return c.Weekday + " " + c.Time + " · " + c.TimeZone
	}
	return scheduleChoiceLabel(c.Kind, scheduleKinds) + " " + c.Time + " · " + c.TimeZone
}

func (m rootModel) updateScheduleDraft(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := &m.schedules
	fields := s.draft.fields(s.page)
	if s.sel < 0 || s.sel >= len(fields) {
		return m, nil
	}
	field := fields[s.sel]
	switch key.String() {
	case "enter":
		if s.page == scheduleRoute {
			return m.previewSchedule()
		}
		if s.page == scheduleTask && (strings.TrimSpace(s.draft.name.text) == "" || strings.TrimSpace(s.draft.prompt.text) == "" || strings.TrimSpace(s.draft.directory.text) == "") {
			s.errorText = "Enter a name, instructions, and directory."
			return m, nil
		}
		if s.page == scheduleCalendar {
			if _, err := s.draft.spec().Calendar.Next(time.Now()); err != nil {
				s.errorText = err.Error()
				return m, nil
			}
		}
		s.gotoPage(s.page + 1)
		return m, nil
	case "ctrl+t":
		if s.page == scheduleTask {
			return m.showScheduleTemplates(scheduleTask)
		}
	case "ctrl+s":
		if s.page == scheduleTask {
			s.template = schedule.Template{}
			s.templateName = newLineEditor(s.draft.name.text)
			s.templatePrompt = newLineEditor(s.draft.prompt.text)
			s.saveDraftTemplate = true
			s.gotoPage(scheduleTemplateEdit)
			return m, nil
		}
	}
	if field.text != nil {
		if key.String() == "alt+enter" && field.id == "prompt" {
			field.text.insert("\n")
			return m, nil
		}
		if field.text.update(key).changed {
			s.errorText = ""
		}
		return m, nil
	}
	if field.choice != nil {
		if key.Code == tea.KeyLeft {
			stepScheduleChoice(field, -1)
		}
		if key.Code == tea.KeyRight || key.Code == ' ' {
			stepScheduleChoice(field, 1)
		}
		return m, nil
	}
	rank := slices.Index(s.draft.agents, field.id)
	switch key.Code {
	case ' ':
		if rank >= 0 {
			s.draft.agents = slices.Delete(slices.Clone(s.draft.agents), rank, rank+1)
		} else {
			s.draft.agents = append(slices.Clone(s.draft.agents), field.id)
		}
	case '-', '+':
		next := rank + 1
		if key.Code == '-' {
			next = rank - 1
		}
		if rank < 0 || next < 0 || next >= len(s.draft.agents) {
			return m, nil
		}
		s.draft.agents = slices.Clone(s.draft.agents)
		s.draft.agents[rank], s.draft.agents[next] = s.draft.agents[next], s.draft.agents[rank]
	default:
		return m, nil
	}
	for i, candidate := range s.draft.fields(s.page) {
		if candidate.id == field.id {
			s.sel = i
			break
		}
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

func (m rootModel) previewSchedule() (tea.Model, tea.Cmd) {
	spec := m.schedules.draft.spec()
	config := m.scheduleConfig
	return m.scheduleRequest(func() scheduleResultMsg {
		spec, err := schedule.PrepareJob(spec)
		if err != nil {
			return scheduleResultMsg{err: err}
		}
		if config.Validate != nil {
			if err := config.Validate(spec); err != nil {
				return scheduleResultMsg{err: err}
			}
		}
		loc, err := time.LoadLocation(spec.Calendar.TimeZone)
		if err != nil {
			return scheduleResultMsg{err: err}
		}
		next, err := spec.Calendar.Next(time.Now())
		if err != nil {
			return scheduleResultMsg{err: err}
		}
		if next.IsZero() {
			return scheduleResultMsg{err: fmt.Errorf("schedule has no future trigger")}
		}
		preview := []string{spec.Name, scheduleCadence(spec.Calendar), "Directory · " + spec.Directory,
			"Workspace · " + scheduleChoiceLabel(spec.Workspace, scheduleWorkspaces),
			"Authority · " + scheduleChoiceLabel(spec.Authority, scheduleAuthorities),
			"Agent order · " + strings.Join(spec.Agents, " → "),
			"Next triggers"}
		for i := 0; i < 3 && !next.IsZero(); i++ {
			preview = append(preview, "  "+next.In(loc).Format("2006-01-02 15:04 MST"))
			next, err = spec.Calendar.Next(next)
			if err != nil {
				return scheduleResultMsg{err: err}
			}
		}
		prompt := spec.Prompt
		if config.Prompt != nil {
			prompt = config.Prompt(prompt)
		}
		missed := "Skip missed runs"
		if spec.Grace > 0 {
			missed = "Catch up latest within " + spec.Grace.String()
		}
		overlap := "Skip overlapping runs"
		if spec.AllowOverlap {
			overlap = "Allow independent Git worktrees"
		}
		preview = append(preview, missed, overlap, "", "Dispatched prompt", prompt)
		return scheduleResultMsg{page: scheduleReview, preview: strings.Join(preview, "\n")}
	})
}

func (m rootModel) saveSchedule() (tea.Model, tea.Cmd) {
	catalog, draft, config := m.schedules.catalog, m.schedules.draft, m.scheduleConfig
	spec := draft.spec()
	return m.scheduleRequest(func() scheduleResultMsg {
		if config.Validate != nil {
			if err := config.Validate(spec); err != nil {
				return scheduleResultMsg{err: err}
			}
		}
		now := time.Now()
		var job schedule.Job
		var err error
		if draft.jobID == "" {
			job, err = catalog.CreateJob(spec, now)
		} else {
			job, err = catalog.EditJobRevision(draft.jobID, draft.revision, spec, now)
		}
		return scheduleResultMsg{page: scheduleJob, job: job, err: err}
	})
}
