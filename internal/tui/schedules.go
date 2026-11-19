package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/charmbracelet/x/ansi"
)

type ScheduleCatalog interface {
	Jobs() ([]schedule.Job, error)
	Job(string) (schedule.Job, error)
	CreateJob(schedule.JobSpec, time.Time) (schedule.Job, error)
	EditJobRevision(string, string, schedule.JobSpec, time.Time) (schedule.Job, error)
	SetJobState(string, schedule.JobState, time.Time) (schedule.Job, error)
	History(string) ([]schedule.Occurrence, error)
	Templates() ([]schedule.Template, error)
	CreateTemplate(string, string, time.Time) (schedule.Template, error)
	EditTemplate(string, string, string, time.Time) (schedule.Template, error)
	DeleteTemplate(string) error
}

type ScheduleConfig struct {
	Open          func() (ScheduleCatalog, error)
	Validate      func(schedule.JobSpec) error
	ValidateRoute func(agent, authority string) error
	Prompt        func(string) string
	TimerStatus   func() (string, error)
	InstallTimer  func() error
}

func WithSchedules(config ScheduleConfig) Option {
	return func(m *rootModel) { m.scheduleConfig = config }
}

type schedulePage uint8

const (
	scheduleList schedulePage = iota
	scheduleTask
	scheduleCalendar
	scheduleRoute
	scheduleReview
	scheduleJob
	scheduleHistory
	scheduleRun
	scheduleArchive
	scheduleTemplates
	scheduleTemplateEdit
	scheduleTemplateDelete
	scheduleTimerEnable
)

type scheduleModel struct {
	directoryShortcut            bool
	returnFromAttach             bool
	timerStatus                  string
	canInstallTimer              bool
	catalog                      ScheduleCatalog
	page                         schedulePage
	archived                     bool
	busy                         bool
	errorText                    string
	jobs                         []schedule.Job
	job                          schedule.Job
	runs                         []schedule.Occurrence
	run                          schedule.Occurrence
	pendingRuns                  map[string]string // Job ID to operation ID awaiting a run record.
	templates                    []schedule.Template
	template                     schedule.Template
	templateName, templatePrompt lineEditor
	templateReturn               schedulePage
	saveDraftTemplate            bool
	draft                        scheduleDraft
	draftReturn                  schedulePage
	preview                      string
	sel, top                     int
	mouseTarget                  string
	mouseX, mouseY               int
}

type scheduleOpenResultMsg struct {
	result     launchResultMsg
	connection uint64
}

type scheduleResultMsg struct {
	timerStatus string
	catalog     ScheduleCatalog
	jobs        []schedule.Job
	job         schedule.Job
	runs        []schedule.Occurrence
	templates   []schedule.Template
	runID       string
	runJobID    string
	preview     string
	page        schedulePage
	err         error
}

func (m rootModel) openSchedules(directory string) (tea.Model, tea.Cmd) {
	m.general.more = nil
	m.screen = screenSchedules
	m.schedules = scheduleModel{
		canInstallTimer: m.scheduleConfig.InstallTimer != nil, directoryShortcut: directory != "",
		pendingRuns: m.schedules.pendingRuns,
	}
	to := scheduleList
	if directory != "" {
		m.schedules.draft = blankSchedule(directory)
		to = scheduleTask
	}
	open, timer := m.scheduleConfig.Open, m.scheduleConfig.TimerStatus
	return m.scheduleRequest(func() scheduleResultMsg {
		catalog, err := open()
		if err != nil {
			return scheduleResultMsg{err: err}
		}
		if to == scheduleTask {
			return scheduleResultMsg{catalog: catalog, page: to, timerStatus: scheduleTimerStatus(timer)}
		}
		jobs, err := catalog.Jobs()
		return scheduleResultMsg{catalog: catalog, jobs: jobs, page: to, err: err, timerStatus: scheduleTimerStatus(timer)}
	})
}

func (m rootModel) scheduleRequest(request func() scheduleResultMsg) (tea.Model, tea.Cmd) {
	m.schedules.busy = true
	m.schedules.errorText = ""
	return m, func() tea.Msg { return request() }
}

