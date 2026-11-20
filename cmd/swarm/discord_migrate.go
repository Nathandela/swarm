package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/skeleton"
	"golang.org/x/sys/unix"
)

// The connector owns enrollment and the supervisor owns its active selector. Holding
// both directory inodes serializes competing administrative changes across terminals.
func lockDiscordConfiguration(dir, supervisor string) (func(), error) {
	var held []*os.File
	release := func() {
		for _, f := range held {
			_ = f.Close()
		}
	}
	for _, path := range []string{supervisor, dir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			release()
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			release()
			return nil, err
		}
		if len(held) > 0 {
			first, e1 := held[0].Stat()
			next, e2 := f.Stat()
			if e1 == nil && e2 == nil && os.SameFile(first, next) {
				_ = f.Close()
				continue
			}
		}
		held = append(held, f)
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			release()
			if errors.Is(err, unix.EWOULDBLOCK) {
				return nil, errors.New("discord: another configuration change is in progress")
			}
			return nil, err
		}
	}
	return release, nil
}

// Keep the connector DB, discussion directories and service registration in place.
// The empty supervisor marker on old records continues to identify the old endpoint.
func migrateDiscordSupervisor(ctx context.Context, home daemon.ClientConfig, dir string, source discord.PreviousSupervisor, preview bool) error {
	cfg, err := discord.LoadConfig(dir)
	if err != nil {
		return err
	}
	if !cfg.Enabled || !cfg.TeammateMode() {
		return errors.New("discord: migration requires an enabled teammate or personal connector with native discussions")
	}
	if source != (discord.PreviousSupervisor{}) {
		for _, p := range []string{source.StateDir, source.Socket, source.Lock, source.Log} {
			if !filepath.IsAbs(p) || filepath.Clean(p) != p {
				return errors.New("discord: supply all four absolute --from supervisor paths from the installed service")
			}
		}
		source.Endpoint = skeleton.EndpointIDForStateDir(source.StateDir)
		if cfg.PreviousSupervisor != nil {
			if *cfg.PreviousSupervisor != source {
				return errors.New("discord: supplied original supervisor differs from the saved migration")
			}
		} else {
			if source.Socket != cfg.DaemonSocket || cfg.Supervisor != nil && source.SupervisorPaths != *cfg.Supervisor {
				return errors.New("discord: supplied original supervisor differs from enrollment")
			}
			// Earlier installations saved only a socket. Prove the explicit state directory owns
			// both this live supervisor and the original connector selection before pinning it.
			if cfg.Supervisor == nil {
				if err := verifyMigrationEndpoint(ctx, source.Socket, source.Endpoint); err != nil {
					return err
				}
				cfg.Supervisor = &source.SupervisorPaths
			}
		}
	}
	if cfg.Supervisor == nil {
		return errors.New("discord: earlier enrollment requires --from-state-dir, --from-socket, --from-lock and --from-log from the installed service")
	}
	for _, v := range []*string{&home.StateDir, &home.SocketPath, &home.LockPath, &home.LogPath} {
		if *v, err = filepath.Abs(*v); err != nil {
			return err
		}
	}
	if preview {
		if err := verifyMigrationEndpoint(ctx, home.SocketPath, skeleton.EndpointIDForStateDir(home.StateDir)); err != nil {
			return err
		}
		legacySocket, legacyEndpoint := cfg.DaemonSocket, skeleton.EndpointIDForStateDir(cfg.Supervisor.StateDir)
		if p := cfg.PreviousSupervisor; p != nil {
			legacySocket, legacyEndpoint = p.Socket, p.Endpoint
		}
		if err := verifyMigrationEndpoint(ctx, legacySocket, legacyEndpoint); err != nil {
			return err
		}
	}
	current, err := discord.ActiveState(home.StateDir)
	if err != nil {
		return err
	}
	if current != dir {
		_, selectedErr := os.Lstat(filepath.Join(home.StateDir, "discord-active.json"))
		_, configErr := os.Stat(discord.ConfigPath(current))
		if !os.IsNotExist(selectedErr) || !os.IsNotExist(configErr) {
			return errors.New("discord: home already selects a different connector")
		}
	}
	if cfg.DaemonSocket == home.SocketPath && cfg.Supervisor.StateDir == home.StateDir {
		if preview {
			return nil
		}
		return discord.SelectState(home.StateDir, dir)
	}
	if cfg.PreviousSupervisor != nil {
		return errors.New("discord: this connector already retains a previous supervisor")
	}
	if skeleton.EndpointIDForStateDir(home.StateDir) == skeleton.EndpointIDForStateDir(cfg.Supervisor.StateDir) || home.SocketPath == cfg.DaemonSocket {
		return errors.New("discord: conflicting supervisor socket or state identity")
	}
	selected, err := discord.SelectedState(cfg.Supervisor.StateDir)
	if err != nil || selected != dir {
		return errors.New("discord: original supervisor does not select this connector")
	}
	cfg.PreviousSupervisor = &discord.PreviousSupervisor{SupervisorPaths: *cfg.Supervisor, Socket: cfg.DaemonSocket, Endpoint: skeleton.EndpointIDForStateDir(cfg.Supervisor.StateDir)}
	cfg.Supervisor = &discord.SupervisorPaths{StateDir: home.StateDir, Lock: home.LockPath, Log: home.LogPath}
	cfg.DaemonSocket = home.SocketPath
	if err = cfg.Validate(); err != nil {
		return err
	}
	if preview {
		return nil
	}
	if err = discord.SaveConfig(dir, cfg); err != nil {
		return err
	}
	// Repeating the command completes an interrupted selection after the atomic config write.
	return discord.SelectState(home.StateDir, dir)
}

func verifyMigrationEndpoint(ctx context.Context, socket, expected string) error {
	client, err := protocol.DialContext(ctx, socket, nil)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if client.EndpointID() != expected {
		return errors.New("discord: socket answered for a different supervisor")
	}
	return nil
}
