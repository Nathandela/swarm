package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
)

// A timer's environment is often missing the owner's agent executables and
// login context. Only an interactive launch may establish the initial snapshot.
func dialScheduleClient(fromTimer bool) (*protocol.Client, error) {
	cfg, err := clientConfig()
	if err != nil {
		return nil, err
	}
	caps := []string{protocol.CapSchedule, protocol.CapScheduleRunID}
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