func (m rootModel) applyScheduleResult(result scheduleResultMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenSchedules || !m.schedules.busy {
		return m, nil
	}
	s := &m.schedules
	s.busy = false
	if result.catalog != nil {
		s.catalog = result.catalog
	}
	if result.err != nil {
		s.errorText = result.err.Error()
		return m, nil
	}
	s.gotoPage(result.page)
	if result.timerStatus != "" {
		s.timerStatus = result.timerStatus
	}
	switch result.page {
	case scheduleList:
		s.jobs = result.jobs
	case scheduleJob:
		s.job = result.job
	case scheduleHistory:
		s.runs = result.runs
	case scheduleRun:
		s.runs = result.runs
		for _, run := range s.runs {
			if run.ID == result.runID {
				delete(s.pendingRuns, result.runJobID)
				s.run = run
				return m, nil
			}
		}
		// The attempt may be updated by a worker, but a persisted occurrence
		// remains addressable from the run page for as long as it exists.
		if result.runJobID != "" {
			s.gotoPage(scheduleJob)
			s.errorText = "Run record is unavailable. Retry Run now to recover it."
		} else {
			s.gotoPage(scheduleHistory)
			s.errorText = "Run record is unavailable."
		}
	case scheduleTemplates, scheduleTask:
		s.templates = result.templates
	case scheduleReview:
		s.preview = result.preview
	}
	return m, nil
}

func (s *scheduleModel) gotoPage(page schedulePage) {
	s.page, s.sel, s.top = page, 0, 0
	s.errorText = ""
	s.mouseTarget = ""
}

func (m rootModel) scheduleList(archived bool) (tea.Model, tea.Cmd) {
	catalog, timer := m.schedules.catalog, m.scheduleConfig.TimerStatus
	m.schedules.archived = archived
	return m.scheduleRequest(func() scheduleResultMsg {
		jobs, err := catalog.Jobs()
		return scheduleResultMsg{page: scheduleList, jobs: jobs, err: err, timerStatus: scheduleTimerStatus(timer)}
	})
}

func scheduleTimerStatus(status func() (string, error)) string {
	if status == nil {
		return ""
	}
	text, err := status()
	if err != nil {
		return "status unavailable"
	}
	return text
}

func (m rootModel) enableScheduleTimer() (tea.Model, tea.Cmd) {
	catalog, install, timer := m.schedules.catalog, m.scheduleConfig.InstallTimer, m.scheduleConfig.TimerStatus
	return m.scheduleRequest(func() scheduleResultMsg {
		if err := install(); err != nil {
			return scheduleResultMsg{err: err}
		}
		jobs, err := catalog.Jobs()
		return scheduleResultMsg{page: scheduleList, jobs: jobs, err: err, timerStatus: scheduleTimerStatus(timer)}
	})
}

func (m rootModel) scheduleHistory(runID string) (tea.Model, tea.Cmd) {
	catalog, jobID := m.schedules.catalog, m.schedules.job.ID
	to := scheduleHistory
	if runID != "" {
		to = scheduleRun
	}
	return m.scheduleRequest(func() scheduleResultMsg {
		runs, err := catalog.History(jobID)
		return scheduleResultMsg{page: to, runs: runs, runID: runID, err: err}
	})
}

func (m rootModel) scheduleState(next schedule.JobState) (tea.Model, tea.Cmd) {
	catalog, jobID := m.schedules.catalog, m.schedules.job.ID
	return m.scheduleRequest(func() scheduleResultMsg {
		job, err := catalog.SetJobState(jobID, next, time.Now())
		return scheduleResultMsg{page: scheduleJob, job: job, err: err}
	})
}

