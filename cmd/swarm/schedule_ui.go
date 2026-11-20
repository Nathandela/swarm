package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/Nathandela/swarm/internal/scheduledexec"
	"github.com/Nathandela/swarm/internal/scheduletimer"
	"github.com/Nathandela/swarm/internal/tui"
)

func scheduleUIOption() tui.Option {
	return tui.WithSchedules(tui.ScheduleConfig{
		Open: func() (tui.ScheduleCatalog, error) {
			cfg, err := clientConfig()
			if err != nil {
				return nil, err
			}
			return schedule.Open(cfg.StateDir)
		},
		Preflight:     preflightSchedule,
		Save:          saveScheduleEditor,
		ValidateRoute: scheduledexec.ValidateRoute,
		TimerStatus:   scheduletimer.Status,
		InstallTimer:  installScheduleTimer,
	})
}

func validateScheduleSpec(spec schedule.JobSpec) error {
	if err := schedule.ValidateJob(spec); err != nil {
		return err
	}
	for _, agent := range spec.Agents {
		if err := scheduledexec.ValidateRoute(agent, spec.Authority); err != nil {
			return err
		}
	}
	return nil
}

func installScheduleTimer() error {
	client, err := dialScheduleClient(false)
	if err != nil {
		return err
	}
	_ = client.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cfg, err := clientConfig()
	if err != nil {
		return err
	}
	return scheduletimer.Install(exe, scheduletimer.Target{State: cfg.StateDir, Socket: cfg.SocketPath, Lock: cfg.LockPath, Log: cfg.LogPath})
}

func preflightSchedule(spec schedule.JobSpec) (tui.SchedulePreflight, error) {
	var report tui.SchedulePreflight
	if githubNetworkHint(spec) {
		report.Warnings = append(report.Warnings, "GitHub access needs a network-enabled harness. Check the permissions selected for Codex.")
	}
	for _, agent := range spec.Agents {
		if err := scheduledexec.ValidateRoute(agent, spec.Authority); err != nil {
			report.Blocking = append(report.Blocking, err.Error())
		}
	}
	dir, err := schedule.ResolveDirectory(spec.Directory)
	if err != nil {
		report.Blocking = append(report.Blocking, err.Error())
		return report, nil
	}
	spec.Directory = dir
	cfg, err := clientConfig()
	if err != nil {
		return report, err
	}
	store, err := schedule.Open(cfg.StateDir)
	if err != nil {
		return report, err
	}
	active, err := store.ActiveOccurrences()
	if err != nil {
		return report, err
	}
	client, err := dialScheduleClient(false)
	if err != nil {
		return report, err
	}
	defer func() { _ = client.Close() }()
	var unavailable []string
	eligible := 0
	for _, agent := range spec.Agents {
		if scheduledexec.ValidateRoute(agent, spec.Authority) != nil {
			continue
		}
		eligible++
		reason, err := client.ScheduleRouteAvailability(agent)
		if err != nil {
			return report, err
		}
		if reason != "" {
			unavailable = append(unavailable, agent+" · "+reason)
		}
	}
	if eligible > 0 && len(unavailable) == eligible {
		report.Blocking = append(report.Blocking, unavailable...)
	} else {
		report.Warnings = append(report.Warnings, unavailable...)
	}
	if err := checkHostTimeCapability(spec.Calendar, client.Capabilities()); err != nil {
		return report, err
	}
	warning, err := scheduledexec.PreviewWorkspace(cfg.StateDir, spec, active, client.ScheduleWorkspaceConflict)
	if err != nil {
		report.Blocking = append(report.Blocking, err.Error())
	}
	if warning != "" {
		report.Warnings = append(report.Warnings, warning)
	}
	return report, nil
}

func githubNetworkHint(spec schedule.JobSpec) bool {
	if spec.Authority == schedule.AuthorityAgentDefault || !slices.Contains(spec.Agents, schedule.RouteCodex) {
		return false
	}
	prompt := strings.ToLower(spec.Prompt)
	return strings.Contains(prompt, "github") || strings.Contains(prompt, "dependabot") || slices.Contains(strings.FieldsFunc(prompt, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' }), "gh")
}

func saveScheduleEditor(spec schedule.JobSpec, id, revision string, enable, allowTimerInstall bool) (schedule.Job, error) {
	cfg, err := clientConfig()
	if err != nil {
		return schedule.Job{}, err
	}
	store, err := schedule.Open(cfg.StateDir)
	if err != nil {
		return schedule.Job{}, err
	}
	return persistScheduleEditor(store, spec, id, revision, enable, allowTimerInstall, preflightSchedule, scheduletimer.Status, installScheduleTimer)
}

func persistScheduleEditor(store *schedule.Store, spec schedule.JobSpec, id, revision string, enable, allowTimerInstall bool,
	preflight func(schedule.JobSpec) (tui.SchedulePreflight, error), timerStatus func() (string, error), install func() error) (schedule.Job, error) {
	if err := schedule.ValidateJob(spec); err != nil {
		return schedule.Job{}, err
	}
	if id != "" {
		job, err := store.Job(id)
		if err != nil {
			return schedule.Job{}, err
		}
		if job.State == schedule.JobArchived {
			return schedule.Job{}, schedule.ErrArchived
		}
		if job.RevisionID != revision {
			return schedule.Job{}, schedule.ErrConflict
		}
	}
	if !enable {
		return store.SaveJob(id, revision, spec, schedule.JobPaused, time.Now())
	}
	report, err := preflight(spec)
	if err != nil {
		return schedule.Job{}, err
	}
	if len(report.Blocking) > 0 {
		return schedule.Job{}, errors.New(strings.Join(report.Blocking, " · "))
	}
	state, err := timerStatus()
	if err != nil {
		return schedule.Job{}, fmt.Errorf("check host timer: %w", err)
	}
	installed := state != "installed, active"
	if installed {
		if !allowTimerInstall {
			return schedule.Job{}, errors.New("host timer is no longer active; select Save and enable again to confirm activation")
		}
		if err := install(); err != nil {
			return schedule.Job{}, fmt.Errorf("enable host timer: %w", err)
		}
		state, err = timerStatus()
		if err != nil {
			return schedule.Job{}, fmt.Errorf("verify host timer: %w", err)
		}
		if state != "installed, active" {
			return schedule.Job{}, fmt.Errorf("host timer reports %s; retry or save paused", state)
		}
	}
	job, err := store.SaveJob(id, revision, spec, schedule.JobActive, time.Now())
	if err != nil && installed {
		return schedule.Job{}, fmt.Errorf("host timer enabled; job save failed: %w", err)
	}
	return job, err
}
