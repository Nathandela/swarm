package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"syscall"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/schedule"
)

// A timer's environment is often missing the owner's agent executables and
// login context. Only an interactive launch may establish the initial snapshot.
func dialScheduleClient(fromTimer bool) (*protocol.Client, error) {
	cfg, err := clientConfig()
	if err != nil {
		return nil, err
	}
	caps := []string{protocol.CapSchedule, protocol.CapScheduleRunID, protocol.CapScheduleWorkspace, protocol.CapScheduleRoute, protocol.CapScheduleHostTime}
	connect := func() (*protocol.Client, error) {
		client, err := protocol.Dial(cfg.SocketPath, caps)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(client.Capabilities(), protocol.CapSchedule) {
			_ = client.Close()
			return nil, errScheduleUnsupported
		}
		return client, nil
	}
	if c, err := connect(); err == nil || errors.Is(err, errScheduleUnsupported) {
		return c, err
	}
	if fromTimer {
		cfg.Env, err = daemon.LoadSavedEnv(cfg.StateDir)
		if err != nil || len(cfg.Env) == 0 {
			return nil, errors.New("scheduled launch has no saved agent environment; open Swarm interactively on this host first")
		}
	}
	conn, err := daemon.EnsureDaemon(cfg)
	if err != nil {
		return nil, fmt.Errorf("start schedule supervisor: %w", err)
	}
	_ = conn.Close()
	return connect()
}

var errScheduleUnsupported = errors.New("running daemon does not support scheduled jobs; upgrade and restart the Swarm daemon")

// The catalog CLI can work offline. Before publishing a host calendar,
// ensure a live older daemon cannot encounter a zone it cannot evaluate.
func checkScheduleCalendar(calendar schedule.Calendar) error {
	if calendar.TimeZone != schedule.HostTimeZone {
		return nil
	}
	cfg, err := clientConfig()
	if err != nil {
		return err
	}
	client, err := protocol.Dial(cfg.SocketPath, []string{protocol.CapScheduleHostTime})
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	return checkHostTimeCapability(calendar, client.Capabilities())
}

func checkHostTimeCapability(calendar schedule.Calendar, caps []string) error {
	if calendar.TimeZone == schedule.HostTimeZone && !slices.Contains(caps, protocol.CapScheduleHostTime) {
		return errors.New("restart the updated Swarm daemon before enabling a schedule that follows host time")
	}
	return nil
}