func (m rootModel) runScheduledJob() (tea.Model, tea.Cmd) {
	runner, ok := m.client.(interface {
		ScheduleRun(string, string) (string, error)
	})
	if !ok || m.connectionLost {
		m.schedules.errorText = "Run now requires a connected schedule-capable daemon."
		return m, nil
	}
	catalog, jobID := m.schedules.catalog, m.schedules.job.ID
	operationID := m.schedules.pendingRuns[jobID]
	if operationID == "" {
		operationID = schedule.NewID(time.Now())
		if m.schedules.pendingRuns == nil {
			m.schedules.pendingRuns = make(map[string]string)
		}
		m.schedules.pendingRuns[jobID] = operationID
	}
	m.schedules.sel, m.schedules.top = 0, 0
	return m.scheduleRequest(func() scheduleResultMsg {
		id, err := runner.ScheduleRun(jobID, operationID)
		if err != nil {
			return scheduleResultMsg{err: err}
		}
		runs, err := catalog.History(jobID)
		return scheduleResultMsg{page: scheduleRun, runs: runs, runID: id, runJobID: jobID, err: err}
	})
}

func scheduleSession(run schedule.Occurrence) string {
	for i := len(run.Attempts) - 1; i >= 0; i-- {
		if id := run.Attempts[i].SessionID; id != "" {
			return id
		}
	}
	return ""
}

func (m rootModel) openScheduledSession() (tea.Model, tea.Cmd) {
	id := scheduleSession(m.schedules.run)
	s, ok := m.general.sessionByID(id)
	if m.connectionLost || !ok {
		m.schedules.errorText = "Recorded session is unavailable for attach."
		return m, nil
	}
	if s.Status.Process != status.ProcessRunning {
		if m.schedules.run.State != schedule.OccurrenceReady {
			m.schedules.errorText = "Recorded session has ended."
			return m, nil
		}
		resume := resumeCmd(m.client, s, m.width, m.height, false)
		generation := m.reconnectGeneration
		m.schedules.busy = true
		m.schedules.errorText = ""
		return m, func() tea.Msg {
			result := resume().(resumeResultMsg)
			return scheduleOpenResultMsg{result: result.result, connection: generation}
		}
	}
	m.general.restoreSel(id)
	m.schedules.returnFromAttach = true
	m.general.commandID = id
	next, cmd := m.openSelected()
	updated := next.(rootModel)
	updated.general.commandID = ""
	if updated.screen != screenAttach {
		updated.schedules.returnFromAttach = false
	}
	return updated, cmd
}

func (m rootModel) applyScheduledChat(msg scheduleOpenResultMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenSchedules || m.schedules.page != scheduleRun || !m.schedules.busy {
		return m, nil
	}
	m.schedules.busy = false
	if msg.connection != m.reconnectGeneration || m.connectionLost {
		m.schedules.errorText = "Connection changed while opening the conversation. Refresh the run and try again."
		return m, nil
	}
	if msg.result.err != nil {
		m.schedules.errorText = msg.result.err.Error()
		return m, nil
	}
	if msg.result.id == "" {
		m.schedules.errorText = "The daemon did not return the resumed session."
		return m, nil
	}
	m.schedules.returnFromAttach = true
	return m.update(msg.result)
}

type scheduleRow struct {
	id, text string
}

type scheduleControl struct {
	key, label string
}

func scheduleControls(items ...string) []scheduleControl {
	controls := make([]scheduleControl, 0, len(items)/2)
	for i := 0; i < len(items); i += 2 {
		controls = append(controls, scheduleControl{items[i], items[i+1]})
	}
	return controls
}

