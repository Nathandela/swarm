package tui

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/charmbracelet/x/ansi"
)

func (m rootModel) updateSchedule(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := &m.schedules
	s.mouseTarget = ""
	if s.busy {
		return m, nil
	}
	if k.Code == tea.KeyEsc {
		return m.backSchedule()
	}
	if s.catalog == nil {
		return m, nil
	}
	view := m.scheduleLayout()
	switch k.String() {
	case "up", "shift+tab":
		s.move(-1, view)
	case "down", "tab":
		s.move(1, view)
	case "pgup":
		s.move(-view.budget, view)
	case "pgdown":
		s.move(view.budget, view)
	default:
		if s.page >= scheduleTask && s.page <= scheduleRoute {
			next, cmd := m.updateScheduleDraft(k)
			updated := next.(rootModel)
			updated.schedules.reveal(updated.scheduleLayout())
			return updated, cmd
		}
		if s.page == scheduleTemplateEdit && k.String() != "enter" {
			editor := &s.templateName
			if s.sel == 1 {
				editor = &s.templatePrompt
			}
			if k.String() == "alt+enter" && s.sel == 1 {
				editor.insert("\n")
			} else {
				editor.update(k)
			}
			return m, nil
		}
		if k.Mod == 0 && (k.Text == "j" || k.Text == "k") {
			delta := 1
			if k.Text == "k" {
				delta = -1
			}
			s.move(delta, view)
			return m, nil
		}
		if k.String() == "home" || k.String() == "end" {
			s.sel = 0
			if k.String() == "end" {
				s.sel = max(0, len(view.rows)-1)
			}
			s.reveal(view)
			return m, nil
		}
		if slices.ContainsFunc(s.controls(), func(control scheduleControl) bool { return control.key == k.String() }) {
			return m.activateSchedule(k.String(), view)
		}
	}
	return m, nil
}

func (m rootModel) backSchedule() (tea.Model, tea.Cmd) {
	s := &m.schedules
	if s.catalog == nil {
		return m.leaveSchedules()
	}
	switch s.page {
	case scheduleTask:
		if s.draftReturn == scheduleJob {
			s.gotoPage(scheduleJob)
			return m, nil
		}
		if s.directoryShortcut {
			return m.leaveSchedules()
		}
		return m.scheduleList(s.archived)
	case scheduleCalendar, scheduleRoute, scheduleReview:
		s.gotoPage(s.page - 1)
	case scheduleJob:
		return m.scheduleList(s.job.State == schedule.JobArchived)
	case scheduleHistory, scheduleArchive:
		s.gotoPage(scheduleJob)
	case scheduleRun:
		s.gotoPage(scheduleHistory)
	case scheduleTemplates:
		s.gotoPage(s.templateReturn)
	case scheduleTemplateEdit:
		if s.saveDraftTemplate {
			s.gotoPage(scheduleTask)
		} else {
			s.gotoPage(scheduleTemplates)
		}
	case scheduleTemplateDelete:
		s.gotoPage(scheduleTemplates)
	case scheduleTimerEnable:
		s.gotoPage(scheduleList)
	default:
		return m.leaveSchedules()
	}
	return m, nil
}

func (m rootModel) leaveSchedules() (tea.Model, tea.Cmd) {
	m.openMore()
	return m, m.enterGeneral()
}

