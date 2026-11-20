package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Nathandela/swarm/internal/attach"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/tui"
)

// There is one installed Discord service per OS user. Its supervisor can differ
// from the default terminal's. Its per-discussion ledgers stay with that supervisor.
func installedDiscordDirectory() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "swarm", "installed-discord"), nil
}
func recordInstalledDiscord(dir string) error {
	slot, err := installedDiscordDirectory()
	if err != nil {
		return err
	}
	if previous, e := discord.SelectedState(slot); e == nil && previous == dir {
		return nil
	}
	return discord.SelectState(slot, dir)
}
func linkedDispatcherSocket(local string) (string, error) {
	slot, e := installedDiscordDirectory()
	if e != nil {
		return "", e
	}
	dir, e := discord.SelectedState(slot)
	if e != nil {
		return "", e
	}
	cfg, e := discord.LoadConfig(dir)
	if e != nil {
		return "", e
	}
	if !cfg.Enabled || !cfg.TeammateMode() || cfg.DaemonSocket == local {
		return "", nil
	}
	a, err := os.Stat(local)
	b, e := os.Stat(cfg.DaemonSocket)
	if e != nil {
		return "", e
	}
	if err == nil && os.SameFile(a, b) {
		return "", nil
	}
	return cfg.DaemonSocket, nil
}
func linkedDispatcherOption(cc daemon.ClientConfig, localEndpoint string, hand tui.TerminalHandoff) tui.Option {
	list := func() ([]protocol.SessionView, error) {
		socket, err := linkedDispatcherSocket(cc.SocketPath)
		if socket == "" || err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		client, err := protocol.DialContext(ctx, socket, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = client.Close() }()
		if client.EndpointID() == localEndpoint {
			return nil, nil
		}
		return client.List()
	}
	dial := func(id string) (attach.Session, func(), error) {
		socket, err := linkedDispatcherSocket(cc.SocketPath)
		if err != nil {
			return nil, nil, err
		}
		if socket == "" {
			return nil, nil, errors.New("dispatcher supervisor unavailable")
		}
		return attachDialer(daemon.ClientConfig{SocketPath: socket})(id)
	}
	return tui.WithLinkedDispatchers(list, tui.NewAttachRunner(dial, hand))
}