func (s scheduleModel) controls() []scheduleControl {
	if s.busy {
		return nil
	}
	if s.catalog == nil {
		return scheduleControls("esc", "Back")
	}
	switch s.page {
	case scheduleList:
		toggle := "Archived"
		if s.archived {
			toggle = "Active"
		}
		controls := scheduleControls("n", "New")
		if s.canInstallTimer && s.timerStatus != "installed, active" {
			controls = append(controls, scheduleControl{"i", "Enable timer"})
		}
		return append(controls, scheduleControls("v", toggle, "t", "Templates", "r", "Refresh", "enter", "Open", "esc", "Back")...)
	case scheduleJob:
		runLabel := "Run now"
		if s.pendingRuns[s.job.ID] != "" {
			runLabel = "Retry Run now"
		}
		if s.job.State == schedule.JobArchived {
			if s.pendingRuns[s.job.ID] != "" {
				return scheduleControls("r", runLabel, "h", "History", "esc", "Back")
			}
			return scheduleControls("h", "History", "esc", "Back")
		}
		toggle := "Pause"
		if s.job.State == schedule.JobPaused {
			toggle = "Resume"
		}
		return scheduleControls("e", "Edit", "p", toggle, "r", runLabel, "h", "History", "x", "Archive", "esc", "Back")
	case scheduleTask:
		return scheduleControls("enter", "Next", "ctrl+t", "Use template", "ctrl+s", "Save prompt as template", "esc", "Back")
	case scheduleCalendar:
		return scheduleControls("enter", "Next", "left", "Previous choice", "right", "Next choice", "esc", "Back")
	case scheduleRoute:
		return scheduleControls("enter", "Review", "left", "Previous choice", "right", "Next choice", "space", "Add/remove", "-", "Move up", "+", "Move down", "esc", "Back")
	case scheduleReview:
		return scheduleControls("enter", "Save", "esc", "Back")
	case scheduleHistory:
		return scheduleControls("enter", "Open", "r", "Refresh", "esc", "Back")
	case scheduleRun:
		if scheduleSession(s.run) != "" {
			return scheduleControls("o", "Open worker", "r", "Refresh", "g", "Board", "esc", "Back")
		}
		return scheduleControls("r", "Refresh", "g", "Board", "esc", "Back")
	case scheduleArchive:
		return scheduleControls("y", "Archive job", "esc", "Cancel")
	case scheduleTemplates:
		if s.templateReturn == scheduleTask {
			return scheduleControls("enter", "Use", "esc", "Back")
		}
		return scheduleControls("n", "New", "enter", "Edit", "d", "Delete", "esc", "Back")
	case scheduleTemplateEdit:
		return scheduleControls("enter", "Save", "esc", "Back")
	case scheduleTimerEnable:
		return scheduleControls("y", "Enable timer", "esc", "Cancel")
	case scheduleTemplateDelete:
		return scheduleControls("y", "Delete template", "esc", "Cancel")
	}
	return nil
}

func (s scheduleModel) content(width int) (string, []scheduleRow) {
	switch s.page {
	case scheduleList:
		return s.listContent(width)
	case scheduleJob:
		title := "Automations / " + s.job.Spec.Name
		rows := []string{string(s.job.State), scheduleCadence(s.job.Spec.Calendar), "Directory · " + s.job.Spec.Directory, "Workspace · " + s.job.Spec.Workspace, "Authority · " + s.job.Spec.Authority, "Agent order · " + strings.Join(s.job.Spec.Agents, " → "), "", s.job.Spec.Prompt}
		if s.timerStatus != "" {
			rows = append([]string{"Timer · " + s.timerStatus}, rows...)
		}
		if operationID := s.pendingRuns[s.job.ID]; operationID != "" {
			rows = append([]string{
				"Run now operation ID · " + operationID,
				"CLI retry · swarm schedule run " + s.job.ID + " --operation-id " + operationID,
			}, rows...)
		}
		return title, scheduleTextRows(rows, width)
	case scheduleHistory:
		rows := make([]scheduleRow, 0, len(s.runs))
		for _, run := range s.runs {
			label := scheduleOutcome(run.State) + " · " + scheduleWhen(run)
			if len(run.Attempts) > 0 {
				label += " · " + run.Attempts[len(run.Attempts)-1].Agent
			}
			rows = append(rows, scheduleRow{run.ID, label})
		}
		if len(rows) == 0 {
			rows = append(rows, scheduleRow{text: "This job has no runs yet."})
		}
		return s.job.Spec.Name + " / History", rows
	case scheduleRun:
		rows := []string{scheduleOutcome(s.run.State) + " · " + scheduleWhen(s.run), "Run · " + s.run.ID, "Workspace · " + s.run.Workspace, "Agent order · " + strings.Join(s.run.Spec.Agents, " → "), "", s.run.Detail}
		for index, attempt := range s.run.Attempts {
			rows = append(rows, fmt.Sprintf("Attempt %d · %s · %s", index+1, attempt.Agent, attempt.State))
			if attempt.SessionID != "" {
				rows = append(rows, "  Session · "+attempt.SessionID)
			}
			if attempt.Detail != "" {
				rows = append(rows, "  "+attempt.Detail)
			}
		}
		if s.run.Completion != nil {
			rows = append(rows, "", "Completion", s.run.Completion.Summary)
			rows = append(rows, s.run.Completion.Links...)
		}
		return "History / Run", scheduleTextRows(rows, width)
	case scheduleReview:
		return "Automation / Review", scheduleTextRows(strings.Split(s.preview, "\n"), width)
	case scheduleArchive:
		return "Archive " + s.job.Spec.Name + "?", []scheduleRow{{text: "Future triggers stop. Run history stays available in Archived."}}
	case scheduleTemplates:
		rows := make([]scheduleRow, 0, len(s.templates))
		for _, template := range s.templates {
			rows = append(rows, scheduleRow{template.ID, template.Name})
		}
		if len(rows) == 0 {
			rows = append(rows, scheduleRow{text: "No personal templates yet."})
		}
		return "Automations / Personal templates", rows
	case scheduleTemplateEdit:
		return "Personal templates / Edit", []scheduleRow{
			{"template-name", scheduleTextField("Template name", s.templateName, s.sel == 0, width)},
			{"template-prompt", scheduleTextField("Instructions", s.templatePrompt, s.sel == 1, width)},
		}
	case scheduleTimerEnable:
		return "Enable automation timer?", scheduleTextRows([]string{"Install a per-user timer on this host.", "It checks for due jobs and launches eligible runs."}, width)
	case scheduleTemplateDelete:
		return "Delete " + s.template.Name + "?", []scheduleRow{{text: "Saved jobs keep their own copy of the instructions."}}
	default:
		return s.draft.content(s.page, s.sel, width)
	}
}

