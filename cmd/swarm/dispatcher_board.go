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
	"github.com/Nathandela/swarm/internal/skeleton"
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
func dialLinkedDispatcher(ctx context.Context, local, localEndpoint string, capabilities []string) (*protocol.Client, error) {
	slot, err := installedDiscordDirectory()
	if err != nil {
		return nil, err
	}
	dir, err := discord.SelectedState(slot)
	if err != nil {
		return nil, err
	}
	cfg, err := discord.LoadConfig(dir)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled || !cfg.TeammateMode() {
		return nil, nil
	}
	socket, endpoint := cfg.DaemonSocket, ""
	if cfg.Supervisor != nil {
		endpoint = skeleton.EndpointIDForStateDir(cfg.Supervisor.StateDir)
	}
	if p := cfg.PreviousSupervisor; p != nil {
		if localEndpoint != endpoint {
			return nil, nil
		}
		socket, endpoint = p.Socket, p.Endpoint
	}
	if socket == local {
		return nil, nil
	}
	localInfo, localErr := os.Stat(local)
	linked, err := os.Stat(socket)
	if err != nil {
		return nil, err
	}
	if localErr == nil && os.SameFile(localInfo, linked) {
		return nil, nil
	}
	client, err := protocol.DialContext(ctx, socket, capabilities)
	if err != nil {
		return nil, err
	}
	if endpoint != "" && client.EndpointID() != endpoint {
		_ = client.Close()
		return nil, errors.New("dispatcher supervisor identity changed")
	}
	return client, nil
}
func linkedDispatcherOption(cc daemon.ClientConfig, localEndpoint string, hand tui.TerminalHandoff) tui.Option {
	list := func() ([]protocol.SessionView, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		client, err := dialLinkedDispatcher(ctx, cc.SocketPath, localEndpoint, nil)
		if err != nil || client == nil {
			return nil, err
		}
		defer func() { _ = client.Close() }()
		if client.EndpointID() == localEndpoint {
			return nil, nil
		}
		return client.List()
	}
	dial := func(id string) (attach.Session, func(), error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		client, err := dialLinkedDispatcher(ctx, cc.SocketPath, localEndpoint, []string{"attach"})
		if err != nil {
			return nil, nil, err
		}
		if client == nil {
			return nil, nil, errors.New("dispatcher supervisor unavailable")
		}
		terminal, err := client.Attach(id)
		if err != nil {
			_ = client.Close()
			return nil, nil, err
		}
		return terminal, func() { _ = client.Close() }, nil
	}
	remove := func(id string, kill bool) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		client, err := dialLinkedDispatcher(ctx, cc.SocketPath, localEndpoint, nil)
		if err != nil {
			return err
		}
		if client == nil {
			return errors.New("dispatcher supervisor unavailable")
		}
		defer func() { _ = client.Close() }()
		if kill {
			return client.Kill(id)
		}
		return client.Delete(id)
	}
	return tui.WithLinkedDispatchers(list, tui.NewAttachRunner(dial, hand), remove)
}