func (m rootModel) activateSchedule(key string, layout scheduleFrame) (tea.Model, tea.Cmd) {
	s := &m.schedules
	rowID := ""
	if s.sel < len(layout.rows) {
		rowID = layout.rows[s.sel].id
	}
	switch s.page {
	case scheduleList:
		switch key {
		case "n":
			s.draft = blankSchedule("")
			s.draftReturn = scheduleList
			s.directoryShortcut = false
			s.gotoPage(scheduleTask)
		case "i":
			s.gotoPage(scheduleTimerEnable)
		case "v":
			return m.scheduleList(!s.archived)
		case "r":
			return m.scheduleList(s.archived)
		case "t":
			return m.showScheduleTemplates(scheduleList)
		case "enter":
			if rowID != "" {
				catalog := s.catalog
				return m.scheduleRequest(func() scheduleResultMsg {
					job, err := catalog.Job(rowID)
					return scheduleResultMsg{page: scheduleJob, job: job, err: err}
				})
			}
		}
	case scheduleJob:
		switch key {
		case "e":
			s.draft = newScheduleDraft(s.job.Spec)
			s.draft.jobID, s.draft.revision = s.job.ID, s.job.RevisionID
			s.draftReturn = scheduleJob
			s.gotoPage(scheduleTask)
		case "p":
			next := schedule.JobPaused
			if s.job.State == schedule.JobPaused {
				next = schedule.JobActive
			}
			return m.scheduleState(next)
		case "r":
			return m.runScheduledJob()
		case "h":
			return m.scheduleHistory("")
		case "x":
			s.gotoPage(scheduleArchive)
		}
	case scheduleReview:
		return m.saveSchedule()
	case scheduleArchive:
		return m.scheduleState(schedule.JobArchived)
	case scheduleHistory:
		switch key {
		case "enter":
			for _, run := range s.runs {
				if run.ID == rowID {
					return m.showScheduleRun(run)
				}
			}
		case "r":
			return m.scheduleHistory("")
		}
	case scheduleRun:
		switch key {
		case "o":
			return m.openScheduledSession()
		case "r":
			return m.scheduleHistory(s.run.ID)
		case "g":
			return m, m.enterGeneral()
		}
	case scheduleTemplates:
		return m.activateScheduleTemplate(key, rowID)
	case scheduleTemplateEdit:
		return m.changeScheduleTemplate(false)
	case scheduleTemplateDelete:
		return m.changeScheduleTemplate(true)
	case scheduleTimerEnable:
		return m.enableScheduleTimer()
	}
	return m, nil
}

func (m rootModel) activateScheduleTemplate(key, id string) (tea.Model, tea.Cmd) {
	s := &m.schedules
	var template schedule.Template
	if key != "n" {
		i := slices.IndexFunc(s.templates, func(candidate schedule.Template) bool { return candidate.ID == id })
		if i < 0 {
			return m, nil
		}
		template = s.templates[i]
	}
	if s.templateReturn == scheduleTask {
		s.draft.prompt.set(template.Prompt)
		if s.draft.name.text == "" {
			s.draft.name.set(template.Name)
		}
		s.gotoPage(scheduleTask)
		return m, nil
	}
	s.template = template
	s.saveDraftTemplate = false
	if key == "d" {
		s.gotoPage(scheduleTemplateDelete)
	} else {
		s.templateName = newLineEditor(template.Name)
		s.templatePrompt = newLineEditor(template.Prompt)
		s.gotoPage(scheduleTemplateEdit)
	}
	return m, nil
}

func (m rootModel) showScheduleTemplates(from schedulePage) (tea.Model, tea.Cmd) {
	m.schedules.templateReturn = from
	catalog := m.schedules.catalog
	return m.scheduleRequest(func() scheduleResultMsg {
		templates, err := catalog.Templates()
		return scheduleResultMsg{page: scheduleTemplates, templates: templates, err: err}
	})
}

func (m rootModel) changeScheduleTemplate(remove bool) (tea.Model, tea.Cmd) {
	s := m.schedules
	if !remove && (strings.TrimSpace(s.templateName.text) == "" || strings.TrimSpace(s.templatePrompt.text) == "") {
		m.schedules.errorText = "Enter a template name and instructions."
		return m, nil
	}
	return m.scheduleRequest(func() scheduleResultMsg {
		var err error
		now := time.Now()
		switch {
		case remove:
			err = s.catalog.DeleteTemplate(s.template.ID)
		case s.template.ID != "":
			_, err = s.catalog.EditTemplate(s.template.ID, s.templateName.text, s.templatePrompt.text, now)
		default:
			_, err = s.catalog.CreateTemplate(s.templateName.text, s.templatePrompt.text, now)
		}
		if err != nil {
			return scheduleResultMsg{err: err}
		}
		if s.saveDraftTemplate {
			return scheduleResultMsg{page: scheduleTask}
		}
		templates, err := s.catalog.Templates()
		return scheduleResultMsg{page: scheduleTemplates, templates: templates, err: err}
	})
}

