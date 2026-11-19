package main

import (
	"os"

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
		Validate:      validateScheduleSpec,
		ValidateRoute: scheduledexec.ValidateRoute,
		TimerStatus:   scheduletimer.Status,
		InstallTimer:  installScheduleTimer,
		Prompt:        scheduledexec.Prompt,
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
