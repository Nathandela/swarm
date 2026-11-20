package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/schedule"
)

type SchedulePreflight struct {
	Blocking, Warnings []string
}

type schedulePreflightTick struct {
	generation uint64
}

type schedulePreflightResult struct {
	generation                  uint64
	report                      SchedulePreflight
	nextFiring                  string
	hostZone, resolvedDirectory string
}

func (m rootModel) checkScheduleEditor() (tea.Model, tea.Cmd) {
	rowID := m.selectedScheduleRow()
	m.scheduleEditorGeneration++
	generation := m.scheduleEditorGeneration
	m.schedules.preflightPending = true
	m.schedules.preflight = SchedulePreflight{}
	m.schedules.errorText = ""
	m.schedules.nextFiring = "Next firing · Calculating…"
	m.schedules.resolvedDirectory = ""
	m.selectScheduleRow(rowID)
	return m, tea.Tick(180*time.Millisecond, func(time.Time) tea.Msg {
		return schedulePreflightTick{generation: generation}
	})
}

func (m rootModel) startSchedulePreflight(msg schedulePreflightTick) (tea.Model, tea.Cmd) {
	if m.screen != screenSchedules || m.schedules.page != scheduleEditor || msg.generation != m.scheduleEditorGeneration {
		return m, nil
	}
	spec, config := m.schedules.draft.spec(), m.scheduleConfig
	return m, func() tea.Msg {
		result := schedulePreflightResult{generation: msg.generation}
		result.resolvedDirectory, _ = schedule.ResolveDirectory(spec.Directory)
		now := time.Now()
		next, err := spec.Calendar.Next(now)
		switch {
		case err != nil:
			result.nextFiring = "Next firing · " + err.Error()
		case next.IsZero():
			result.nextFiring = "Next firing · No future trigger"
		default:
			if spec.Calendar.TimeZone == schedule.HostTimeZone {
				result.hostZone = next.Location().String()
			}
			result.nextFiring = "Next firing · " + next.Format("Mon, 02 Jan 2006 15:04 MST")
		}
		if config.Preflight != nil {
			report, err := config.Preflight(spec)
			result.report = report
			if err != nil {
				result.report.Blocking = append(result.report.Blocking, err.Error())
			}
		} else if config.ValidateRoute != nil {
			for _, route := range spec.Agents {
				if err := config.ValidateRoute(route, spec.Authority); err != nil {
					result.report.Blocking = append(result.report.Blocking, err.Error())
				}
			}
		}
		return result
	}
}

func (m rootModel) finishSchedulePreflight(result schedulePreflightResult) (tea.Model, tea.Cmd) {
	if m.screen != screenSchedules || m.schedules.page != scheduleEditor || result.generation != m.scheduleEditorGeneration {
		return m, nil
	}
	s := &m.schedules
	rowID := m.selectedScheduleRow()
	s.preflight, s.nextFiring, s.preflightPending = result.report, result.nextFiring, false
	s.hostZone, s.resolvedDirectory = result.hostZone, result.resolvedDirectory
	next := m.scheduleLayout()
	if i := slices.IndexFunc(next.rows, func(row scheduleRow) bool { return rowID != "" && row.id == rowID }); i >= 0 {
		s.sel = i
	} else {
		s.sel = min(s.sel, max(0, len(next.rows)-1))
	}
	s.reveal(m.scheduleLayout())
	return m, nil
}

func (m rootModel) requestEditorEnable() (tea.Model, tea.Cmd) {
	s := &m.schedules
	if len(s.preflight.Blocking) > 0 && !s.preflightPending {
		s.errorText = s.preflight.Blocking[0]
		return m, nil
	}
	if s.timerStatus != "installed, active" && m.scheduleConfig.TimerStatus != nil {
		s.gotoPage(scheduleEditorEnable)
		return m, nil
	}
	return m.saveSchedule(true, false)
}

func (m rootModel) saveSchedule(enable, allowTimerInstall bool) (tea.Model, tea.Cmd) {
	catalog, draft, config := m.schedules.catalog, m.schedules.draft, m.scheduleConfig
	spec := draft.spec()
	if strings.TrimSpace(spec.Name) == "" || strings.TrimSpace(spec.Prompt) == "" || spec.Directory == "" {
		m.schedules.gotoPage(scheduleEditor)
		m.schedules.errorText = "Enter a name, task instructions, and directory."
		return m, nil
	}
	if enable && len(m.schedules.preflight.Blocking) > 0 && !m.schedules.preflightPending {
		m.schedules.gotoPage(scheduleEditor)
		m.schedules.errorText = m.schedules.preflight.Blocking[0]
		return m, nil
	}
	if m.schedules.page != scheduleEditor {
		m.schedules.gotoPage(scheduleEditor)
	}
	return m.scheduleRequest(func() scheduleResultMsg {
		result := scheduleResultMsg{page: scheduleJob}
		if config.Save != nil {
			result.job, result.err = config.Save(spec, draft.jobID, draft.revision, enable, allowTimerInstall)
		} else {
			// An embedded caller may provide the store directly. Production supplies
			// a save operation that verifies readiness and timer health first.
			state := schedule.JobPaused
			if enable {
				state = schedule.JobActive
			}
			store, ok := catalog.(interface {
				SaveJob(string, string, schedule.JobSpec, schedule.JobState, time.Time) (schedule.Job, error)
			})
			if !ok {
				result.err = fmt.Errorf("automation saving unavailable")
				return result
			}
			result.job, result.err = store.SaveJob(draft.jobID, draft.revision, spec, state, time.Now())
		}
		if config.TimerStatus != nil {
			result.timerStatus = scheduleTimerStatus(config.TimerStatus)
		}
		return result
	})
}