func (s scheduleModel) listContent(width int) (string, []scheduleRow) {
	title := "Automations"
	if s.archived {
		title += " / Archived"
	}
	if s.timerStatus != "" {
		if width < 42 {
			timer := "unknown"
			if _, state, found := strings.Cut(s.timerStatus, ", "); found {
				timer = state
			}
			if s.archived && width < 32 {
				title = "Archived"
			}
			title += " · " + timer
		} else {
			title += " · Timer " + s.timerStatus
		}
	}
	rows := make([]scheduleRow, 0, len(s.jobs))
	for _, job := range s.jobs {
		if (job.State == schedule.JobArchived) == s.archived {
			rows = append(rows, scheduleRow{job.ID, string(job.State) + " · " + job.Spec.Name + " · " + scheduleCadence(job.Spec.Calendar)})
		}
	}
	if len(rows) == 0 {
		rows = append(rows, scheduleRow{text: "No jobs in this view."})
	}
	return title, rows
}

func scheduleTextRows(text []string, width int) []scheduleRow {
	var rows []scheduleRow
	for _, line := range text {
		for _, part := range strings.Split(scheduleWrap(line, max(1, width-2)), "\n") {
			rows = append(rows, scheduleRow{text: part})
		}
	}
	return rows
}

func scheduleWhen(run schedule.Occurrence) string {
	kind := "scheduled"
	if run.Manual {
		kind = "manual"
	}
	due := run.DueAt
	if due.IsZero() {
		due = run.ClaimedAt
	}
	if loc, err := time.LoadLocation(run.Spec.Calendar.TimeZone); err == nil {
		due = due.In(loc)
	}
	return due.Format("2006-01-02 15:04 MST") + " · " + kind
}

func scheduleOutcome(state schedule.OccurrenceState) string {
	switch string(state) {
	case string(schedule.OccurrenceNeedsInput):
		return "Needs input"
	case string(schedule.OccurrenceReady):
		return "Ready for review"
	case string(schedule.OccurrenceAttention):
		return "Needs attention"
	case string(schedule.OccurrenceExhausted):
		return "Agent list exhausted"
	}
	value := string(state)
	if value == "" {
		return "Unknown"
	}
	return strings.ToUpper(value[:1]) + strings.ReplaceAll(value[1:], "-", " ")
}

func scheduleWrap(value string, width int) string {
	return ansi.Hardwrap(scheduleSafe(value), width, true)
}

func scheduleSafe(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' && (r < 0x7f || r > 0x9f) {
			return r
		}
		return -1
	}, ansi.Strip(value))
}