func (m *rootModel) pasteSchedule(content string) {
	s := &m.schedules
	if s.busy {
		return
	}
	if s.page == scheduleTemplateEdit {
		if s.sel == 0 {
			s.templateName.paste(scheduleSafe(content))
		} else {
			s.templatePrompt.insert(schedulePastePrompt(content))
		}
		return
	}
	fields := s.draft.fields(s.page)
	if s.sel < 0 || s.sel >= len(fields) || fields[s.sel].text == nil {
		return
	}
	field := fields[s.sel]
	if field.id == "prompt" {
		field.text.insert(schedulePastePrompt(content))
	} else {
		field.text.paste(scheduleSafe(content))
	}
}

func schedulePastePrompt(content string) string {
	// Normalize pasted line endings while preserving paragraph breaks.
	return scheduleSafe(strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n"))
}

type scheduleButton struct {
	scheduleControl
	text        string
	x, y, width int
}

type scheduleFrame struct {
	title              string
	rows               []scheduleRow
	buttons            []scheduleButton
	footerRows, budget int
	top                int
}

func (m rootModel) scheduleLayout() scheduleFrame {
	width := m.width
	height := m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	s := m.schedules
	title, rows := s.content(width)
	if s.page == scheduleRoute && m.scheduleConfig.ValidateRoute != nil {
		for i, row := range rows {
			if slices.Contains(scheduleAgents, row.id) && m.scheduleConfig.ValidateRoute(row.id, s.draft.authority) != nil {
				rows[i].text += " · unavailable with this authority"
			}
		}
	}
	if s.busy {
		title += " · Loading"
	}
	if s.errorText != "" {
		title = scheduleSafe(s.errorText)
	}
	frame := scheduleFrame{title: title, rows: rows}
	maxFooter := min(3, max(1, height-3))
	controls := s.controls()
	if len(controls) > 0 {
		x, y := 0, 0
		if height < 8 || width < 42 {
			// Shortcuts stay clickable on narrow terminals. The title and body
			// retain room for the selected item and validation feedback.
			keys := make([]scheduleControl, 0, len(controls))
			for _, control := range controls {
				if control.key == "esc" {
					keys = append([]scheduleControl{control}, keys...)
				} else {
					keys = append(keys, control)
				}
			}
			controls = keys
		}
		for _, control := range controls {
			key := control.key
			switch key {
			case "enter":
				key = "⏎"
			case "left":
				key = "←"
			case "right":
				key = "→"
			}
			label := "[" + control.label + " (" + key + ")]"
			if width < 42 || height < 8 {
				label = "[" + key + "]"
			}
			w := ansi.StringWidth(label)
			if x+w > width {
				x, y = 0, y+1
			}
			if y >= maxFooter {
				break
			}
			frame.buttons = append(frame.buttons, scheduleButton{control, label, x, y, w})
			x += w + 1
		}
		if len(frame.buttons) > 0 {
			frame.footerRows = frame.buttons[len(frame.buttons)-1].y + 1
		}
	}
	frame.budget = max(1, height-1-frame.footerRows)
	frame.top = min(s.top, max(0, len(rows)-frame.budget))
	frame.top = max(0, min(frame.top, s.sel))
	if s.sel >= frame.top+frame.budget {
		frame.top = s.sel - frame.budget + 1
	}
	return frame
}

func (m rootModel) scheduleView() string {
	frame := m.scheduleLayout()
	s := m.schedules
	width := m.width
	if width <= 0 {
		width = 80
	}
	title := strings.ReplaceAll(scheduleSafe(frame.title), "\n", " ")
	titleStyle := styleAccent
	if s.errorText != "" && !s.busy {
		titleStyle = styleError
	}
	lines := []string{titleStyle.Render(ellipsis(title, width))}
	for i := frame.top; i < min(len(frame.rows), frame.top+frame.budget); i++ {
		prefix := "  "
		if i == s.sel {
			prefix = styleAccent.Render("▌") + " "
		}
		row := strings.NewReplacer("\n", " ", "\t", "    ").Replace(scheduleSafe(frame.rows[i].text))
		lines = append(lines, prefix+ellipsis(row, width-2))
	}
	for len(lines) < frame.budget+1 {
		lines = append(lines, "")
	}
	footers := make([]string, frame.footerRows)
	for _, button := range frame.buttons {
		if footers[button.y] != "" {
			footers[button.y] += " "
		}
		footers[button.y] += button.text
	}
	return boundedBoard(strings.Join(append(lines, footers...), "\n"), m.width, m.height)
}

func (s *scheduleModel) reveal(layout scheduleFrame) {
	s.top = layout.top
}

func (s *scheduleModel) move(delta int, layout scheduleFrame) {
	s.sel = max(0, min(s.sel+delta, len(layout.rows)-1))
	s.top = max(0, min(layout.top, s.sel))
	if s.sel >= s.top+layout.budget {
		s.top = s.sel - layout.budget + 1
	}
}

func (m rootModel) updateScheduleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	s := &m.schedules
	if s.busy {
		return m, nil
	}
	mouse := msg.Mouse()
	frame := m.scheduleLayout()
	row := frame.top + mouse.Y - 1
	inBody := mouse.X >= 0 && mouse.X < m.width && mouse.Y > 0 && mouse.Y <= frame.budget && row < len(frame.rows)
	onButton := ""
	for _, button := range frame.buttons {
		if mouse.Y == frame.budget+1+button.y && mouse.X >= button.x && mouse.X < button.x+button.width {
			onButton = button.key
		}
	}
	switch msg.(type) {
	case tea.MouseWheelMsg:
		s.mouseTarget = ""
		if mouse.Button == tea.MouseWheelUp {
			s.move(-1, frame)
		}
		if mouse.Button == tea.MouseWheelDown {
			s.move(1, frame)
		}
	case tea.MouseClickMsg:
		s.mouseTarget = ""
		if mouse.Button != tea.MouseLeft {
			return m, nil
		}
		if inBody {
			active := s.sel == row
			s.sel = row
			if active && frame.rows[row].id != "" && (s.page == scheduleList || s.page == scheduleHistory || s.page == scheduleTemplates) {
				onButton = "enter"
			}
		}
		s.mouseTarget, s.mouseX, s.mouseY = onButton, mouse.X, mouse.Y
	case tea.MouseMotionMsg:
		if s.mouseX != mouse.X || s.mouseY != mouse.Y {
			s.mouseTarget = ""
		}
	case tea.MouseReleaseMsg:
		key := s.mouseTarget
		s.mouseTarget = ""
		if key != "" && s.mouseX == mouse.X && s.mouseY == mouse.Y && (mouse.Button == tea.MouseNone || mouse.Button == tea.MouseLeft) {
			if key == onButton || inBody && key == "enter" && s.sel == row {
				return m.updateSchedule(scheduleActionKey(key))
			}
		}
	}
	return m, nil
}

func scheduleActionKey(key string) tea.KeyPressMsg {
	switch key {
	case "x":
		return tea.KeyPressMsg{Code: 'x', Text: "x"}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "+", "-":
		return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	case "ctrl+t":
		return tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "space":
		return tea.KeyPressMsg{Code: ' ', Text: " "}
	}
	return actionKey(key)
}
