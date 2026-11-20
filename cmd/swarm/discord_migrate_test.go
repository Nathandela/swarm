package main

import (
	"github.com/Nathandela/swarm/internal/protocol"
	"net"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/skeleton"
)

func migrationCLI(t *testing.T) (string, string) {
	t.Helper()
	for _, key := range []string{daemon.EnvSocket, daemon.EnvLock, daemon.EnvLog} {
		t.Setenv(key, "")
	}
	return discordCLIRig(t)
}

func TestMigrateDiscordSupervisorPreservesEnrollmentAndIsRestartable(t *testing.T) {
	dir, token := migrationCLI(t)
	if code, _, err := cliDiscord(t, "connect", "--mode", "teammate", "--name", "W", "--application-id", "100", "--token-file", token, "--guild", "10"); code != 0 {
		t.Fatal(err)
	}
	original, err := discord.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv(daemon.EnvStateDir, home)
	for range 2 {
		if code, _, out := cliDiscord(t, "migrate-supervisor", "--state-dir", dir); code != 0 {
			t.Fatal(out)
		}
	}
	migrated, err := discord.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	legacy := migrated.PreviousSupervisor
	if legacy == nil || legacy.Socket != original.DaemonSocket || legacy.Endpoint != skeleton.EndpointIDForStateDir(original.Supervisor.StateDir) || legacy.SupervisorPaths != *original.Supervisor {
		t.Fatal("wrong original supervisor")
	}
	if migrated.Supervisor.StateDir != home || migrated.DaemonSocket != filepath.Join(home, "daemon.sock") {
		t.Fatal("wrong home")
	}
	for _, state := range []string{original.Supervisor.StateDir, home} {
		if selected, err := discord.SelectedState(state); err != nil || selected != dir {
			t.Fatal("lost state", state, err)
		}
	}
	migrated.PreviousSupervisor = nil
	migrated.Supervisor = original.Supervisor
	migrated.DaemonSocket = original.DaemonSocket
	if !reflect.DeepEqual(migrated, original) {
		t.Fatal("changed existing enrollment")
	}
	t.Setenv(daemon.EnvStateDir, t.TempDir())
	if code, _, _ := cliDiscord(t, "migrate-supervisor", "--state-dir", dir); code == 0 {
		t.Fatal("replaced existing route owner")
	}
}

func TestMigrateDiscordSupervisorRejectsOccupiedHome(t *testing.T) {
	dir, token := migrationCLI(t)
	if code, _, err := cliDiscord(t, "connect", "--mode", "teammate", "--name", "W", "--application-id", "100", "--token-file", token, "--guild", "10"); code != 0 {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv(daemon.EnvStateDir, home)
	if err := discord.SelectState(home, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := cliDiscord(t, "migrate-supervisor", "--state-dir", dir); code == 0 {
		t.Fatal("clobbered enrolled connector")
	}
}

func TestMigrateLegacyUsesAuthenticatedSourceAndSelector(t *testing.T) {
	// Short socket root for Linux sockaddr_un.
	dir, token := migrationCLI(t)
	if code, _, err := cliDiscord(t, "connect", "--mode", "teammate", "--name", "W", "--application-id", "100", "--token-file", token, "--guild", "10"); code != 0 {
		t.Fatal(err)
	}
	cfg, err := discord.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := *cfg.Supervisor
	cfg.Supervisor = nil
	if err := discord.SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv(daemon.EnvStateDir, home)
	args := []string{"migrate-supervisor", "--state-dir", dir, "--from-state-dir", old.StateDir, "--from-socket", cfg.DaemonSocket, "--from-lock", old.Lock, "--from-log", old.Log}
	if code, _, _ := cliDiscord(t, args...); code == 0 {
		t.Fatal("accepted an offline socket")
	}
	listen, err := net.Listen("unix", cfg.DaemonSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listen.Close() }()
	owner := protocol.NewServer(newScriptedPairingHost(), skeleton.EndpointIDForStateDir(old.StateDir))
	defer func() { _ = owner.Close() }()
	wrong := protocol.NewServer(newScriptedPairingHost(), "other-endpoint")
	defer func() { _ = wrong.Close() }()
	serve := func(srv *protocol.Server) {
		go func() {
			c, e := listen.Accept()
			if e == nil {
				serveFakePairingConn(srv, c)
			}
		}()
	}
	serve(wrong)
	if code, _, _ := cliDiscord(t, args...); code == 0 {
		t.Fatal("accepted wrong live endpoint")
	}
	serve(owner)
	if code, _, msg := cliDiscord(t, args...); code != 0 {
		t.Fatal(msg)
	}
	stored, err := discord.LoadConfig(dir)
	if err != nil || stored.PreviousSupervisor == nil || stored.PreviousSupervisor.StateDir != old.StateDir {
		t.Fatal("lost legacy identity", err)
	}
}

func TestProjectAccessOptInPersistsAndReverts(t *testing.T) {
	dir, token := migrationCLI(t)
	if code, _, err := cliDiscord(t, "connect", "--mode", "teammate", "--name", "W", "--application-id", "100", "--token-file", token, "--guild", "10", "--project", "alias="+filepath.Dir(dir)); code != 0 {
		t.Fatal(err)
	}
	load := func() discord.Config {
		cfg, err := discord.LoadConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	if load().ProjectAccess != discord.ProjectAccessVM {
		t.Fatal("new connector should use the VM policy")
	}
	historic := load()
	historic.ProjectAccess = ""
	if err := discord.SaveConfig(dir, historic); err != nil {
		t.Fatal(err)
	}
	if code, _, msg := cliDiscord(t, "connect", "--state-dir", dir); code != 0 {
		t.Fatal(msg)
	}
	if load().ProjectAccess != "" {
		t.Fatal("historic connector policy changed implicitly")
	}
	for _, policy := range []string{"vm", "enrolled"} {
		if code, _, msg := cliDiscord(t, "connect", "--state-dir", dir, "--project-access", policy); code != 0 {
			t.Fatal(msg)
		}
		cfg := load()
		if cfg.ProjectAccess != policy || cfg.Projects["alias"] != filepath.Dir(dir) {
			t.Fatal("policy change lost configured aliases")
		}
	}
	for _, policy := range []string{"all", ""} {
		if code, _, _ := cliDiscord(t, "connect", "--state-dir", dir, "--project-access", policy); code == 0 {
			t.Fatal("invalid policy accepted")
		}
	}
}

func TestMigrationDoesNotOverwriteConcurrentConnectorChange(t *testing.T) {
	dir, token := migrationCLI(t)
	if code, _, msg := cliDiscord(t, "connect", "--mode", "teammate", "--name", "W", "--application-id", "100", "--token-file", token, "--guild", "10"); code != 0 {
		t.Fatal(msg)
	}
	original, err := discord.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	release, err := lockDiscordConfiguration(dir, original.Supervisor.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	t.Setenv(daemon.EnvStateDir, t.TempDir())
	if code, _, _ := cliDiscord(t, "migrate-supervisor", "--state-dir", dir); code == 0 {
		t.Fatal("overwrote a concurrent enrollment change")
	}
	latest, err := discord.LoadConfig(dir)
	if err != nil || !reflect.DeepEqual(original, latest) {
		t.Fatal("active enrollment changed", err)
	}
}
